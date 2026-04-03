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
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		claims := ClaimsFromContext(r.Context())
		if claims == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		var req StartLabRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			fmt.Printf("[ERROR] Invalid request: %v\n", err)
			return
		}

		if claims.Role == "admin" {
			if req.UserID == "" {
				http.Error(w, "Missing userID", http.StatusBadRequest)
				return
			}
		} else {
			// Always bind non-admin requests to token identity.
			req.UserID = claims.UserID
			req.UserEmail = claims.Email
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
				TimeLimit:   min(timeLimit, 600),
				ChallengeID: session.ChallengeID,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		// 1. Sanitize the user-controlled data
		cleanUserID := strings.ReplaceAll(strings.ReplaceAll(req.UserID, "\n", ""), "\r", "")

		// Lock the user's session creation process to prevent concurrent provisionings (race conditions)
		if err := sm.LockSession(req.UserID); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			// 2. Use the cleaned variable in the log
			fmt.Printf("[WARN] Concurrent lab start attempt blocked for user %s: %v\n", cleanUserID, err)
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
		challengeID := challenge.ID
		if challengeID == "" {
			challengeID = req.LabType
		}

		sessionID := cloudrun.GenerateSessionID()
		

		if kc == nil {
			http.Error(w, "Kubernetes orchestration is disabled", http.StatusServiceUnavailable)
			return
		}

		var url string
		var namespace string
		var provisionErr error

		cpuQuota := challenge.CPUQuota
		if cpuQuota == "" {
			cpuQuota = "200m"
		}
		memoryQuota := challenge.MemoryQuota
		if memoryQuota == "" {
			memoryQuota = "256Mi"
		}
		podQuota := challenge.PodQuota
		if podQuota == "" {
			podQuota = "5"
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
			ChallengeID:   challengeID,
			UserID:        req.UserID,
			ExpiryHours:   0.17, // Cap at 10 minutes (1/6 hours)
			CPUQuota:      cpuQuota,
			MemoryQuota:   memoryQuota,
			PodQuota:      podQuota,
			Image:         challenge.Image,
			EnvVars:       challenge.EnvVars,
			StartupScript: challenge.StartupScript,
			ConfigFiles:   challenge.ConfigFiles,
		})
		// Return backend terminal proxy URL for K8s-based labs.
		url = fmt.Sprintf("%s/api/terminal/%s/", buildBaseURL(r), sessionID)

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

		session, err := sm.CreateSession(url, sessionID, req.UserID, req.UserEmail, req.UserDisplayName, challengeID, challenge.Score, challenge.EndScript)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			fmt.Printf("[ERROR] Session creation failed (manager cache): %v\n", err)
			return
		}
		session.IsK8s = true
		session.Namespace = namespace
		// Cloud Run directly provides the terminal URL via HTTP!
		log.Printf("[START] Returning lab URL: %s", url)
		resp := StartLabResponse{
			SessionID:   session.SessionID,
			URL:         url,
			TimeLimit:   min(challenge.TimeLimit, 600),
			ChallengeID: challengeID,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func GetCurrentSessionHandler(sm *cloudrun.SessionManager, kc *k8s.K8sClient, dbClient *db.FirestoreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		claims := ClaimsFromContext(r.Context())
		if claims == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		requestedUserID := claims.UserID
		if claims.Role == "admin" {
			if q := r.URL.Query().Get("userId"); q != "" {
				requestedUserID = q
			}
		}

		session, ok := sm.GetUserSession(requestedUserID)
		if !ok {
			// Check if it's still being provisioned
			if sm.IsProvisioning(requestedUserID) {
				w.WriteHeader(http.StatusAccepted)
				w.Write([]byte("Lab session is currently being provisioned"))
				return
			}

			w.WriteHeader(http.StatusNotFound)
			return
		}

		// For K8s sessions, ensure terminal sidecar is actually ready before returning session as active.
		if session.IsK8s && kc != nil {
			ready, err := kc.CheckPodStatus(r.Context(), session.Namespace, "app=challenge-app")
			if err != nil {
				log.Printf("[CURRENT-SESSION] Pod readiness check failed for %s: %v", session.Namespace, err)
				w.WriteHeader(http.StatusAccepted)
				w.Write([]byte("Lab session is warming up"))
				return
			}
			if !ready {
				w.WriteHeader(http.StatusAccepted)
				w.Write([]byte("Lab session is warming up"))
				return
			}
		}

		currentChallenge, _ := dbClient.GetChallenge(r.Context(), session.ChallengeID)
		timeLimit := 300
		if currentChallenge != nil {
			timeLimit = currentChallenge.TimeLimit
		}

		resp := StartLabResponse{
			SessionID:   session.SessionID,
			URL:         session.URL,
			TimeLimit:   min(timeLimit, 600),
			ChallengeID: session.ChallengeID,
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
