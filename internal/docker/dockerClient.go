package docker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// DockerClient wraps the official Docker SDK client
type DockerClient struct {
	cli *client.Client
}

// CreateLabContainer creates a new Ubuntu 22.04 container for a lab session
// Returns container ID and the random port number mapped to ttyd
func (dc *DockerClient) CreateLabContainer(hostPort int) (string, int, error) {
	ctx := context.Background()
	image := "ubuntu:22.04"

	// Only allow official Ubuntu image
	if image != "ubuntu:22.04" {
		return "", 0, fmt.Errorf("invalid image")
	}

	// Pull image if not present
	reader, err := dc.cli.ImagePull(ctx, image, types.ImagePullOptions{})
	if err != nil {
		fmt.Printf("[ERROR] failed to pull image: %v\n", err)
		return "", 0, fmt.Errorf("failed to pull image: %w", err)
	}
	defer reader.Close()
	// Read the response to ensure the pull completes
	_, _ = io.ReadAll(reader)

	containerPort := "7681" // ttyd default

	// Install ttyd and run it
	// Install ttyd and run it (static binary for reliability)
	cmd := []string{"bash", "-c", "set -e; export DEBIAN_FRONTEND=noninteractive; apt-get update; apt-get install -y --no-install-recommends wget ca-certificates; wget -O /usr/local/bin/ttyd https://github.com/tsl0922/ttyd/releases/download/1.7.4/ttyd.x86_64; chmod +x /usr/local/bin/ttyd; export TERM=xterm-256color; exec ttyd -p 7681 -o bash -i"}

	config := &container.Config{
		Image: image,
		Cmd: cmd,
		Tty: true,
		ExposedPorts: nat.PortSet{nat.Port(containerPort + "/tcp"): struct{}{}},
	}

	pidsLimit := int64(128)
	hostConfig := &container.HostConfig{
		AutoRemove: false,
		Resources: container.Resources{
			NanoCPUs: 200_000_000, // 0.2 CPU
			Memory: 256 * 1024 * 1024, // 256MB
			PidsLimit: &pidsLimit,
		},
		PortBindings: nat.PortMap{
			nat.Port(containerPort + "/tcp"): []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d", hostPort)}},
		},
	}

	networkingConfig := &network.NetworkingConfig{}

	resp, err := dc.cli.ContainerCreate(ctx, config, hostConfig, networkingConfig, nil, "")
	if err != nil {
		fmt.Printf("[ERROR] failed to create container: %v\n", err)
		return "", 0, fmt.Errorf("failed to create container: %w", err)
	}

	if err := dc.cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		fmt.Printf("[ERROR] failed to start container: %v\n", err)
		return "", 0, fmt.Errorf("failed to start container: %w", err)
	}

	if err := waitForTTYD(hostPort, 5*time.Minute); err != nil {
		fmt.Printf("[ERROR] ttyd did not become ready: %v\n", err)
		return "", 0, fmt.Errorf("ttyd not ready: %w", err)
	}

	url := fmt.Sprintf("http://localhost:%d/", hostPort)
	fmt.Printf("Lab session started. Open your browser: %s\n", url)

	return resp.ID, hostPort, nil
}

func waitForTTYD(hostPort int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://127.0.0.1:%d/", hostPort)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("timeout waiting for %s", url)
}

// NewDockerClient creates a new DockerClient
func NewDockerClient() (*DockerClient, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &DockerClient{cli: cli}, nil
}

// ListContainers lists running containers and prints their ID and image name
func (dc *DockerClient) ListContainers() error {
	ctx := context.Background()
	containers, err := dc.cli.ContainerList(ctx, types.ContainerListOptions{All: true})
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}
	if len(containers) == 0 {
		fmt.Println("No containers found. Docker is running.")
	}
	for _, container := range containers {
		fmt.Printf("Container ID: %s, Image: %s\n", container.ID[:12], container.Image)
	}
	return nil
}
