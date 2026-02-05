# DevOps Lab Backend MVP: Local Run Instructions

## 1. Start the Go Server

1. Ensure Docker is running locally.
2. Install Go 1.21+.
3. In the project root, run:

```bash
go run ./cmd/main.go
```

The server will listen on port 8080 and serve both the backend API and frontend.

---

## 2. Access the Frontend

Open your browser and navigate to:

```
http://localhost:8080/
```

This loads the interactive lab management interface.

Click "Start Lab" to create a new session with a web terminal.

---

## 3. API Endpoints

### POST /start-lab

Create a new lab session:

```bash
curl -X POST http://localhost:8080/start-lab \
  -H "Content-Type: application/json" \
  -d '{"labType": "linux"}'
```

Response:
```json
{
  "sessionID": "...",
  "url": "http://localhost:<port>"
}
```

### POST /stop-lab

Stop a lab session:

```bash
curl -X POST http://localhost:8080/stop-lab \
  -H "Content-Type: application/json" \
  -d '{"sessionID": "..."}'
```

---

## 4. Auto Cleanup

- Sessions auto-stop after 30 minutes total or 5 minutes idle.
- To test idle cleanup, do not interact with the terminal for 5 minutes.
- To test total time, keep session open for 30 minutes.
- Check server logs for `[CLEANUP]` events.

---

## 5. Debug Common Issues

- **Docker not running:** Ensure Docker daemon is started.
- **Port collision:** Backend avoids collisions, but check for other local services using ports 20000-29999.
- **Session limit reached:** Max 50 sessions; wait or stop sessions via `/stop-lab`.
- **Container fails to start:** Check logs for `[ERROR]` messages.
- **Web terminal not loading:** Ensure browser can access the returned port; check firewall settings.

---

For further troubleshooting, review logs and ensure all dependencies are installed.
