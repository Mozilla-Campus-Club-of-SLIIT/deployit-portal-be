package api

import (
	"devops-lab-backend/internal/db"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var (
	leaderboardCache      []LeaderboardEntry
	leaderboardLastUpdate time.Time
	leaderboardMutex      sync.RWMutex
)

type RegisterRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
	University  string `json:"university"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type AuthResponse struct {
	User  *db.User `json:"user"`
	Token string   `json:"token"`
}

type AddAdminRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Password    string `json:"password"`
}

func RegisterHandler(fc *db.FirestoreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req RegisterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		if req.Email == "" || req.Password == "" || req.DisplayName == "" || req.University == "" {
			http.Error(w, "Email, password, display name, and university are required", http.StatusBadRequest)
			return
		}

		// Hash password
		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, "Error hashing password", http.StatusInternalServerError)
			return
		}

		user, err := fc.CreateUser(r.Context(), req.Email, req.DisplayName, string(hash), req.University)
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
			// Explicitly set role for accounts in admins collection
			user.Role = "admin"
		}

		// Verify password
		if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
			http.Error(w, "Invalid email or password", http.StatusUnauthorized)
			return
		}

		fmt.Printf("[DEBUG] Login successful: ID=%s, Email=%s, Role=%s\n", user.ID, user.Email, user.Role)

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

func ListAdminsHandler(fc *db.FirestoreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		admins, err := fc.ListAdmins(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(admins)
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
	University  string `json:"university"`
}

func LeaderboardHandler(fc *db.FirestoreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		leaderboardMutex.RLock()
		if time.Since(leaderboardLastUpdate) < 60*time.Second && leaderboardCache != nil {
			cache := leaderboardCache
			leaderboardMutex.RUnlock()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(cache)
			return
		}
		leaderboardMutex.RUnlock()

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
				University:  u.University,
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

		leaderboardMutex.Lock()
		leaderboardCache = entries
		leaderboardLastUpdate = time.Now()
		leaderboardMutex.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}
}

func CreateAdminHandler(fc *db.FirestoreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req AddAdminRequest
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

		id := strings.Split(req.Email, "@")[0] + "-admin"
		admin := &db.User{
			ID:           id,
			Email:        req.Email,
			DisplayName:  req.DisplayName,
			PasswordHash: string(hash),
			Role:         "admin",
			CreatedAt:    time.Now(),
			Verified:     true,
		}

		_, err = fc.CreateAdminUserExplicitly(r.Context(), admin)
		if err != nil {
			http.Error(w, "Failed to create admin: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"message": "Admin created successfully", "id": id})
	}
}
