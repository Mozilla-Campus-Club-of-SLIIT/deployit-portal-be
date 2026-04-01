package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// contextKey is a private type for context keys to avoid collisions.
type contextKey string

const claimsKey contextKey = "claims"

type authzAuditEvent struct {
	Event     string `json:"event"`
	RequestID string `json:"requestId"`
	UserID    string `json:"userId"`
	Route     string `json:"route"`
	Action    string `json:"action"`
	Result    string `json:"result"`
}

func requestIDFromRequest(r *http.Request) string {
	if id := strings.TrimSpace(r.Header.Get("X-Request-Id")); id != "" {
		return id
	}
	if id := strings.TrimSpace(r.Header.Get("X-Correlation-Id")); id != "" {
		return id
	}
	return fmt.Sprintf("req-%d", time.Now().UnixNano())
}

func logAuthzEvent(r *http.Request, userID, action, result string) {
	if userID == "" {
		userID = "unknown"
	}
	evt := authzAuditEvent{
		Event:     "authz",
		RequestID: requestIDFromRequest(r),
		UserID:    userID,
		Route:     r.URL.Path,
		Action:    action,
		Result:    result,
	}
	b, err := json.Marshal(evt)
	if err != nil {
		log.Printf("{\"event\":\"authz\",\"requestId\":\"%s\",\"userId\":\"%s\",\"route\":\"%s\",\"action\":\"%s\",\"result\":\"%s\"}", evt.RequestID, evt.UserID, evt.Route, evt.Action, evt.Result)
		return
	}
	log.Printf("%s", string(b))
}

// AdminClaims holds the JWT payload for authenticated admin users.
type AdminClaims struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

// ValidateJWTSecret ensures the JWT signing secret is configured.
func ValidateJWTSecret() error {
	if strings.TrimSpace(os.Getenv("JWT_SECRET")) == "" {
		return fmt.Errorf("JWT_SECRET must be set")
	}
	return nil
}

// jwtSecret returns the signing secret from the environment.
func jwtSecret() []byte {
	return []byte(os.Getenv("JWT_SECRET"))
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

	// Browser iframes/WebSocket upgrades cannot reliably attach custom Authorization headers.
	// Accept terminal token from query, cookie, or referer for terminal subrequests only.
	if strings.HasPrefix(r.URL.Path, "/api/terminal/") {
		if token := strings.TrimSpace(r.URL.Query().Get("token")); token != "" {
			return token
		}
		if c, err := r.Cookie("terminal_token"); err == nil {
			if token := strings.TrimSpace(c.Value); token != "" {
				return token
			}
		}
		if ref := strings.TrimSpace(r.Header.Get("Referer")); ref != "" {
			if u, err := url.Parse(ref); err == nil && strings.HasPrefix(u.Path, "/api/terminal/") {
				if token := strings.TrimSpace(u.Query().Get("token")); token != "" {
					return token
				}
			}
		}
	}

	return ""
}

// RequireAuth middleware validates the JWT and injects claims into the request context.
// Returns 401 if the token is missing or invalid.
func RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenStr := extractToken(r)
		if tokenStr == "" {
			logAuthzEvent(r, "", "require_auth", "deny")
			http.Error(w, "Authorization token required", http.StatusUnauthorized)
			return
		}
		claims, err := parseToken(tokenStr)
		if err != nil {
			logAuthzEvent(r, "", "require_auth", "deny")
			http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
			return
		}
		logAuthzEvent(r, claims.UserID, "require_auth", "allow")
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
			logAuthzEvent(r, "", "require_admin", "deny")
			http.Error(w, "Authorization token required", http.StatusUnauthorized)
			return
		}
		claims, err := parseToken(tokenStr)
		if err != nil {
			logAuthzEvent(r, "", "require_admin", "deny")
			http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
			return
		}
		if claims.Role != "admin" {
			logAuthzEvent(r, claims.UserID, "require_admin", "deny")
			http.Error(w, "Forbidden: admin access required", http.StatusForbidden)
			return
		}
		logAuthzEvent(r, claims.UserID, "require_admin", "allow")
		ctx := context.WithValue(r.Context(), claimsKey, claims)
		next(w, r.WithContext(ctx))
	}
}

// ClaimsFromContext retrieves the AdminClaims from the request context.
func ClaimsFromContext(ctx context.Context) *AdminClaims {
	claims, _ := ctx.Value(claimsKey).(*AdminClaims)
	return claims
}
