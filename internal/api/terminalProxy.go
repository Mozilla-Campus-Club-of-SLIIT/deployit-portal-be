package api

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"devops-lab-backend/internal/docker"
)

// TerminalProxyHandler proxies requests to the ttyd port for a session.
func TerminalProxyHandler(sm *docker.SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/terminal/")
		parts := strings.SplitN(path, "/", 2)
		sessionID := parts[0]
		if sessionID == "" {
			http.Error(w, "Missing session ID", http.StatusBadRequest)
			return
		}

		session, ok := sm.GetSession(sessionID)
		if !ok {
			http.Error(w, "Session not found", http.StatusNotFound)
			return
		}

		targetURL, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", session.HostPort))
		if err != nil {
			http.Error(w, "Invalid target", http.StatusInternalServerError)
			return
		}

		basePrefix := "/terminal/" + sessionID
		proxy := httputil.NewSingleHostReverseProxy(targetURL)
		proxy.Director = func(req *http.Request) {
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.Host = targetURL.Host
			req.URL.Path = strings.TrimPrefix(req.URL.Path, basePrefix)
			if req.URL.Path == "" {
				req.URL.Path = "/"
			}
		}
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "Terminal unavailable", http.StatusBadGateway)
		}

		proxy.ServeHTTP(w, r)
	}
}
