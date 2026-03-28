package main

import (
	"devops-lab-backend/internal/api"
	"devops-lab-backend/internal/cloudrun"
	"devops-lab-backend/internal/db"
	"devops-lab-backend/internal/k8s"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
)

func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Log every request to help with debugging on Cloud Run
		log.Printf("[REQ] %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)

		// Set Strict Security Headers
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		w.Header().Set("Content-Security-Policy", "default-src 'self'")

		// Set CORS headers
		allowedOrigin := os.Getenv("ALLOWED_ORIGINS")
		if allowedOrigin == "" {
			allowedOrigin = "*" // Fallback, but should be restricted in Prod
		}
		w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE, PATCH")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Max-Age", "86400")

		// Handle preflight OPTIONS request
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func main() {
	// Load the .env file if it exists
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found or error reading it, relying on environment variables.")
	}

	cloudrunClient, err := cloudrun.NewCloudRunClient()
	if err != nil {
		log.Fatalf("Failed to create Cloud Run client: %v", err)
	}

	firestoreClient, err := db.NewFirestoreClient()
	if err != nil {
		log.Printf("Failed to initialize Firestore Client (dynamic challenges disabled): %v", err)
	}

	k8sClient, err := k8s.NewK8sClient(context.Background())
	if err != nil {
		log.Printf("Failed to initialize K8s Client (K8s challenges disabled): %v", err)
	}

	sessionManager := cloudrun.NewSessionManager(cloudrunClient, k8sClient, 100)

	mux := http.NewServeMux()
	
	// Start background worker for cluster maintenance & cost optimization
	if k8sClient != nil {
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				ctx := context.Background()
				count, _ := k8sClient.CleanupExpiredNamespaces(ctx)
				if count > 0 {
					log.Printf("[MAINTENANCE] Cleaned up %d expired namespaces", count)
				}
				// Warm node strategy: keep cluster for 45min idle to save startup time for next students
				_ = k8sClient.DeleteClusterIfIdle(ctx, 45)
			}
		}()
	}

	// --- Public routes ---
	mux.HandleFunc("/api/challenges", api.GetChallengesHandler(firestoreClient))
	mux.HandleFunc("/api/leaderboard", api.LeaderboardHandler(firestoreClient))
	mux.HandleFunc("/api/register", api.RegisterHandler(firestoreClient))
	mux.HandleFunc("/api/login", api.LoginHandler(firestoreClient))
	mux.HandleFunc("/api/forgot-password", api.ForgotPasswordHandler(firestoreClient))

	// --- Authenticated user routes ---
	mux.HandleFunc("/api/attempts", api.RequireAuth(api.GetAttemptsHandler(firestoreClient)))
	mux.HandleFunc("/api/upload-avatar", api.RequireAuth(api.UploadAvatarHandler(firestoreClient)))
	mux.HandleFunc("/api/send-verification", api.RequireAuth(api.SendVerificationHandler(firestoreClient)))
	mux.HandleFunc("/api/verify-otp", api.RequireAuth(api.VerifyOtpHandler(firestoreClient)))
	mux.HandleFunc("/api/current-session", api.RequireAuth(api.GetCurrentSessionHandler(sessionManager, k8sClient, firestoreClient)))
	mux.HandleFunc("/start-lab", api.RequireAuth(api.StartLabHandler(sessionManager, cloudrunClient, k8sClient, firestoreClient)))
	mux.HandleFunc("/stop-lab", api.RequireAuth(api.StopLabHandler(sessionManager, firestoreClient, k8sClient)))
	mux.HandleFunc("/api/terminal/", api.TerminalProxyHandler(sessionManager, k8sClient))

	// --- Admin-only routes ---
	mux.HandleFunc("/api/users", api.RequireAdmin(api.ListUsersHandler(firestoreClient)))
	mux.HandleFunc("/api/challenges/add", api.RequireAdmin(api.AddChallengeHandler(firestoreClient)))
	mux.HandleFunc("/api/challenges/toggle", api.RequireAdmin(api.ToggleChallengeHandler(firestoreClient)))
	mux.HandleFunc("/api/challenges/delete", api.RequireAdmin(api.DeleteChallengeHandler(firestoreClient)))
	mux.HandleFunc("/api/admins", api.RequireAdmin(api.ListAdminsHandler(firestoreClient)))
	mux.HandleFunc("/api/admins/add", api.RequireAdmin(api.CreateAdminHandler(firestoreClient)))
	mux.HandleFunc("/api/cluster/status", api.RequireAdmin(api.GetClusterStatusHandler(k8sClient)))
	mux.HandleFunc("/api/cluster/status/ws", api.GetClusterStatusWS(k8sClient))
	mux.HandleFunc("/api/cluster/create", api.RequireAdmin(api.CreateClusterHandler(k8sClient)))
	mux.HandleFunc("/api/cluster/delete", api.RequireAdmin(api.DeleteClusterHandler(k8sClient)))
	mux.HandleFunc("/api/sessions", api.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		sessions := sessionManager.ListActiveSessions()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessions)
	}))

	// Fallback/Health check
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "DevOps Lab Backend API Running")
	})

	// Wrap the entire mux with our robust CORS and logging middleware
	finalHandler := CORSMiddleware(mux)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      finalHandler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("Server listening on :%s (Concurrency limit: 100)", port)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
