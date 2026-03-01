package api

import (
	"encoding/json"
	"net/http"
	"devops-lab-backend/internal/cloudrun"
)

type SubmitChallengeRequest struct {
	SessionID string `json:"sessionID"`
	Answer    string `json:"answer"`
}

type SubmitChallengeResponse struct {
	Correct bool `json:"correct"`
}

func SubmitChallengeHandler(sm *cloudrun.SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req SubmitChallengeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}

		session, ok := sm.GetSession(req.SessionID)
		if !ok {
			json.NewEncoder(w).Encode(SubmitChallengeResponse{Correct: false})
			return
		}

		correct := (req.Answer == session.Flag)
		
		// Stop the container dynamically, as requested
		sm.DeleteSession(req.SessionID)
		
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SubmitChallengeResponse{Correct: correct})
	}
}
