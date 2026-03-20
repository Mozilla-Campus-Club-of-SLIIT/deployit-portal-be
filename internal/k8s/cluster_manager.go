package k8s

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"google.golang.org/api/container/v1"
	"google.golang.org/api/option"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type ClusterManager struct {
	service    *container.Service
	projectID  string
	region     string
	clientset  *kubernetes.Clientset
	lastActive time.Time
	isCreating bool
}

func NewClusterManager(ctx context.Context) (*ClusterManager, error) {
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	region := os.Getenv("GOOGLE_CLOUD_REGION")
	if region == "" {
		region = "us-central1"
	}

	var opts []option.ClientOption
	jsonBytes := getK8sCredentialsJSON()
	if len(jsonBytes) > 0 {
		opts = append(opts, option.WithCredentialsJSON(jsonBytes))
	}

	svc, err := container.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create GKE service: %v", err)
	}

	cm := &ClusterManager{
		service:    svc,
		projectID:  projectID,
		region:     region,
		lastActive: time.Now(),
	}

	return cm, nil
}

func (cm *ClusterManager) GetClientset(ctx context.Context) (*kubernetes.Clientset, error) {
	if cm.clientset != nil {
		cm.lastActive = time.Now()
		return cm.clientset, nil
	}

	// Check if an active cluster already exists in the region
	cluster, err := cm.findActiveCluster(ctx)
	if err != nil {
		if cm.isCreating {
			return nil, fmt.Errorf("cluster is currently being provisioned")
		}
		return nil, fmt.Errorf("no active cluster found")
	}

	if cluster.Status == "PROVISIONING" || cluster.Status == "RECONCILING" {
		cm.isCreating = true
		return nil, fmt.Errorf("cluster is currently being provisioned")
	}

	if cluster.Status != "RUNNING" {
		return nil, fmt.Errorf("cluster is in status: %s", cluster.Status)
	}

	// Cluster is running, build clientset
	clientset, err := cm.buildClientset(cluster)
	if err != nil {
		return nil, err
	}

	cm.clientset = clientset
	cm.lastActive = time.Now()
	return clientset, nil
}

func (cm *ClusterManager) EnsureCluster(ctx context.Context) error {
	cluster, err := cm.findActiveCluster(ctx)
	if err == nil {
		if cluster.Status == "RUNNING" {
			cm.lastActive = time.Now()
			cm.isCreating = false
			return nil
		}
		if cluster.Status == "PROVISIONING" || cluster.Status == "RECONCILING" {
			cm.isCreating = true
			return nil
		}
	}

	// Automatic creation disabled as per user request.
	// Use scripts/k8s_cluster_create.go to provision the infrastructure.
	return fmt.Errorf("No active GKE cluster found. Infrastructure must be warmed up manually by an admin.")
}

// ManualCreateCluster for use by CLI scripts
func (cm *ClusterManager) ManualCreateCluster(ctx context.Context) error {
	cm.isCreating = true
	clusterName := fmt.Sprintf("dynamic-challenge-%d", time.Now().Unix())
	log.Printf("[GKE] Manually initiating cluster creation: %s...", clusterName)
	
	req := &container.CreateClusterRequest{
		Cluster: &container.Cluster{
			Name:             clusterName,
			InitialNodeCount: 1,
			NodeConfig: &container.NodeConfig{
				MachineType: "e2-medium",
				Spot:        true,
				Labels: map[string]string{
					"cloud.google.com/gke-spot": "true",
				},
			},
		},
	}

	parent := fmt.Sprintf("projects/%s/locations/%s", cm.projectID, cm.region)
	_, err := cm.service.Projects.Locations.Clusters.Create(parent, req).Context(ctx).Do()
	return err
}

// ManualDeleteClusters for use by CLI scripts
func (cm *ClusterManager) ManualDeleteClusters(ctx context.Context) error {
	parent := fmt.Sprintf("projects/%s/locations/%s", cm.projectID, cm.region)
	resp, err := cm.service.Projects.Locations.Clusters.List(parent).Context(ctx).Do()
	if err != nil {
		return err
	}

	for _, c := range resp.Clusters {
		if strings.HasPrefix(c.Name, "dynamic-challenge-") {
			log.Printf("[GKE] Manually deleting cluster: %s...", c.Name)
			name := fmt.Sprintf("%s/clusters/%s", parent, c.Name)
			_, err := cm.service.Projects.Locations.Clusters.Delete(name).Context(ctx).Do()
			if err != nil {
				log.Printf("[ERROR] Failed to delete %s: %v", c.Name, err)
			}
		}
	}
	return nil
}

func (cm *ClusterManager) DeleteClusterIfIdle(ctx context.Context, idleMinutes int) error {
	// We want to cleanup ANY idle cluster with our prefix
	parent := fmt.Sprintf("projects/%s/locations/%s", cm.projectID, cm.region)
	resp, err := cm.service.Projects.Locations.Clusters.List(parent).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to list clusters for deletion check: %v", err)
	}

	for _, c := range resp.Clusters {
		if !strings.HasPrefix(c.Name, "dynamic-challenge-") {
			continue
		}

		// Only delete if it's been idle. Since we don't have per-cluster lastActive markers easily persistent here,
		// we use the singleton's lastActive as a global proxy.
		// If another user joined, lastActive was updated.
		if time.Since(cm.lastActive).Minutes() >= float64(idleMinutes) {
			log.Printf("[GKE] Dynamic cluster %s idle for %d minutes. Initiating deletion...", c.Name, idleMinutes)
			name := fmt.Sprintf("%s/clusters/%s", parent, c.Name)
			_, err := cm.service.Projects.Locations.Clusters.Delete(name).Context(ctx).Do()
			if err != nil {
				log.Printf("[ERROR] Failed to delete cluster %s: %v", c.Name, err)
			}
			cm.clientset = nil
			cm.isCreating = false
		}
	}
	return nil
}

func (cm *ClusterManager) findActiveCluster(ctx context.Context) (*container.Cluster, error) {
	parent := fmt.Sprintf("projects/%s/locations/%s", cm.projectID, cm.region)
	resp, err := cm.service.Projects.Locations.Clusters.List(parent).Context(ctx).Do()
	if err != nil {
		return nil, err
	}

	for _, c := range resp.Clusters {
		if strings.HasPrefix(c.Name, "dynamic-challenge-") {
			// Ignore clusters that are stopping or deleting
			if c.Status != "STOPPING" && c.Status != "DELETING" {
				return c, nil
			}
		}
	}

	return nil, fmt.Errorf("no active cluster")
}

func (cm *ClusterManager) GetConfig(ctx context.Context) (*rest.Config, error) {
	cluster, err := cm.findActiveCluster(ctx)
	if err != nil {
		return nil, err
	}
	return cm.buildConfig(cluster)
}

func (cm *ClusterManager) buildClientset(c *container.Cluster) (*kubernetes.Clientset, error) {
	config, err := cm.buildConfig(c)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

func (cm *ClusterManager) buildConfig(c *container.Cluster) (*rest.Config, error) {
	cap, err := base64.StdEncoding.DecodeString(c.MasterAuth.ClusterCaCertificate)
	if err != nil {
		return nil, fmt.Errorf("failed to decode cluster CA: %v", err)
	}

	// Get a token source using explicit credentials if available, otherwise fallback to ADC
	var ts oauth2.TokenSource
	jsonBytes := getK8sCredentialsJSON()
	if len(jsonBytes) > 0 {
		creds, err := google.CredentialsFromJSON(context.Background(), jsonBytes, "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return nil, fmt.Errorf("failed to parse credentials: %v", err)
		}
		ts = creds.TokenSource
	} else {
		ts, err = google.DefaultTokenSource(context.Background(), "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return nil, fmt.Errorf("failed to get default token source: %v", err)
		}
	}

	config := &rest.Config{
		Host: "https://" + c.Endpoint,
		TLSClientConfig: rest.TLSClientConfig{
			CAData: cap,
		},
	}

	// Wrapper to inject the dynamic token into the transport
	config.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return &tokenRoundTripper{
			rt: rt,
			ts: ts,
		}
	})

	return config, nil
}

type tokenRoundTripper struct {
	rt http.RoundTripper
	ts oauth2.TokenSource
}

func (t *tokenRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := t.ts.Token()
	if err != nil {
		return nil, err
	}
	newReq := req.Clone(req.Context())
	newReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	return t.rt.RoundTrip(newReq)
}

func (cm *ClusterManager) MarkActive() {
	cm.lastActive = time.Now()
}

func getK8sCredentialsJSON() []byte {
	privateKey := strings.ReplaceAll(os.Getenv("GCP_SA_PRIVATE_KEY"), "\\n", "\n")
	if os.Getenv("GCP_SA_PROJECT_ID") == "" || privateKey == "" {
		return nil
	}
	m := map[string]string{
		"type":           os.Getenv("GCP_SA_TYPE"),
		"project_id":     os.Getenv("GCP_SA_PROJECT_ID"),
		"private_key_id": os.Getenv("GCP_SA_PRIVATE_KEY_ID"),
		"private_key":    privateKey,
		"client_email":   os.Getenv("GCP_SA_CLIENT_EMAIL"),
		"client_id":      os.Getenv("GCP_SA_CLIENT_ID"),
		"auth_uri":       os.Getenv("GCP_SA_AUTH_URI"),
		"token_uri":      os.Getenv("GCP_SA_TOKEN_URI"),
	}
	b, _ := json.Marshal(m)
	return b
}
