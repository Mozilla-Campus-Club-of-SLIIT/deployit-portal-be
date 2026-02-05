package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"github.com/gorilla/websocket"
	"devops-lab-backend/internal/docker"
	"devops-lab-backend/internal/api"
)

var upgrader = websocket.Upgrader{}

func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket upgrade error:", err)
		return
	}
	defer conn.Close()
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			log.Println("Read error:", err)
			break
		}
		log.Printf("Received: %s", msg)
		if err := conn.WriteMessage(websocket.TextMessage, []byte("Echo: "+string(msg)));
			err != nil {
			log.Println("Write error:", err)
			break
		}
	}
}

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
	dockerClient, err := docker.NewDockerClient()
	if err != nil {
		log.Fatalf("Failed to create Docker client: %v", err)
	}
	sessionManager := docker.NewSessionManager(dockerClient, 50)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", wsHandler)
	mux.HandleFunc("/start-lab", corsMiddleware(api.StartLabHandler(sessionManager, dockerClient)))
	mux.HandleFunc("/stop-lab", corsMiddleware(api.StopLabHandler(sessionManager)))
	mux.HandleFunc("/terminal/", api.TerminalProxyHandler(sessionManager))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if data, err := ioutil.ReadFile("./frontend.html"); err == nil {
			w.Write(data)
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			fmt.Fprintln(w, "Lab Backend Running")
		}
	})
	
	handler := corsMiddlewareGlobal(mux)
	log.Println("Server listening on :8080")
	log.Println("CORS enabled for all origins")
	log.Println("Frontend available at http://localhost:8080/")
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
