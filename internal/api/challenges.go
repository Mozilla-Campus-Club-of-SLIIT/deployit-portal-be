package api

import (
	"encoding/json"
	"net/http"

	"devops-lab-backend/internal/db"
)

// GetChallengesHandler lists all available challenges from Firestore
func GetChallengesHandler(dbClient *db.FirestoreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		challenges, err := dbClient.ListChallenges(r.Context())
		if err != nil {
			http.Error(w, "Failed to load challenges database", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(challenges)
	}
}

// AddChallengeHandler saves a new challenge to Firestore
func AddChallengeHandler(dbClient *db.FirestoreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var challenge db.Challenge
		if err := json.NewDecoder(r.Body).Decode(&challenge); err != nil {
			http.Error(w, "Invalid request payload", http.StatusBadRequest)
			return
		}

		if err := dbClient.AddChallenge(r.Context(), &challenge); err != nil {
			http.Error(w, "Failed to save challenge: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "success",
			"message": "Challenge added successfully",
		})
	}
}

// ToggleChallengeHandler flips the locked/enabled state of a challenge.
// PATCH /api/challenges/toggle?id=<challengeId>   (admin-only)
func ToggleChallengeHandler(dbClient *db.FirestoreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id query parameter is required", http.StatusBadRequest)
			return
		}

		newLocked, err := dbClient.ToggleChallengeLocked(r.Context(), id)
		if err != nil {
			http.Error(w, "Failed to toggle challenge: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":      id,
			"locked":  newLocked,
			"enabled": !newLocked,
		})
	}
}

// DeleteChallengeHandler deletes a challenge from Firestore
// DELETE /api/challenges/delete?id=<challengeId>   (admin-only)
func DeleteChallengeHandler(dbClient *db.FirestoreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id query parameter is required", http.StatusBadRequest)
			return
		}

		if err := dbClient.DeleteChallenge(r.Context(), id); err != nil {
			http.Error(w, "Failed to delete challenge: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "success",
			"message": "Challenge deleted successfully",
		})
	}
}
