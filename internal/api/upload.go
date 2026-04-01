package api

import (
	"context"
	"devops-lab-backend/internal/db"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"cloud.google.com/go/storage"
	firebase "firebase.google.com/go"
	"google.golang.org/api/option"
)

type UploadAvatarResponse struct {
	PhotoURL string `json:"photoUrl"`
}

func initStorageBucket(ctx context.Context) (*storage.BucketHandle, string, error) {
	bucketName := os.Getenv("FIREBASE_STORAGE_BUCKET")
	if bucketName == "" {
		bucketName = "devops-labs-488916.firebasestorage.app"
	}
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		projectID = "devops-labs-488916"
	}

	log.Printf("[upload-avatar] Using bucket=%s, project=%s", bucketName, projectID)

	conf := &firebase.Config{
		ProjectID:     projectID,
		StorageBucket: bucketName,
	}

	privateKey := strings.ReplaceAll(os.Getenv("GCP_SA_PRIVATE_KEY"), "\\n", "\n")
	var app *firebase.App
	var err error

	if privateKey != "" {
		creds := map[string]string{
			"type":                        os.Getenv("GCP_SA_TYPE"),
			"project_id":                  os.Getenv("GCP_SA_PROJECT_ID"),
			"private_key_id":              os.Getenv("GCP_SA_PRIVATE_KEY_ID"),
			"private_key":                 privateKey,
			"client_email":                os.Getenv("GCP_SA_CLIENT_EMAIL"),
			"client_id":                   os.Getenv("GCP_SA_CLIENT_ID"),
			"auth_uri":                    os.Getenv("GCP_SA_AUTH_URI"),
			"token_uri":                   os.Getenv("GCP_SA_TOKEN_URI"),
			"auth_provider_x509_cert_url": os.Getenv("GCP_SA_AUTH_PROVIDER_X509_CERT_URL"),
			"client_x509_cert_url":        os.Getenv("GCP_SA_CLIENT_X509_CERT_URL"),
			"universe_domain":             os.Getenv("GCP_SA_UNIVERSE_DOMAIN"),
		}
		b, _ := json.Marshal(creds)
		log.Printf("[upload-avatar] Initializing Firebase with service account: %s", os.Getenv("GCP_SA_CLIENT_EMAIL"))
		app, err = firebase.NewApp(ctx, conf, option.WithCredentialsJSON(b))
	} else {
		log.Printf("[upload-avatar] No private key found, using default credentials")
		app, err = firebase.NewApp(ctx, conf)
	}
	if err != nil {
		return nil, "", fmt.Errorf("firebase init failed: %w", err)
	}

	storageClient, err := app.Storage(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("storage client failed: %w", err)
	}

	bucket, err := storageClient.DefaultBucket()
	if err != nil {
		return nil, "", fmt.Errorf("bucket access failed: %w", err)
	}

	return bucket, bucketName, nil
}

// UploadAvatarHandler accepts a multipart image upload, stores it in Firebase Storage
// server-side (no CORS issues), updates the Firestore user document with the photoUrl,
// and returns the public download URL.
func UploadAvatarHandler(fc *db.FirestoreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[upload-avatar] Received %s request from %s", r.Method, r.RemoteAddr)

		claims := ClaimsFromContext(r.Context())
		if claims == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if err := r.ParseMultipartForm(10 << 20); err != nil {
			log.Printf("[upload-avatar] ParseMultipartForm error: %v", err)
			http.Error(w, "File too large or invalid form", http.StatusBadRequest)
			return
		}

		userID := r.FormValue("userId")
		if userID == "" {
			userID = claims.UserID
		}
		if claims.Role != "admin" && userID != claims.UserID {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		log.Printf("[upload-avatar] userId=%q", userID)

		file, handler, err := r.FormFile("avatar")
		if err != nil {
			log.Printf("[upload-avatar] FormFile error: %v", err)
			http.Error(w, "No avatar file provided", http.StatusBadRequest)
			return
		}
		defer file.Close()
		log.Printf("[upload-avatar] File received: name=%s size=%d type=%s",
			handler.Filename, handler.Size, handler.Header.Get("Content-Type"))

		// Enforce 1MB limit
		const maxSizeBytes = 1 * 1024 * 1024
		if handler.Size > maxSizeBytes {
			log.Printf("[upload-avatar] File too large: %d bytes", handler.Size)
			http.Error(w, fmt.Sprintf("Profile image must be smaller than 1MB (got %.2fMB)", float64(handler.Size)/1024/1024), http.StatusRequestEntityTooLarge)
			return
		}

		ext := "jpg"
		parts := strings.Split(handler.Filename, ".")
		if len(parts) > 1 {
			ext = strings.ToLower(parts[len(parts)-1])
		}

		ctx := r.Context()
		bucket, bucketName, err := initStorageBucket(ctx)
		if err != nil {
			log.Printf("[upload-avatar] Storage init error: %v", err)
			http.Error(w, fmt.Sprintf("Storage init failed: %v", err), http.StatusInternalServerError)
			return
		}

		objectPath := fmt.Sprintf("profile-images/%s.%s", userID, ext)
		log.Printf("[upload-avatar] Uploading to gs://%s/%s", bucketName, objectPath)

		obj := bucket.Object(objectPath)
		wc := obj.NewWriter(ctx)
		contentType := handler.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "image/jpeg"
		}
		wc.ContentType = contentType
		wc.ACL = []storage.ACLRule{{Entity: storage.AllUsers, Role: storage.RoleReader}}

		written, err := io.Copy(wc, file)
		if err != nil {
			log.Printf("[upload-avatar] io.Copy error: %v", err)
			http.Error(w, fmt.Sprintf("Upload failed: %v", err), http.StatusInternalServerError)
			return
		}
		log.Printf("[upload-avatar] Wrote %d bytes", written)

		if err := wc.Close(); err != nil {
			log.Printf("[upload-avatar] Writer close error: %v", err)
			http.Error(w, fmt.Sprintf("Finalizing upload failed: %v", err), http.StatusInternalServerError)
			return
		}
		log.Printf("[upload-avatar] Upload complete!")

		photoURL := fmt.Sprintf("https://storage.googleapis.com/%s/%s", bucketName, objectPath)
		log.Printf("[upload-avatar] photoURL=%s", photoURL)

		if fc != nil {
			if err := fc.UpdateUserPhotoUrl(ctx, userID, photoURL); err != nil {
				log.Printf("[upload-avatar] Firestore update failed for user %s: %v", userID, err)
			} else {
				log.Printf("[upload-avatar] Firestore photoUrl updated for user %s", userID)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(UploadAvatarResponse{PhotoURL: photoURL})
	}
}
