package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// contextKey is a private type for context keys to avoid collisions.
type contextKey string

const claimsKey contextKey = "claims"

// AdminClaims holds the JWT payload for authenticated admin users.
type AdminClaims struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

// jwtSecret returns the signing secret from the environment, with a fallback.
func jwtSecret() []byte {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		// Fallback for dev — in production JWT_SECRET must be set
		return []byte("devops-lab-dev-secret-change-in-prod")
	}
	return []byte(secret)
}

// GenerateToken creates a signed JWT for the given user. Expires in 12 hours.
func GenerateToken(userID, email, role string) (string, error) {
	claims := AdminClaims{
		UserID: userID,
		Email:  email,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(12 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "devops-lab-backend",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret())
}

// parseToken validates a JWT string and returns the claims.
func parseToken(tokenStr string) (*AdminClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &AdminClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return jwtSecret(), nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*AdminClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}

// extractToken gets the Bearer token from the Authorization header.
func extractToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	parts := strings.SplitN(header, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
		return parts[1]
	}
	return ""
}

// RequireAuth middleware validates the JWT and injects claims into the request context.
// Returns 401 if the token is missing or invalid.
func RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenStr := extractToken(r)
		if tokenStr == "" {
			http.Error(w, "Authorization token required", http.StatusUnauthorized)
			return
		}
		claims, err := parseToken(tokenStr)
		if err != nil {
			http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
			return
		}
		// Inject claims into context for downstream handlers
		ctx := context.WithValue(r.Context(), claimsKey, claims)
		next(w, r.WithContext(ctx))
	}
}

// RequireAdmin middleware validates the JWT AND checks that the role is "admin".
// Returns 401 for missing/invalid token, 403 for insufficient role.
func RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenStr := extractToken(r)
		if tokenStr == "" {
			http.Error(w, "Authorization token required", http.StatusUnauthorized)
			return
		}
		claims, err := parseToken(tokenStr)
		if err != nil {
			http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
			return
		}
		if claims.Role != "admin" {
			http.Error(w, "Forbidden: admin access required", http.StatusForbidden)
			return
		}
		ctx := context.WithValue(r.Context(), claimsKey, claims)
		next(w, r.WithContext(ctx))
	}
}

// ClaimsFromContext retrieves the AdminClaims from the request context.
func ClaimsFromContext(ctx context.Context) *AdminClaims {
	claims, _ := ctx.Value(claimsKey).(*AdminClaims)
	return claims
}
