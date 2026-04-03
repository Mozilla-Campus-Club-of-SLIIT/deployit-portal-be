package db

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	firebase "firebase.google.com/go"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Challenge struct {
	ID            string            `firestore:"id" json:"id"`
	Title         string            `firestore:"title" json:"title"`
	Description   string            `firestore:"description" json:"description"`
	Difficulty    string            `firestore:"difficulty" json:"difficulty"`
	Score         int               `firestore:"score" json:"score"`         // Points awarded for passing
	TimeLimit     int               `firestore:"timeLimit" json:"timeLimit"` // Lab timer in seconds
	Image         string            `firestore:"image" json:"image"`
	Tags          []string          `firestore:"tags" json:"tags"`
	Locked        bool              `firestore:"locked" json:"locked"`
	EnvVars       map[string]string `firestore:"envVars" json:"envVars"`
	ConfigFiles   map[string]string `firestore:"configFiles" json:"configFiles"`
	StartupScript string            `firestore:"startupScript" json:"startupScript"`
	EndScript     string            `firestore:"endScript" json:"endScript"`
	Gen2          bool              `firestore:"gen2" json:"gen2"`
	IsK8s         bool              `firestore:"isK8s" json:"isK8s"`
	CPUQuota      string            `firestore:"cpuQuota" json:"cpuQuota"`       // e.g. "1"
	MemoryQuota   string            `firestore:"memoryQuota" json:"memoryQuota"` // e.g. "1Gi"
	PodQuota      string            `firestore:"podQuota" json:"podQuota"`       // e.g. "10"
}

type ChallengeAttempt struct {
	ID              string    `firestore:"id" json:"id"`
	UserID          string    `firestore:"userId" json:"userId"`
	UserEmail       string    `firestore:"userEmail" json:"userEmail"`
	UserDisplayName string    `firestore:"userDisplayName" json:"userDisplayName"`
	ChallengeID     string    `firestore:"challengeId" json:"challengeId"`
	Result          string    `firestore:"result" json:"result"` // SUCCESS/FAILURE
	ScoreEarned     int       `firestore:"scoreEarned" json:"scoreEarned"`
	Output          string    `firestore:"output" json:"output"`
	Timestamp       time.Time `firestore:"timestamp" json:"timestamp"`
}

type User struct {
	ID           string    `firestore:"id" json:"id"`
	Email        string    `firestore:"email" json:"email"`
	DisplayName  string    `firestore:"displayName" json:"displayName"`
	PasswordHash string    `firestore:"passwordHash" json:"-"`
	TotalScore   int       `firestore:"totalScore" json:"totalScore"`
	CreatedAt    time.Time `firestore:"createdAt" json:"createdAt"`
	Role         string    `firestore:"role" json:"role"`
	PhotoUrl     string    `firestore:"photoUrl" json:"photoUrl"`
	OTPCode      string    `firestore:"otpCode" json:"-"`
	OTPExpiry    time.Time `firestore:"otpExpiry" json:"-"`
	Verified     bool      `firestore:"verified" json:"verified"`
	University   string    `firestore:"university" json:"university"`
}

type FirestoreClient struct {
	client *firestore.Client
}

func NewFirestoreClient() (*FirestoreClient, error) {
	ctx := context.Background()
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		// Defaulting or using local emulation will be required if GCP auth not set
		projectID = "my-project-id"
	}
	conf := &firebase.Config{ProjectID: projectID}

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
		app, err = firebase.NewApp(ctx, conf, option.WithCredentialsJSON(b))
	} else {
		app, err = firebase.NewApp(ctx, conf)
	}

	if err != nil {
		return nil, fmt.Errorf("error initializing firebase app: %v", err)
	}

	client, err := app.Firestore(ctx)
	if err != nil {
		return nil, fmt.Errorf("error initializing firestore: %v", err)
	}

	return &FirestoreClient{client: client}, nil
}

// GetChallenge retrieves a challenge by ID
func (fc *FirestoreClient) GetChallenge(ctx context.Context, id string) (*Challenge, error) {
	doc, err := fc.client.Collection("challenges").Doc(id).Get(ctx)
	if err != nil {
		return nil, err
	}
	var challenge Challenge
	if err := doc.DataTo(&challenge); err != nil {
		return nil, err
	}
	if challenge.ID == "" {
		challenge.ID = doc.Ref.ID
	}
	// Default image fallback just in case
	if challenge.Image == "" {
		challenge.Image = "tsl0922/ttyd:latest"
	}
	if challenge.IsK8s {
		if challenge.CPUQuota == "" {
			challenge.CPUQuota = "200m"
		}
		if challenge.MemoryQuota == "" {
			challenge.MemoryQuota = "256Mi"
		}
		if challenge.PodQuota == "" {
			challenge.PodQuota = "5"
		}
	}
	return &challenge, nil
}

// ListChallenges retrieves all challenges
func (fc *FirestoreClient) ListChallenges(ctx context.Context) ([]Challenge, error) {
	docs, err := fc.client.Collection("challenges").Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	var challenges []Challenge
	for _, doc := range docs {
		var challenge Challenge
		if err := doc.DataTo(&challenge); err != nil {
			continue
		}
		if challenge.ID == "" {
			challenge.ID = doc.Ref.ID
		}
		if challenge.Image == "" {
			challenge.Image = "tsl0922/ttyd:latest"
		}
		challenges = append(challenges, challenge)
	}
	return challenges, nil
}

// AddChallenge inserts or updates a challenge in Firestore
func (fc *FirestoreClient) AddChallenge(ctx context.Context, challenge *Challenge) error {
	if challenge.ID == "" {
		return fmt.Errorf("challenge ID is required")
	}

	// Create or overwrite the specific challenge document
	_, err := fc.client.Collection("challenges").Doc(challenge.ID).Set(ctx, challenge)
	return err
}

// ToggleChallengeLocked flips the `locked` field on a challenge document.
// Returns the new locked state (true = disabled, false = enabled).
func (fc *FirestoreClient) ToggleChallengeLocked(ctx context.Context, id string) (bool, error) {
	ref := fc.client.Collection("challenges").Doc(id)

	// Read current state
	doc, err := ref.Get(ctx)
	if err != nil {
		return false, fmt.Errorf("challenge not found: %w", err)
	}

	var challenge Challenge
	if err := doc.DataTo(&challenge); err != nil {
		return false, err
	}

	newLocked := !challenge.Locked

	_, err = ref.Update(ctx, []firestore.Update{
		{Path: "locked", Value: newLocked},
	})
	return newLocked, err
}

// DeleteChallenge drops a new challenge from the DB
func (fc *FirestoreClient) DeleteChallenge(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("challenge ID is required")
	}

	_, err := fc.client.Collection("challenges").Doc(id).Delete(ctx)
	return err
}

// CreateUser saves a new user to Firestore
func (fc *FirestoreClient) CreateUser(ctx context.Context, email string, displayName string, passwordHash string, university string) (*User, error) {
	// Check if user already exists
	existing, _ := fc.GetUserByEmail(ctx, email)
	if existing != nil {
		return nil, fmt.Errorf("user with this email already exists")
	}

	id := strings.Split(email, "@")[0] + "-" + strings.ToLower(strings.ReplaceAll(displayName, " ", "-")) // Basic ID generation
	user := &User{
		ID:           id,
		Email:        email,
		DisplayName:  displayName,
		PasswordHash: passwordHash,
		University:   university,
		CreatedAt:    time.Now(),
		Role:         "user",
		Verified:     false,
	}

	_, err := fc.client.Collection("users").Doc(user.ID).Set(ctx, user)
	if err != nil {
		return nil, err
	}
	return user, nil
}

// UpdateUserPhotoUrl sets the photoUrl field on an existing user document
func (fc *FirestoreClient) UpdateUserPhotoUrl(ctx context.Context, userID string, photoUrl string) error {
	_, err := fc.client.Collection("users").Doc(userID).Update(ctx, []firestore.Update{
		{Path: "photoUrl", Value: photoUrl},
	})
	return err
}

func (fc *FirestoreClient) CreateAdminUserExplicitly(ctx context.Context, user *User) (*User, error) {
	_, err := fc.client.Collection("admins").Doc(user.ID).Set(ctx, user)
	if err != nil {
		return nil, err
	}
	return user, nil
}

// GetUserByEmail finds a user by their email address
func (fc *FirestoreClient) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	docs, err := fc.client.Collection("users").Where("email", "==", email).Limit(1).Documents(ctx).GetAll()
	if err != nil || len(docs) == 0 {
		return nil, fmt.Errorf("user not found")
	}
	var u User
	if err := docs[0].DataTo(&u); err != nil {
		return nil, err
	}
	return &u, nil
}

// GetAdminByEmail finds an admin by their email address in the admins collection
func (fc *FirestoreClient) GetAdminByEmail(ctx context.Context, email string) (*User, error) {
	docs, err := fc.client.Collection("admins").Where("email", "==", email).Limit(1).Documents(ctx).GetAll()
	if err != nil || len(docs) == 0 {
		return nil, fmt.Errorf("admin not found")
	}
	var u User
	if err := docs[0].DataTo(&u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (fc *FirestoreClient) DeleteUserByEmail(ctx context.Context, email string) error {
	docs, err := fc.client.Collection("users").Where("email", "==", email).Documents(ctx).GetAll()
	if err != nil {
		return err
	}
	for _, doc := range docs {
		_, err := doc.Ref.Delete(ctx)
		if err != nil {
			return err
		}
	}
	return nil
}

// GetUserByID finds a user by their ID
func (fc *FirestoreClient) GetUserByID(ctx context.Context, id string) (*User, error) {
	doc, err := fc.client.Collection("users").Doc(id).Get(ctx)
	if err != nil {
		return nil, err
	}
	var u User
	if err := doc.DataTo(&u); err != nil {
		return nil, err
	}
	return &u, nil
}

// IncrementUserScore increases a user's total score in Firestore
func (fc *FirestoreClient) IncrementUserScore(ctx context.Context, userID string, score int) error {
	if score <= 0 {
		return nil
	}
	_, err := fc.client.Collection("users").Doc(userID).Update(ctx, []firestore.Update{
		{
			Path:  "totalScore",
			Value: firestore.Increment(score),
		},
	})
	return err
}

// AddChallenge puts a new challenge into the DB
func (fc *FirestoreClient) SaveAttempt(ctx context.Context, attempt *ChallengeAttempt) error {
	if attempt.UserID == "" {
		return fmt.Errorf("userId is required")
	}
	attempt.Timestamp = time.Now()
	// Let Firestore generate a random ID
	ref := fc.client.Collection("attempts").NewDoc()
	attempt.ID = ref.ID
	_, err := ref.Set(ctx, attempt)
	return err
}

// SaveAttemptAndApplyScore saves an attempt and updates totalScore atomically.
// Points are awarded only when the first attempt for a challenge is SUCCESS.
func (fc *FirestoreClient) SaveAttemptAndApplyScore(ctx context.Context, attempt *ChallengeAttempt, challengeScore int) (int, error) {
	if attempt == nil {
		return 0, fmt.Errorf("attempt is required")
	}
	if attempt.UserID == "" {
		return 0, fmt.Errorf("userId is required")
	}
	if attempt.ChallengeID == "" {
		return 0, fmt.Errorf("challengeId is required")
	}
	if challengeScore < 0 {
		challengeScore = 0
	}

	now := time.Now()
	attemptRef := fc.client.Collection("attempts").NewDoc()
	normalizedUserID := strings.ToLower(strings.TrimSpace(attempt.UserID))
	normalizedChallengeID := strings.ToLower(strings.TrimSpace(attempt.ChallengeID))
	trackerID := url.QueryEscape(normalizedUserID) + "__" + url.QueryEscape(normalizedChallengeID)
	trackerRef := fc.client.Collection("userChallengeAttempts").Doc(trackerID)
	userRef := fc.client.Collection("users").Doc(attempt.UserID)

	awardedScore := 0
	err := fc.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		attemptCount := 0

		trackerSnap, err := tx.Get(trackerRef)
		if err != nil {
			if status.Code(err) != codes.NotFound {
				return err
			}

			// Backfill-safe fallback: if the tracker doc doesn't exist yet,
			// inspect existing attempts so historical data still enforces the rule.
			docs, qErr := tx.Documents(fc.client.Collection("attempts").
				Where("userId", "==", attempt.UserID).
				Where("challengeId", "==", attempt.ChallengeID).
				Limit(1)).GetAll()
			if qErr != nil {
				return qErr
			}
			if len(docs) > 0 {
				attemptCount = 1
			}
		} else {
			var tracker struct {
				AttemptCount int `firestore:"attemptCount"`
			}
			if decodeErr := trackerSnap.DataTo(&tracker); decodeErr != nil {
				return decodeErr
			}
			if tracker.AttemptCount > 0 {
				attemptCount = tracker.AttemptCount
			}
		}

		if attempt.Result == "SUCCESS" && attemptCount == 0 {
			awardedScore = challengeScore
		}

		attempt.ID = attemptRef.ID
		attempt.Timestamp = now
		attempt.ScoreEarned = awardedScore

		if err := tx.Set(attemptRef, attempt); err != nil {
			return err
		}

		if awardedScore > 0 {
			if err := tx.Update(userRef, []firestore.Update{{
				Path:  "totalScore",
				Value: firestore.Increment(awardedScore),
			}}); err != nil {
				return err
			}
		}

		if err := tx.Set(trackerRef, map[string]interface{}{
			"userId":      attempt.UserID,
			"challengeId": attempt.ChallengeID,
			"normalizedUserId": normalizedUserID,
			"normalizedChallengeId": normalizedChallengeID,
			"attemptCount": attemptCount + 1,
			"updatedAt":   now,
		}, firestore.MergeAll); err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return 0, err
	}

	return awardedScore, nil
}

// ListAttempts retrieves all attempts for a specific user
func (fc *FirestoreClient) ListAttempts(ctx context.Context, userID string) ([]ChallengeAttempt, error) {
	log.Printf("[DATABASE] Querying attempts for userId: %s", userID)

	// Temporarily removed OrderBy to resolve potential index requirement issues
	docs, err := fc.client.Collection("attempts").
		Where("userId", "==", userID).
		Documents(ctx).GetAll()

	if err != nil {
		log.Printf("[ERROR] Firestore ListAttempts failed: %v", err)
		return nil, err
	}

	var attempts []ChallengeAttempt
	for _, doc := range docs {
		var attempt ChallengeAttempt
		if err := doc.DataTo(&attempt); err != nil {
			log.Printf("[WARNING] data mapping failed for attempt %s: %v", doc.Ref.ID, err)
			continue
		}
		attempts = append(attempts, attempt)
	}

	// Manual sort by timestamp descending since we removed OrderBy from query
	sort.Slice(attempts, func(i, j int) bool {
		return attempts[i].Timestamp.After(attempts[j].Timestamp)
	})

	log.Printf("[DATABASE] Found %d attempts for user %s", len(attempts), userID)
	return attempts, nil
}

// HasSuccessfulAttemptedChallenge checks whether a user has already passed a specific challenge.
// Score should only be awarded on the first SUCCESS for that challenge.
func (fc *FirestoreClient) HasSuccessfulAttemptedChallenge(ctx context.Context, userID, challengeID string) (bool, error) {
	docs, err := fc.client.Collection("attempts").
		Where("userId", "==", userID).
		Where("challengeId", "==", challengeID).
		Where("result", "==", "SUCCESS").
		Limit(1).
		Documents(ctx).GetAll()

	if err != nil {
		return false, err
	}
	return len(docs) > 0, nil
}

// HasAttemptedChallenge checks whether a user has any prior attempt for a specific challenge.
// Used for strict first-attempt-only scoring.
func (fc *FirestoreClient) HasAttemptedChallenge(ctx context.Context, userID, challengeID string) (bool, error) {
	docs, err := fc.client.Collection("attempts").
		Where("userId", "==", userID).
		Where("challengeId", "==", challengeID).
		Limit(1).
		Documents(ctx).GetAll()

	if err != nil {
		return false, err
	}
	return len(docs) > 0, nil
}

// ListUsers retrieves all users from Firestore
func (fc *FirestoreClient) ListUsers(ctx context.Context) ([]User, error) {
	docs, err := fc.client.Collection("users").Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}

	var users []User
	for _, doc := range docs {
		var u User
		if err := doc.DataTo(&u); err != nil {
			continue
		}
		users = append(users, u)
	}

	// Sort by creation time descending
	sort.Slice(users, func(i, j int) bool {
		return users[i].CreatedAt.After(users[j].CreatedAt)
	})

	return users, nil
}

// ListAdmins retrieves all admin accounts from the dedicated admins collection
func (fc *FirestoreClient) ListAdmins(ctx context.Context) ([]User, error) {
	docs, err := fc.client.Collection("admins").Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}

	var admins []User
	for _, doc := range docs {
		var u User
		if err := doc.DataTo(&u); err != nil {
			continue
		}
		u.Role = "admin" // Ensure role is set
		admins = append(admins, u)
	}

	// Sort by creation time descending
	sort.Slice(admins, func(i, j int) bool {
		return admins[i].CreatedAt.After(admins[j].CreatedAt)
	})

	return admins, nil
}

// SetOTP saves an OTP code for a user inside a given collection
func (fc *FirestoreClient) SetOTP(ctx context.Context, collection, userID, otp string, expiry time.Time) error {
	_, err := fc.client.Collection(collection).Doc(userID).Update(ctx, []firestore.Update{
		{Path: "otpCode", Value: otp},
		{Path: "otpExpiry", Value: expiry},
	})
	return err
}

// VerifyOTP validates the OTP code for a user in a given collection
func (fc *FirestoreClient) VerifyOTP(ctx context.Context, collection, userID, otp string) error {
	doc, err := fc.client.Collection(collection).Doc(userID).Get(ctx)
	if err != nil {
		return err
	}
	var u User
	if err := doc.DataTo(&u); err != nil {
		return err
	}
	if u.OTPCode == "" || u.OTPCode != otp {
		return fmt.Errorf("invalid verification code")
	}
	if time.Now().After(u.OTPExpiry) {
		return fmt.Errorf("verification code expired")
	}
	
	// Clear the OTP on successful verification and set Verified to true
	_, err = fc.client.Collection(collection).Doc(userID).Update(ctx, []firestore.Update{
		{Path: "otpCode", Value: ""},
		{Path: "verified", Value: true},
	})
	return err
}
