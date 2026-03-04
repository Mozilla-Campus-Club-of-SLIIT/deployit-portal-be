package main

import (
	"devops-lab-backend/internal/api"
	"devops-lab-backend/internal/cloudrun"
	"devops-lab-backend/internal/db"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/joho/godotenv"
)

func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next(w, r)
	}
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
		log.Printf("Failed to initialize Firestore Client (dynamc challenges disabled): %v", err)
	}

	sessionManager := cloudrun.NewSessionManager(cloudrunClient, 50)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/challenges", corsMiddleware(api.GetChallengesHandler(firestoreClient)))
	mux.HandleFunc("/api/challenges/add", corsMiddleware(api.AddChallengeHandler(firestoreClient)))
	mux.HandleFunc("/api/register", corsMiddleware(api.RegisterHandler(firestoreClient)))
	mux.HandleFunc("/api/login", corsMiddleware(api.LoginHandler(firestoreClient)))
	mux.HandleFunc("/api/users", corsMiddleware(api.ListUsersHandler(firestoreClient)))
	mux.HandleFunc("/start-lab", corsMiddleware(api.StartLabHandler(sessionManager, cloudrunClient, firestoreClient)))
	mux.HandleFunc("/stop-lab", corsMiddleware(api.StopLabHandler(sessionManager, firestoreClient)))
	mux.HandleFunc("/api/attempts", corsMiddleware(api.GetAttemptsHandler(firestoreClient)))
	mux.HandleFunc("/api/upload-avatar", corsMiddleware(api.UploadAvatarHandler(firestoreClient)))
	mux.HandleFunc("/api/sessions", corsMiddleware(func(w http.ResponseWriter, r *http.Request) {
		sessions := sessionManager.ListActiveSessions()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessions)
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		fmt.Fprintln(w, "DevOps Lab Backend API Running")
	})

	handler := corsMiddlewareGlobal(mux)
	log.Println("Server listening on :8080")
	log.Println("CORS enabled for all origins")
	if err := http.ListenAndServe(":8080", handler); err != nil {
		log.Fatal(err)
	}
}

func corsMiddlewareGlobal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
