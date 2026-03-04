package api

import (
	"devops-lab-backend/internal/db"
	"encoding/json"
	"net/http"
)

func GetAttemptsHandler(fc *db.FirestoreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.URL.Query().Get("userId")
		if userID == "" {
			http.Error(w, "Missing userID param", http.StatusBadRequest)
			return
		}

		attempts, err := fc.ListAttempts(r.Context(), userID)
		if err != nil {
			http.Error(w, "Failed to list attempts: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(attempts)
	}
}
