package api

import (
	"devops-lab-backend/internal/db"
	"encoding/json"
	"net/http"
)

func GetAttemptsHandler(fc *db.FirestoreClient) http.HandlerFunc {
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

		attempts, err := fc.ListAttempts(r.Context(), requestedUserID)
		if err != nil {
			http.Error(w, "Failed to list attempts: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(attempts)
	}
}
