package api

import (
	"devops-lab-backend/internal/db"
	"encoding/json"
	"net/http"

	"golang.org/x/crypto/bcrypt"
)

type RegisterRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type AuthResponse struct {
	User  *db.User `json:"user"`
	Token string   `json:"token"`
}

func RegisterHandler(fc *db.FirestoreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req RegisterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		if req.Email == "" || req.Password == "" || req.DisplayName == "" {
			http.Error(w, "Email, password, and display name are required", http.StatusBadRequest)
			return
		}

		// Hash password
		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, "Error hashing password", http.StatusInternalServerError)
			return
		}

		user, err := fc.CreateUser(r.Context(), req.Email, req.DisplayName, string(hash))
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}

		// Issue JWT for the new user (role = "user")
		token, err := GenerateToken(user.ID, user.Email, user.Role)
		if err != nil {
			http.Error(w, "Failed to generate token", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(AuthResponse{User: user, Token: token})
	}
}

func LoginHandler(fc *db.FirestoreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req LoginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		user, err := fc.GetUserByEmail(r.Context(), req.Email)
		if err != nil {
			// Try admins collection
			user, err = fc.GetAdminByEmail(r.Context(), req.Email)
			if err != nil {
				http.Error(w, "Invalid email or password", http.StatusUnauthorized)
				return
			}
		}

		// Verify password
		if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
			http.Error(w, "Invalid email or password", http.StatusUnauthorized)
			return
		}

		// Issue JWT
		token, err := GenerateToken(user.ID, user.Email, user.Role)
		if err != nil {
			http.Error(w, "Failed to generate token", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(AuthResponse{User: user, Token: token})
	}
}

func ListUsersHandler(fc *db.FirestoreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		users, err := fc.ListUsers(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(users)
	}
}

// LeaderboardHandler is a PUBLIC endpoint that returns users sorted by totalScore.
// Only exposes non-sensitive fields: id, displayName, totalScore, photoUrl, role.
type LeaderboardEntry struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	TotalScore  int    `json:"totalScore"`
	PhotoUrl    string `json:"photoUrl"`
	Role        string `json:"role"`
}

func LeaderboardHandler(fc *db.FirestoreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		users, err := fc.ListUsers(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		entries := make([]LeaderboardEntry, 0, len(users))
		for _, u := range users {
			entries = append(entries, LeaderboardEntry{
				ID:          u.ID,
				DisplayName: u.DisplayName,
				TotalScore:  u.TotalScore,
				PhotoUrl:    u.PhotoUrl,
				Role:        u.Role,
			})
		}

		// Sort by totalScore descending
		for i := 0; i < len(entries); i++ {
			for j := i + 1; j < len(entries); j++ {
				if entries[j].TotalScore > entries[i].TotalScore {
					entries[i], entries[j] = entries[j], entries[i]
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}
}
