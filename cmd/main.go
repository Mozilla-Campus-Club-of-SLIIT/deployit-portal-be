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
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const defaultMaxJSONBodyBytes int64 = 1 << 20 // 1MB

func maxJSONBodyBytes() int64 {
	v := strings.TrimSpace(os.Getenv("MAX_JSON_BODY_BYTES"))
	if v == "" {
		return defaultMaxJSONBodyBytes
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		log.Printf("Invalid MAX_JSON_BODY_BYTES=%q, using default %d", v, defaultMaxJSONBodyBytes)
		return defaultMaxJSONBodyBytes
	}
	return n
}

func CORSMiddleware(next http.Handler, allowedOrigins map[string]struct{}) http.Handler {
	maxJSONBytes := maxJSONBodyBytes()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Log every request to help with debugging on Cloud Run
		log.Printf("[REQ] %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)

		// Set Strict Security Headers
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		w.Header().Set("Content-Security-Policy", "default-src 'self'")

		// Set CORS headers from explicit allowlist only.
		origin := r.Header.Get("Origin")
		if origin != "" {
			if _, ok := allowedOrigins[origin]; ok {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			} else if r.Method == http.MethodOptions {
				http.Error(w, "Origin not allowed", http.StatusForbidden)
				return
			}
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE, PATCH")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Max-Age", "86400")

		// Handle preflight OPTIONS request
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
			contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
			if strings.Contains(contentType, "application/json") {
				r.Body = http.MaxBytesReader(w, r.Body, maxJSONBytes)
			}
		}

		next.ServeHTTP(w, r)
	})
}

func main() {
	// Load the .env file if it exists
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found or error reading it, relying on environment variables.")
	}

	allowedOriginsEnv := strings.TrimSpace(os.Getenv("ALLOWED_ORIGINS"))
	if allowedOriginsEnv == "" {
		log.Fatal("ALLOWED_ORIGINS must be set in production (comma-separated list of allowed origins)")
	}
	allowedOrigins := make(map[string]struct{})
	for _, o := range strings.Split(allowedOriginsEnv, ",") {
		origin := strings.TrimSpace(o)
		if origin != "" {
			allowedOrigins[origin] = struct{}{}
		}
	}
	if len(allowedOrigins) == 0 {
		log.Fatal("ALLOWED_ORIGINS is set but empty after parsing")
	}
	if err := api.ValidateJWTSecret(); err != nil {
		log.Fatal(err)
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

	sessionManager := cloudrun.NewSessionManager(cloudrunClient, k8sClient, 220)
	rateLimiter := api.NewRateLimiter()
	startedAt := time.Now().UTC()

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
	mux.HandleFunc("/api/register", rateLimiter.LimitByIP(api.RegisterHandler(firestoreClient), "register", 6, time.Minute))
	mux.HandleFunc("/api/login", rateLimiter.LimitByIP(api.LoginHandler(firestoreClient), "login", 12, time.Minute))
	mux.HandleFunc("/api/forgot-password", rateLimiter.LimitByIP(api.ForgotPasswordHandler(firestoreClient), "forgot-password", 6, time.Minute))

	// --- Authenticated user routes ---
	mux.HandleFunc("/api/attempts", api.RequireAuth(api.GetAttemptsHandler(firestoreClient)))
	mux.HandleFunc("/api/upload-avatar", api.RequireAuth(api.UploadAvatarHandler(firestoreClient)))
	mux.HandleFunc("/api/send-verification", api.RequireAuth(rateLimiter.LimitByIP(api.SendVerificationHandler(firestoreClient), "send-verification", 6, time.Minute)))
	mux.HandleFunc("/api/verify-otp", api.RequireAuth(rateLimiter.LimitByIP(api.VerifyOtpHandler(firestoreClient), "verify-otp", 12, time.Minute)))
	mux.HandleFunc("/api/current-session", api.RequireAuth(api.GetCurrentSessionHandler(sessionManager, k8sClient, firestoreClient)))
	mux.HandleFunc("/start-lab", api.RequireAuth(api.StartLabHandler(sessionManager, cloudrunClient, k8sClient, firestoreClient)))
	mux.HandleFunc("/stop-lab", api.RequireAuth(api.StopLabHandler(sessionManager, firestoreClient, k8sClient)))
	mux.HandleFunc("/api/terminal/", api.RequireAuth(api.TerminalProxyHandler(sessionManager, k8sClient, allowedOrigins)))

	// --- Admin-only routes ---
	mux.HandleFunc("/api/users", api.RequireAdmin(api.ListUsersHandler(firestoreClient)))
	mux.HandleFunc("/api/challenges/add", api.RequireAdmin(api.AddChallengeHandler(firestoreClient)))
	mux.HandleFunc("/api/challenges/toggle", api.RequireAdmin(api.ToggleChallengeHandler(firestoreClient)))
	mux.HandleFunc("/api/challenges/delete", api.RequireAdmin(api.DeleteChallengeHandler(firestoreClient)))
	mux.HandleFunc("/api/admins", api.RequireAdmin(api.ListAdminsHandler(firestoreClient)))
	mux.HandleFunc("/api/admins/add", api.RequireAdmin(api.CreateAdminHandler(firestoreClient)))
	mux.HandleFunc("/api/cluster/status", api.RequireAdmin(api.GetClusterStatusHandler(k8sClient)))
	mux.HandleFunc("/api/cluster/status/ws", api.RequireAdmin(api.GetClusterStatusWS(k8sClient, allowedOrigins)))
	mux.HandleFunc("/api/cluster/create", api.RequireAdmin(api.CreateClusterHandler(k8sClient)))
	mux.HandleFunc("/api/cluster/delete", api.RequireAdmin(api.DeleteClusterHandler(k8sClient)))
	mux.HandleFunc("/api/sessions", api.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		sessions := sessionManager.ListActiveSessions()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessions)
	}))

	// --- Health endpoints ---
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":    "ok",
			"service":   "deployit-portal-be",
			"startedAt": startedAt.Format(time.RFC3339),
		})
	})

	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		components := map[string]string{
			"cloudrun": "ready",
			"firestore": "ready",
			"k8s":      "ready",
		}

		ready := true
		if cloudrunClient == nil {
			ready = false
			components["cloudrun"] = "not_ready"
		}
		if firestoreClient == nil {
			ready = false
			components["firestore"] = "not_ready"
		}
		if k8sClient == nil {
			components["k8s"] = "degraded"
		}

		status := "ready"
		httpStatus := http.StatusOK
		if !ready {
			status = "not_ready"
			httpStatus = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(httpStatus)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":     status,
			"service":    "deployit-portal-be",
			"components": components,
		})
	})

	// Fallback/Health check
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "DevOps Lab Backend API Running")
	})

	// Wrap the entire mux with our robust CORS and logging middleware
	finalHandler := CORSMiddleware(mux, allowedOrigins)

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

	log.Printf("Server listening on :%s (Concurrency limit: 220)", port)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
