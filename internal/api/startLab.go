package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"devops-lab-backend/internal/docker"
)

type StartLabRequest struct {
	LabType string `json:"labType"`
}

type StartLabResponse struct {
	SessionID string `json:"sessionID"`
	URL       string `json:"url"`
}

func StartLabHandler(sm *docker.SessionManager, dc *docker.DockerClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req StartLabRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			fmt.Printf("[ERROR] Invalid request: %v\n", err)
			return
		}

		// Only "linux" supported for now
		if req.LabType != "linux" {
			http.Error(w, "Unsupported lab type", http.StatusBadRequest)
			fmt.Printf("[ERROR] Unsupported lab type: %s\n", req.LabType)
			return
		}

		port := sm.FindAvailablePort()
		if port == 0 {
			http.Error(w, "No available ports", http.StatusServiceUnavailable)
			fmt.Printf("[ERROR] No available ports\n")
			return
		}

		containerID, assignedPort, err := dc.CreateLabContainer(port)
		if err != nil {
			http.Error(w, "Failed to start lab container", http.StatusInternalServerError)
			fmt.Printf("[ERROR] Failed to start lab container: %v\n", err)
			return
		}

		session, err := sm.CreateSession(containerID, assignedPort)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			fmt.Printf("[ERROR] Session creation failed: %v\n", err)
			return
		}

		resp := StartLabResponse{
			SessionID: session.SessionID,
			URL:       buildBaseURL(r) + "/terminal/" + session.SessionID,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func buildBaseURL(r *http.Request) string {
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		if r.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}

	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return fmt.Sprintf("%s://%s", proto, host)
}
