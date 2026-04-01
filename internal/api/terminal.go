package api

import (
	"devops-lab-backend/internal/cloudrun"
	"devops-lab-backend/internal/k8s"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"k8s.io/client-go/rest"
)

func buildFrameAncestorsPolicy(allowedOrigins map[string]struct{}) string {
	ancestors := []string{"'self'"}
	for origin := range allowedOrigins {
		ancestors = append(ancestors, origin)
	}
	return "frame-ancestors " + strings.Join(ancestors, " ")
}

func TerminalProxyHandler(sm *cloudrun.SessionManager, kc *k8s.K8sClient, allowedOrigins map[string]struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[TERMINAL] Incoming request: %s %s (Upgrade: %s)", r.Method, r.URL.Path, r.Header.Get("Upgrade"))

		claims := ClaimsFromContext(r.Context())
		if claims == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		origin := r.Header.Get("Origin")
		if origin != "" {
			if _, ok := allowedOrigins[origin]; !ok {
				http.Error(w, "Origin not allowed", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Content-Security-Policy", buildFrameAncestorsPolicy(allowedOrigins))

		sessionID := strings.TrimPrefix(r.URL.Path, "/api/terminal/")
		if i := strings.Index(sessionID, "/"); i != -1 {
			sessionID = sessionID[:i]
		}

		session, ok := sm.GetSession(sessionID)
		if !ok {
			log.Printf("[TERMINAL] Error: Session %s not found", sessionID)
			http.Error(w, "Session not found", http.StatusNotFound)
			return
		}

		if claims.Role != "admin" && session.UserID != claims.UserID {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		if !session.IsK8s {
			log.Printf("[TERMINAL] Error: Session %s is not a Kubernetes session", sessionID)
			http.Error(w, "Not a K8s session", http.StatusBadRequest)
			return
		}

		// Find the pod with wait logic to prevent immediate 503s
		log.Printf("[TERMINAL] Searching for pod in namespace %s with label app=challenge-app (Upgrade: %s)...", session.Namespace, r.Header.Get("Upgrade"))
		podName, err := kc.FindPod(r.Context(), session.Namespace, "app=challenge-app")
		if err != nil {
			log.Printf("[TERMINAL] Pod discovery failed for session %s: %v", sessionID, err)
			http.Error(w, "Terminal is still starting up. Please refresh in a moment. Error: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		log.Printf("[TERMINAL] Found pod: %s for session %s", podName, sessionID)

		// 0-indexed terminal ID (0, 1, or 2)
		terminalID := r.URL.Query().Get("terminal")
		port := 9000
		if terminalID == "1" {
			port = 9001
		} else if terminalID == "2" {
			port = 9002
		}

		config, err := kc.GetRestConfig(r.Context())
		if err != nil {
			log.Printf("[TERMINAL] K8s config retrieval failed: %v", err)
			http.Error(w, "K8s config error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("[TERMINAL] GKE API Host: %s, Terminal: %d", config.Host, port)

		// Create the proxy to the API server's pod proxy endpoint
		// Format: https://{host}/api/v1/namespaces/{ns}/pods/{pod}:8080/proxy/
		target, err := url.Parse(config.Host)
		if err != nil {
			http.Error(w, "Invalid host: "+err.Error(), http.StatusInternalServerError)
			return
		}

		proxy := httputil.NewSingleHostReverseProxy(target)

		// Set up transport with K8s authentication
		rt, err := rest.TransportFor(config)
		if err != nil {
			http.Error(w, "Transport error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		proxy.Transport = rt

		// Handle WebSocket upgrade
		if strings.ToLower(r.Header.Get("Upgrade")) == "websocket" {
			proxyWebSocket(w, r, config, sessionID, session.Namespace, podName, port)
			return
		}

		// Rewrite the path for standard HTTP requests
		proxy.Director = func(req *http.Request) {
			subpath := strings.TrimPrefix(r.URL.Path, "/api/terminal/"+sessionID)
			if subpath == "" {
				subpath = "/"
			}
			if !strings.HasPrefix(subpath, "/") {
				subpath = "/" + subpath
			}

			req.URL.Scheme = "https"
			req.URL.Host = target.Host
			req.URL.Path = fmt.Sprintf("/api/v1/namespaces/%s/pods/%s:%d/proxy%s", 
				session.Namespace, podName, port, subpath)
			req.Host = target.Host
			log.Printf("[TERMINAL] Proxying HTTP to Port %d: %s", port, req.URL.String())
		}

		proxy.ServeHTTP(w, r)
	}
}

func proxyWebSocket(w http.ResponseWriter, r *http.Request, config *rest.Config, sessionID, ns, podName string, port int) {
	// Extract the subpath after /api/terminal/{id}
	idx := strings.Index(r.URL.Path, "/api/terminal/"+sessionID)
	subpath := "/"
	if idx != -1 {
		subpath = r.URL.Path[idx+len("/api/terminal/")+len(sessionID):]
		if subpath == "" {
			subpath = "/"
		}
	}
	if !strings.HasPrefix(subpath, "/") {
		subpath = "/" + subpath
	}

	// GKE API Server WebSocket proxy path to sidecar on port 9000/9001/9002
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s:%d/proxy%s", ns, podName, port, subpath)
	
	targetURL, _ := url.Parse(config.Host)
	targetURL.Path = path

	log.Printf("[TERMINAL] Hijacking for WebSocket proxy to Port %d: %s", port, targetURL.String())

	// For GKE, we need to handle the WebSocket proxying carefully.
	// Since httputil.ReverseProxy doesn't naturally support hijacking for all K8s versions,
	// we'll use the Director + Transport approach but ensure we don't buffer.
	
	transport, err := rest.TransportFor(config)
	if err != nil {
		log.Printf("[TERMINAL] Failed to create transport for WS: %v", err)
		http.Error(w, "Transport error", 500)
		return
	}

	proxy := &httputil.ReverseProxy{
		Director: func(out *http.Request) {
			out.URL = targetURL
			out.Host = targetURL.Host
			// Copy important headers for GKE auth
			if config.BearerToken != "" {
				out.Header.Set("Authorization", "Bearer "+config.BearerToken)
			}
			log.Printf("[TERMINAL] WS Director (Port %d): %s %s", port, out.Method, out.URL.String())
		},
		Transport: transport,
		// Disable buffering to allow real-time terminal stream
		FlushInterval: -1,
	}
	proxy.ServeHTTP(w, r)
}
