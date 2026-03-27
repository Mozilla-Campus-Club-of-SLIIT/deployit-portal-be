package k8s

import (
	"context"
	"fmt"
	"log"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"bytes"
	"os"
	"strings"
	"encoding/base64"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

type K8sClient struct {
	ClusterMgr *ClusterManager
}

func NewK8sClient(ctx context.Context) (*K8sClient, error) {
	cm, err := NewClusterManager(ctx)
	if err != nil {
		return nil, err
	}
	return &K8sClient{ClusterMgr: cm}, nil
}

type ChallengeConfig struct {
	Namespace     string
	ChallengeID   string
	UserID        string
	ExpiryHours   float64
	CPUQuota      string
	MemoryQuota   string
	PodQuota      string
	Image         string
	StartupScript string
	ConfigFiles   map[string]string
}

func (c *K8sClient) ProvisionChallenge(ctx context.Context, config *ChallengeConfig) (string, error) {
	// Ensure cluster exists
	err := c.ClusterMgr.EnsureCluster(ctx)
	if err != nil {
		return "", err
	}

	clientset, err := c.ClusterMgr.GetClientset(ctx)
	if err != nil {
		return "", err
	}

	// 1. Create Namespace
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: config.Namespace,
			Labels: map[string]string{
				"managed-by":   "deployit-orchestrator",
				"challenge-id": config.ChallengeID,
				"user-id":      config.UserID,
			},
			Annotations: map[string]string{
				"expires-at": time.Now().Add(time.Duration(float64(time.Hour) * config.ExpiryHours)).Format(time.RFC3339),
			},
		},
	}

	// 1. Create Namespace (with retry for transient Control Plane timeouts)
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		_, err = clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		if err == nil {
			log.Printf("[K8S] Namespace %s created (Attempt %d)", config.Namespace, attempt)
			goto NS_SUCCESS
		}
		lastErr = err
		log.Printf("[K8S] Warning: Namespace creation failed (Attempt %d): %v. Retrying in %ds...", attempt, err, attempt*2)
		time.Sleep(time.Duration(attempt*2) * time.Second)
	}
	return "", fmt.Errorf("failed to create namespace after 3 attempts: %v", lastErr)

NS_SUCCESS:

	// 2. Apply Resource Quotas (with headroom for terminal sidecar)
	// Sidecar uses 200m CPU and 256Mi Memory. We add 500m and 512Mi total headroom.
	hardLimits := corev1.ResourceList{
		corev1.ResourcePods: resource.MustParse(config.PodQuota),
	}

	_, err = clientset.CoreV1().ResourceQuotas(config.Namespace).Create(ctx, &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "challenge-quota"},
		Spec: corev1.ResourceQuotaSpec{
			Hard: hardLimits,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to create quota: %v", err)
	}
	log.Printf("[K8S] Quota applied to %s: Pods=%s", config.Namespace, config.PodQuota)

	// Also create a LimitRange so user-deployed pods without limits aren't rejected
	_, err = clientset.CoreV1().LimitRanges(config.Namespace).Create(ctx, &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: "default-limits"},
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{
				{
					Type: corev1.LimitTypeContainer,
					DefaultRequest: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("10m"),
						corev1.ResourceMemory: resource.MustParse("32Mi"),
					},
					Default: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("50m"),
						corev1.ResourceMemory: resource.MustParse("64Mi"),
					},
				},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to create limit range: %v", err)
	}

	// 3. Create Service Account & RBAC for the user
	saName := "participant-sa"
	_, err = clientset.CoreV1().ServiceAccounts(config.Namespace).Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: saName},
	}, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to create service account: %v", err)
	}

	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "participant-full-access"},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"", "apps", "batch", "networking.k8s.io"},
				Resources: []string{"*"},
				Verbs:     []string{"*"},
			},
		},
	}
	_, err = clientset.RbacV1().Roles(config.Namespace).Create(ctx, role, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to create role: %v", err)
	}

	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "participant-binding"},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      saName,
				Namespace: config.Namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "Role",
			Name:     "participant-full-access",
			APIGroup: "rbac.authorization.k8s.io",
		},
	}
	_, err = clientset.RbacV1().RoleBindings(config.Namespace).Create(ctx, rb, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to create rolebinding: %v", err)
	}

	// 4. Deploy Challenge Resources with Terminal Sidecar
	podSpec := corev1.PodSpec{
		ServiceAccountName: saName, // Allow terminal to use kubectl
		Containers: []corev1.Container{
			{
				Name:  "challenge-container",
				Image: config.Image,
				// Run startup script if provided, but ALWAYS keep container alive with tail -f /dev/null
				Command: []string{"/bin/sh", "-c"},
				Args: []string{
					fmt.Sprintf(`export TERM=xterm-256color; export LANG=C.UTF-8; export LC_ALL=C.UTF-8
# 1. Provide service/systemctl shims for minimal container images
cat <<'EOF' > /usr/local/bin/service
#!/bin/sh
SERVICE=$1
ACTION=$2
if [ -z "$SERVICE" ] || [ -z "$ACTION" ]; then
	echo "Usage: service <service> {start|stop|restart|status}"
	exit 1
fi
if [ -x "/etc/init.d/$SERVICE" ]; then
	/etc/init.d/$SERVICE $ACTION
elif command -v rc-service >/dev/null 2>&1; then
	rc-service $SERVICE $ACTION
else
	echo "Service $SERVICE not found (no init script/rc-service available)."
	exit 1
fi
EOF
chmod +x /usr/local/bin/service
cat <<'EOF' > /usr/local/bin/systemctl
#!/bin/sh
ACTION=$1
SERVICE=$2
if [ -z "$SERVICE" ] || [ -z "$ACTION" ]; then
  echo "Usage: systemctl {start|stop|restart|status} {service}"
  exit 1
fi
service $SERVICE $ACTION
EOF
chmod +x /usr/local/bin/systemctl
# 2. Signal readiness immediately
touch /data/provisioned
# 3. Link kubectl from sidecar background-ready
(while [ ! -f /data/kubectl ]; do sleep 0.1; done; ln -sf /data/kubectl /usr/local/bin/kubectl) &
# 4. Apply Config Files
%s
# 5. Run the challenge-specific startup script in the background
STARTUP_SCRIPT="%s"
if [ -n "$STARTUP_SCRIPT" ]; then
  (eval "$STARTUP_SCRIPT") >>/data/provision_log 2>&1 &
fi
# 6. Keep container alive
tail -f /dev/null`, buildK8sConfigFiles(config.ConfigFiles), config.StartupScript),
				},
				Stdin: true,
				TTY:   true,
				SecurityContext: &corev1.SecurityContext{
					Privileged:               boolPtr(false),
					AllowPrivilegeEscalation: boolPtr(false),
				},
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse(config.CPUQuota),
						corev1.ResourceMemory: resource.MustParse(config.MemoryQuota),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("10m"),
						corev1.ResourceMemory: resource.MustParse("64Mi"),
					},
				},
				ImagePullPolicy: corev1.PullIfNotPresent,
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "shared-data",
						MountPath: "/data",
					},
				},
			},
			{
				Name: "terminal-sidecar",
				// Pre-baked image is auto-derived from GOOGLE_CLOUD_PROJECT.
				// Built & pushed by CI: .github/workflows/build-sidecar.yml
				// Falls back to alpine:latest (installs at runtime) when running locally without GCP.
				Image: func() string {
					if img := os.Getenv("K8S_SIDECAR_IMAGE"); img != "" {
						return img
					}
					if proj := os.Getenv("GOOGLE_CLOUD_PROJECT"); proj != "" {
						return "gcr.io/" + proj + "/deployit-lab-sidecar:latest"
					}
					return "alpine:latest"
				}(),
				Env: []corev1.EnvVar{
					{
						Name: "POD_NAME",
						ValueFrom: &corev1.EnvVarSource{
							FieldRef: &corev1.ObjectFieldSelector{
								FieldPath: "metadata.name",
							},
						},
					},
				},
				Ports: []corev1.ContainerPort{
					{ContainerPort: 9000},
					{ContainerPort: 9001},
					{ContainerPort: 9002},
				},
				Command: []string{"/bin/sh", "-c"},
				Args: []string{func() string {
					// Bind explicitly to all interfaces so kubelet TCP readiness probe can reach ttyd.
					const ttydBase = `ttyd -i 0.0.0.0 -W -t disableLeaveAlert=true -t fontSize=14 sh -c "stty sane; kubectl exec -it $POD_NAME -c challenge-container -- sh -c 'export EDITOR=nano; if command -v bash >/dev/null 2>&1; then exec bash -i; else exec sh -i; fi'"`
					
					// Run 3 instances on ports 9000, 9001, 9002
					script := fmt.Sprintf(`(cp /usr/local/bin/kubectl /data/kubectl) >/dev/null 2>&1
%s -p 9000 &
%s -p 9001 &
%s -p 9002 &
wait`, ttydBase, ttydBase, ttydBase)
					return script
				}()},
				SecurityContext: &corev1.SecurityContext{
					Privileged:               boolPtr(false),
					AllowPrivilegeEscalation: boolPtr(false),
				},
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("50m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
				},
				Stdin: true,
				TTY:   true,
				ImagePullPolicy: corev1.PullIfNotPresent,
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "shared-data",
						MountPath: "/data",
					},
				},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						TCPSocket: &corev1.TCPSocketAction{
							Port: intstr.FromInt(9000),
						},
					},
					InitialDelaySeconds: 20, // Huge increase: ttyd wget download can take 15s on slow node initializations
					PeriodSeconds:       5,
					FailureThreshold:    6, 
				},
			},
		},
		Volumes: []corev1.Volume{
			{
				Name: "shared-data",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
		},
		Tolerations: []corev1.Toleration{
			{
				Key:      "cloud.google.com/gke-spot",
				Operator: corev1.TolerationOpEqual,
				Value:    "true",
				Effect:   corev1.TaintEffectNoSchedule,
			},
		},
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "challenge-app",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "challenge-app"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "challenge-app"},
				},
				Spec: podSpec,
			},
		},
	}

	_, err = clientset.AppsV1().Deployments(config.Namespace).Create(ctx, deployment, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to create challenge deployment: %v", err)
	}

	// 5. Create Service to expose the terminal
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "terminal-service",
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "challenge-app"},
			Ports: []corev1.ServicePort{
				{
					Port: 80,
					TargetPort: intstr.FromInt(8080),
				},
			},
		},
	}
	_, err = clientset.CoreV1().Services(config.Namespace).Create(ctx, service, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to create terminal service: %v", err)
	}
	log.Printf("[K8S] Resources successfully provisioned in %s. Waiting for pod readiness...", config.Namespace)

	// 6. BLOCK until pod is ready (up to 60s)
	_, err = c.FindPod(ctx, config.Namespace, "app=challenge-app")
	if err != nil {
		return "", fmt.Errorf("pod readiness timeout: %v", err)
	}
	log.Printf("[K8S] Pod ready in %s. Lab is now accessible.", config.Namespace)

	return config.Namespace, nil
}

func (c *K8sClient) FindPod(ctx context.Context, namespace string, labelSelector string) (string, error) {
	clientset, err := c.ClusterMgr.GetClientset(ctx)
	if err != nil {
		return "", err
	}

	strictTerminalReadiness := true
	if v := strings.TrimSpace(strings.ToLower(os.Getenv("K8S_REQUIRE_TERMINAL_READY"))); v == "false" || v == "0" || v == "no" {
		strictTerminalReadiness = false
	}

	log.Printf("[K8S] Searching for pods in %s with selector %s (max 300s)...", namespace, labelSelector)

	// Retry for up to 300 seconds to find a running pod
	for i := 0; i < 300; i++ {
		pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		
		if err != nil {
			log.Printf("[K8S] [Attempt %d] Error listing pods: %v", i+1, err)
		} else if len(pods.Items) == 0 {
			log.Printf("[K8S] [Attempt %d] No pods found in namespace %s yet...", i+1, namespace)
		} else {
			pod := pods.Items[0]
			log.Printf("[K8S] [Attempt %d] Found pod %s in state %s", i+1, pod.Name, pod.Status.Phase)
			if pod.Status.Phase == corev1.PodRunning {
				challengeRunning := false
				terminalFound := false
				terminalReady := false
				for _, stat := range pod.Status.ContainerStatuses {
					if stat.Name == "challenge-container" && stat.State.Running != nil {
						challengeRunning = true
					}
					if stat.Name == "terminal-sidecar" {
						terminalFound = true
						if stat.Ready {
							terminalReady = true
						}
					}
				}
				// Check if any container has actually crashed
				for _, stat := range pod.Status.ContainerStatuses {
					if stat.State.Terminated != nil && stat.State.Terminated.ExitCode != 0 {
						return "", fmt.Errorf("container %s crashed: %s (exit %d)", 
							stat.Name, stat.State.Terminated.Reason, stat.State.Terminated.ExitCode)
					}
				}

				if terminalReady {
					log.Printf("[K8S] Pod ready in %s. Terminal is now accessible.", namespace)
					return pod.Name, nil
				}

				if !strictTerminalReadiness && challengeRunning {
					if terminalFound {
						log.Printf("[K8S] Pod ready in %s (relaxed mode): challenge container running; terminal sidecar still warming.", namespace)
					} else {
						log.Printf("[K8S] Pod ready in %s (relaxed mode): challenge container running.", namespace)
					}
					return pod.Name, nil
				}
				
				// Log why it's not ready (e.g., ImagePulling, ContainerCreating)
				for _, stat := range pod.Status.ContainerStatuses {
					if !stat.Ready && stat.State.Waiting != nil {
						log.Printf("[K8S] Container %s: %s - %s", stat.Name, stat.State.Waiting.Reason, stat.State.Waiting.Message)
					} else if !stat.Ready && stat.State.Running != nil {
						log.Printf("[K8S] Container %s running but not ready yet (restarts=%d)", stat.Name, stat.RestartCount)
					}
				}
			}
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
			// continue loop
		}
	}

	return "", fmt.Errorf("timeout waiting for running and ready pod with selector %s in %s", labelSelector, namespace)
}

func (c *K8sClient) GetRestConfig(ctx context.Context) (*rest.Config, error) {
	return c.ClusterMgr.GetConfig(ctx)
}

func int32Ptr(i int32) *int32 { return &i }

func (c *K8sClient) DeleteNamespace(ctx context.Context, namespace string) error {
	log.Printf("[K8S] Deleting namespace %s...", namespace)
	clientset, err := c.ClusterMgr.GetClientset(ctx)
	if err != nil {
		return err
	}
	return clientset.CoreV1().Namespaces().Delete(ctx, namespace, metav1.DeleteOptions{})
}

func (c *K8sClient) CleanupExpiredNamespaces(ctx context.Context) (int, error) {
	clientset, err := c.ClusterMgr.GetClientset(ctx)
	if err != nil {
		return 0, err
	}

	list, err := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: "managed-by=deployit-orchestrator",
	})
	if err != nil {
		return 0, err
	}

	deletedCount := 0
	now := time.Now()
	for _, ns := range list.Items {
		expiryStr, ok := ns.Annotations["expires-at"]
		if !ok {
			continue
		}
		expiryTime, err := time.Parse(time.RFC3339, expiryStr)
		if err != nil {
			continue
		}

		if now.After(expiryTime) {
			err := c.DeleteNamespace(ctx, ns.Name)
			if err == nil {
				deletedCount++
			}
		}
	}
	return deletedCount, nil
}

func (c *K8sClient) MarkClusterActive() {
	c.ClusterMgr.MarkActive()
}

func (c *K8sClient) DeleteClusterIfIdle(ctx context.Context, idleMinutes int) error {
	return c.ClusterMgr.DeleteClusterIfIdle(ctx, idleMinutes)
}

// EvaluateScript runs a custom bash script non-interactively using kubectl exec
func (c *K8sClient) EvaluateScript(ctx context.Context, namespace, script string) (string, error) {
	podName, err := c.FindPod(ctx, namespace, "app=challenge-app")
	if err != nil {
		return "", fmt.Errorf("failed to find pod for evaluation: %v", err)
	}

	clientset, err := c.ClusterMgr.GetClientset(ctx)
	if err != nil {
		return "", err
	}
	config, err := c.ClusterMgr.GetConfig(ctx)
	if err != nil {
		return "", err
	}

	cmd := []string{"sh", "-c", "if command -v bash >/dev/null 2>&1; then exec bash -c \"$1\"; else exec sh -c \"$1\"; fi", "--", script}

	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command:   cmd,
			Container: "challenge-container",
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("failed to initialize executor: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})

	output := stdout.String()
	if err != nil {
		output += "\n" + stderr.String()
		return output, fmt.Errorf("script execution failed: %v", err)
	}

	return strings.TrimSpace(output), nil
}
func boolPtr(b bool) *bool { return &b }

func buildK8sConfigFiles(files map[string]string) string {
	if len(files) == 0 {
		return ""
	}
	var script strings.Builder
	for path, content := range files {
		b64 := base64.StdEncoding.EncodeToString([]byte(content))
		script.WriteString(fmt.Sprintf("mkdir -p $(dirname '%s') && base64 -d << 'EOF_B64' > '%s'\n%s\nEOF_B64\n", path, path, b64))
	}
	return script.String()
}
