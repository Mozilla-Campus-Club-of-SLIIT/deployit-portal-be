package api

import (
	"encoding/json"
	"net/http"
	"devops-lab-backend/internal/docker"
)

type StopLabRequest struct {
	SessionID string `json:"sessionID"`
}

type StopLabResponse struct {
	Status string `json:"status"`
}

func StopLabHandler(sm *docker.SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req StopLabRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}

		if req.SessionID == "" {
			http.Error(w, "Missing sessionID", http.StatusBadRequest)
			return
		}

		_, ok := sm.GetSession(req.SessionID)
		if !ok {
			json.NewEncoder(w).Encode(StopLabResponse{Status: "failure"})
			return
		}

		sm.DeleteSession(req.SessionID)
		json.NewEncoder(w).Encode(StopLabResponse{Status: "success"})
	}
}
