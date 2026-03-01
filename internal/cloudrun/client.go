package cloudrun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
)

type CloudRunClient struct {
	client     *run.ServicesClient
	httpClient *http.Client
	projectID  string
	region     string
}

func getCredentialsJSON() []byte {
	// Normalize newlines in the private key from .env string
	privateKey := strings.ReplaceAll(os.Getenv("GCP_SA_PRIVATE_KEY"), "\\n", "\n")
	if os.Getenv("GCP_SA_PROJECT_ID") == "" {
		return nil
	}
	m := map[string]string{
		"type":                        os.Getenv("GCP_SA_TYPE"),
		"project_id":                  os.Getenv("GCP_SA_PROJECT_ID"),
		"private_key_id":              os.Getenv("GCP_SA_PRIVATE_KEY_ID"),
		"private_key":                 privateKey,
		"client_email":                os.Getenv("GCP_SA_CLIENT_EMAIL"),
		"client_id":                   os.Getenv("GCP_SA_CLIENT_ID"),
		"auth_uri":                    os.Getenv("GCP_SA_AUTH_URI"),
		"token_uri":                   os.Getenv("GCP_SA_TOKEN_URI"),
		"auth_provider_x509_cert_url": os.Getenv("GCP_SA_AUTH_PROVIDER_X509_CERT_URL"),
		"client_x509_cert_url":        os.Getenv("GCP_SA_CLIENT_X509_CERT_URL"),
		"universe_domain":             os.Getenv("GCP_SA_UNIVERSE_DOMAIN"),
	}
	b, _ := json.Marshal(m)
	return b
}

func NewCloudRunClient() (*CloudRunClient, error) {
	ctx := context.Background()

	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		fmt.Println("[WARNING] GOOGLE_CLOUD_PROJECT is not set, falling back to 'my-project-id'. Please set this environment variable for Cloud Run deployments!")
		projectID = "my-project-id"
	}
	region := os.Getenv("GOOGLE_CLOUD_REGION")
	if region == "" {
		region = "us-central1"
	}

	var client *run.ServicesClient
	var httpClient *http.Client
	var err error

	jsonBytes := getCredentialsJSON()
	if len(jsonBytes) > 0 {
		client, err = run.NewServicesClient(ctx, option.WithCredentialsJSON(jsonBytes))
		if err != nil {
			fmt.Printf("[ERROR] Could not build Cloud Run SDK client from env JSON: %v\n", err)
		}

		creds, err2 := google.CredentialsFromJSON(ctx, jsonBytes, "https://www.googleapis.com/auth/cloud-platform")
		if err2 != nil {
			fmt.Printf("[ERROR] Could not parse Credentials for IAM setup from env JSON: %v\n", err2)
		} else {
			// Get an HTTP client using the custom credentials
			httpClient = oauth2.NewClient(ctx, creds.TokenSource)
		}
	} else {
		// Fallback to ADC
		client, err = run.NewServicesClient(ctx)
		if err != nil {
			fmt.Printf("[ERROR] Could not build Cloud Run SDK client: %v\n", err)
		}

		httpClient, err = google.DefaultClient(ctx, "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			fmt.Printf("[ERROR] Could not build DefaultClient for IAM setup: %v\n", err)
		}
	}

	return &CloudRunClient{
		client:     client,
		httpClient: httpClient,
		projectID:  projectID,
		region:     region,
	}, nil
}

// CreateLabContainer creates the Cloud Run service and makes it publicly accessible
// Returns the direct HTTPS URL to access the container
func (c *CloudRunClient) CreateLabContainer(sessionID string, flag string) (string, error) {
	if c.client == nil || c.httpClient == nil {
		return "", fmt.Errorf("Cloud Run Client is not fully initialized. Have you configured application default credentials? Try: gcloud auth application-default login")
	}

	ctx := context.Background()
	serviceName := fmt.Sprintf("lab-session-%s", sessionID)
	// Must match regex `^[a-z]([-a-z0-9]*[a-z0-9])?$`
	parent := fmt.Sprintf("projects/%s/locations/%s", c.projectID, c.region)

	fmt.Printf("[CLOUDRUN] Deploying service %s to %s...\n", serviceName, parent)

	req := &runpb.CreateServiceRequest{
		Parent:    parent,
		ServiceId: serviceName,
		Service: &runpb.Service{
			Template: &runpb.RevisionTemplate{
				Annotations: map[string]string{
					"run.googleapis.com/startup-cpu-boost": "false",
				},
				Containers: []*runpb.Container{
					{
						Image:   "tsl0922/ttyd:latest",
						Command: []string{"bash"},
						Args: []string{
							"-c",
							fmt.Sprintf("echo '%s' > /root/flag.txt && exec ttyd -p 8080 -W -o bash", flag),
						},
						Ports: []*runpb.ContainerPort{
							{ContainerPort: 8080},
						},
						Resources: &runpb.ResourceRequirements{
							Limits: map[string]string{
								"cpu":    "1000m",
								"memory": "512Mi",
							},
						},
					},
				},
				Scaling: &runpb.RevisionScaling{
					MinInstanceCount: 0,
					MaxInstanceCount: 1,
				},
			},
		},
	}

	op, err := c.client.CreateService(ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to call CreateService API: %v", err)
	}

	fmt.Println("[CLOUDRUN] Waiting for Cloud Run service deployment (this takes 15-40 seconds)...")
	svc, err := op.Wait(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to deploy Cloud Run service: %v", err)
	}

	// Make the service unauthenticated so frontend IFRAME can reach it directly
	err = c.MakeServicePublic(svc.Name)
	if err != nil {
		fmt.Printf("[CLOUDRUN] WARNING: Failed to make service public. The lab might be unaccessible from the browser without auth headers: %v\n", err)
	}

	url := svc.Uri
	fmt.Printf("[CLOUDRUN] Lab session started successfully on Cloud Run! URL: %s\n", url)
	return url, nil
}

// DeleteLabContainer removes the Cloud Run service via the SDK
func (c *CloudRunClient) DeleteLabContainer(sessionID string) {
	if c.client == nil {
		return
	}
	ctx := context.Background()
	serviceName := fmt.Sprintf("projects/%s/locations/%s/services/lab-session-%s", c.projectID, c.region, sessionID)
	req := &runpb.DeleteServiceRequest{Name: serviceName}

	fmt.Printf("[CLOUDRUN] Deleting lab session %s...\n", serviceName)
	op, err := c.client.DeleteService(ctx, req)
	if err == nil {
		go func() {
			_, waitErr := op.Wait(context.Background())
			if waitErr != nil {
				fmt.Printf("[CLOUDRUN] Background delete wait failed for %s: %v\n", serviceName, waitErr)
			} else {
				fmt.Printf("[CLOUDRUN] Successfully deleted Cloud Run service %s\n", serviceName)
			}
		}()
	} else {
		fmt.Printf("[CLOUDRUN] Failed to issue delete request for %s: %v\n", serviceName, err)
	}
}

// MakeServicePublic hits the IAM REST endpoints manually to apply roles/run.invoker to allUsers
func (c *CloudRunClient) MakeServicePublic(serviceName string) error {
	// API V1 endpoint for IAM: https://run.googleapis.com/v1/{resource}:setIamPolicy
	url := fmt.Sprintf("https://run.googleapis.com/v1/%s:setIamPolicy", serviceName)

	// Create JSON payload
	payload := []byte(`{
		"policy": {
			"bindings": [
				{
					"role": "roles/run.invoker",
					"members": ["allUsers"]
				}
			]
		}
	}`)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("IAM API returned status %d", resp.StatusCode)
	}

	return nil
}
