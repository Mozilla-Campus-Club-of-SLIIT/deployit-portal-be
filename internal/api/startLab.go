package api

import (
	"devops-lab-backend/internal/cloudrun"
	"devops-lab-backend/internal/db"
	"devops-lab-backend/internal/k8s"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
)

type StartLabRequest struct {
	LabType         string `json:"labType"`
	UserID          string `json:"userId"`
	UserEmail       string `json:"userEmail"`
	UserDisplayName string `json:"userDisplayName"`
}

type StartLabResponse struct {
	SessionID   string `json:"sessionID"`
	URL         string `json:"url"`
	TimeLimit   int    `json:"timeLimit"`
	ChallengeID string `json:"challengeID"`
}

func StartLabHandler(sm *cloudrun.SessionManager, crc *cloudrun.CloudRunClient, kc *k8s.K8sClient, dbClient *db.FirestoreClient) http.HandlerFunc {
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

		// Check if user already has an active session
		if session, ok := sm.GetUserSession(req.UserID); ok {
			// Find the challenge they are currently running to get the time limit
			currentChallenge, _ := dbClient.GetChallenge(r.Context(), session.ChallengeID)
			timeLimit := 300
			if currentChallenge != nil {
				timeLimit = currentChallenge.TimeLimit
			}

			resp := StartLabResponse{
				SessionID:   session.SessionID,
				URL:         session.URL,
				TimeLimit:   timeLimit,
				ChallengeID: session.ChallengeID,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		// Lock the user's session creation process to prevent concurrent provisionings (race conditions)
		if err := sm.LockSession(req.UserID); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			fmt.Printf("[WARN] Concurrent lab start attempt blocked for user %s: %v\n", req.UserID, err)
			return
		}
		defer sm.UnlockSession(req.UserID)

		// Safely lookup Firebase
		challenge, err := dbClient.GetChallenge(r.Context(), req.LabType)
		if err != nil {
			http.Error(w, "Unsupported lab type", http.StatusBadRequest)
			fmt.Printf("[ERROR] Challenge Firestore lookup error %s: %v\n", req.LabType, err)
			return
		}

		sessionID := cloudrun.GenerateSessionID()

		var url string
		var namespace string
		var provisionErr error

		if challenge.IsK8s {
			if kc == nil {
				http.Error(w, "Kubernetes orchestration is disabled", http.StatusServiceUnavailable)
				return
			}
			safeUserID := strings.Map(func(r rune) rune {
				if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
					return r
				}
				return '-'
			}, strings.ToLower(req.UserID))
			namespace = fmt.Sprintf("challenge-%s-%s", safeUserID, sessionID)
			_, provisionErr = kc.ProvisionChallenge(r.Context(), &k8s.ChallengeConfig{
				Namespace:     namespace,
				ChallengeID:   req.LabType,
				UserID:        req.UserID,
				ExpiryHours:   1, // Reduce to 1 hour for cost saving
				CPUQuota:      challenge.CPUQuota,
				MemoryQuota:   challenge.MemoryQuota,
				PodQuota:      challenge.PodQuota,
				Image:         challenge.Image,
				StartupScript: challenge.StartupScript,
			})
			// In a real system, the URL would be the web terminal ingress for that namespace
			// Return a proxy URL through our own backend to reach the K8s pod
			url = fmt.Sprintf("%s/api/terminal/%s/", buildBaseURL(r), sessionID)
		} else {
			// Map database definitions to lab environment configuration
			config := &cloudrun.LabConfig{
				Image:         challenge.Image,
				EnvVars:       challenge.EnvVars,
				ConfigFiles:   challenge.ConfigFiles,
				StartupScript: challenge.StartupScript,
			}

			// Deploy to Cloud Run API with Dynamic Injected Configs
			url, provisionErr = crc.CreateLabContainer(sessionID, config)
		}

		if provisionErr != nil {
			if provisionErr.Error() == "cluster is currently being provisioned" {
				w.WriteHeader(http.StatusAccepted)
				w.Write([]byte("Infrastructure is being warmed up"))
				return
			}
			http.Error(w, "Failed to start lab environment: "+provisionErr.Error(), http.StatusInternalServerError)
			fmt.Printf("[ERROR] Failed to start lab environment: %v\n", provisionErr)
			return
		}

		session, err := sm.CreateSession(url, sessionID, req.UserID, req.UserEmail, req.UserDisplayName, req.LabType, challenge.Score, challenge.EndScript)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			fmt.Printf("[ERROR] Session creation failed (manager cache): %v\n", err)
			return
		}
		session.IsK8s = challenge.IsK8s
		session.Namespace = namespace
		// Cloud Run directly provides the terminal URL via HTTP!
		log.Printf("[START] Returning lab URL: %s", url)
		resp := StartLabResponse{
			SessionID:   session.SessionID,
			URL:         url,
			TimeLimit:   challenge.TimeLimit,
			ChallengeID: req.LabType,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func GetCurrentSessionHandler(sm *cloudrun.SessionManager, dbClient *db.FirestoreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.URL.Query().Get("userId")
		if userID == "" {
			http.Error(w, "Missing userId", http.StatusBadRequest)
			return
		}

		if session, ok := sm.GetUserSession(userID); ok {
			currentChallenge, _ := dbClient.GetChallenge(r.Context(), session.ChallengeID)
			timeLimit := 300
			if currentChallenge != nil {
				timeLimit = currentChallenge.TimeLimit
			}

			resp := StartLabResponse{
				SessionID:   session.SessionID,
				URL:         session.URL,
				TimeLimit:   timeLimit,
				ChallengeID: session.ChallengeID,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		// Check if it's still being provisioned
		if sm.IsProvisioning(userID) {
			w.WriteHeader(http.StatusAccepted) // 202 Accepted means we are still working on it
			w.Write([]byte("Lab session is currently being provisioned"))
			return
		}

		w.WriteHeader(http.StatusNotFound)
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
