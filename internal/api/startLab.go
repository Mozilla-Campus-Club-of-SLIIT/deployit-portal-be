package api

import (
	"devops-lab-backend/internal/cloudrun"
	"devops-lab-backend/internal/db"
	"encoding/json"
	"fmt"
	"net/http"
)

type StartLabRequest struct {
	LabType         string `json:"labType"`
	UserID          string `json:"userId"`
	UserEmail       string `json:"userEmail"`
	UserDisplayName string `json:"userDisplayName"`
}

type StartLabResponse struct {
	SessionID string `json:"sessionID"`
	URL       string `json:"url"`
}

func StartLabHandler(sm *cloudrun.SessionManager, crc *cloudrun.CloudRunClient, dbClient *db.FirestoreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req StartLabRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			fmt.Printf("[ERROR] Invalid request: %v\n", err)
			return
		}

		if req.UserID == "" {
			http.Error(w, "Missing userID", http.StatusBadRequest)
			return
		}

		// Safely lookup Firebase
		challenge, err := dbClient.GetChallenge(r.Context(), req.LabType)
		if err != nil {
			http.Error(w, "Unsupported lab type", http.StatusBadRequest)
			fmt.Printf("[ERROR] Challenge Firestore lookup error %s: %v\n", req.LabType, err)
			return
		}

		sessionID := cloudrun.GenerateSessionID()

		// Map database definitions to lab environment configuration
		config := &cloudrun.LabConfig{
			Image:         challenge.Image,
			EnvVars:       challenge.EnvVars,
			ConfigFiles:   challenge.ConfigFiles,
			StartupScript: challenge.StartupScript,
		}

		// Deploy to Cloud Run API with Dynamic Injected Configs
		url, err := crc.CreateLabContainer(sessionID, config)
		if err != nil {
			http.Error(w, "Failed to start lab container on Cloud Run: "+err.Error(), http.StatusInternalServerError)
			fmt.Printf("[ERROR] Failed to start cloud run lab container: %v\n", err)
			return
		}

		session, err := sm.CreateSession(url, sessionID, req.UserID, req.UserEmail, req.UserDisplayName, req.LabType, challenge.Score, challenge.EndScript)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			fmt.Printf("[ERROR] Session creation failed (manager cache): %v\n", err)
			return
		}

		// Cloud Run directly provides the terminal URL via HTTP!
		resp := StartLabResponse{
			SessionID: session.SessionID,
			URL:       url,
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
