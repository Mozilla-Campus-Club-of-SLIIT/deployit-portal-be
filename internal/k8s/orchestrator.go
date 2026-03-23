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
	"strings"
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

	_, err = clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to create namespace: %v", err)
	}
	log.Printf("[K8S] Namespace %s created", config.Namespace)

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
					fmt.Sprintf("%s\ntail -f /dev/null", config.StartupScript),
				},
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
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "shared-data",
						MountPath: "/data",
					},
				},
			},
			{
				Name:  "terminal-sidecar",
				Image: "alpine:latest",
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
				Ports: []corev1.ContainerPort{{ContainerPort: 9000}},
				Command: []string{"/bin/sh", "-c"},
				Args: []string{
					"apk add --no-cache curl kubectl bash > /dev/null && " +
						"curl -fsSL -L -o /usr/local/bin/ttyd https://github.com/tsl0922/ttyd/releases/download/1.7.7/ttyd.x86_64 && " +
						"chmod +x /usr/local/bin/ttyd && " +
						"ttyd -W -p 9000 bash -c \"echo 'Connecting to lab environment...'; kubectl exec -it $POD_NAME -c challenge-container -- bash || kubectl exec -it $POD_NAME -c challenge-container -- sh\"",
				},
				SecurityContext: &corev1.SecurityContext{
					Privileged:               boolPtr(false),
					AllowPrivilegeEscalation: boolPtr(false),
				},
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("50m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("10m"),
						corev1.ResourceMemory: resource.MustParse("64Mi"),
					},
				},
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
					InitialDelaySeconds: 5,
					PeriodSeconds:       2,
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
				// Check ONLY the terminal sidecar status
				terminalReady := false
				for _, stat := range pod.Status.ContainerStatuses {
					if stat.Name == "terminal-sidecar" && stat.Ready {
						terminalReady = true
						break
					}
				}
				if terminalReady {
					return pod.Name, nil
				}
				log.Printf("[K8S] Pod Running, waiting for terminal-sidecar on port 8080...")
			}
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(1 * time.Second):
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

	cmd := []string{"bash", "-c", script}

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

	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line, nil
		}
	}
	return "", nil
}
func boolPtr(b bool) *bool { return &b }
