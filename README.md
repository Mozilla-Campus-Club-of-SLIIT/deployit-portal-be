# DevOps Lab Portal Backend

This is the Go backend API for the DevOps Lab platform. It dynamically provisions temporary web-based terminals (using `tsl0922/ttyd` running an Ubuntu bash shell) directly on **Google Cloud Run**.

## Prerequisites

- Go 1.21+
- A Google Cloud Project with the **Cloud Run Admin API** enabled.
- Google Cloud Service Account with the following roles:
  - **Cloud Run Admin** (`roles/run.admin`)
  - **Service Account User** (`roles/iam.serviceAccountUser`)

## Environment Setup

1. Copy `.env.sample` to `.env`.
   ```bash
   cp .env.sample .env
   ```
2. Open `.env` and fill in your Google Cloud Project ID and your Service Account JSON credentials exactly as requested by the template.

## Running the Backend Locally

To start the Go server, and download any missing Go modules:

```bash
go mod tidy
go run ./cmd/main.go
```

The server will start listening on `http://localhost:8080`.

---

## API Endpoints

### `POST /start-lab`
Dynamically provisions a new Cloud Run service containing a web terminal.
```bash
curl -X POST http://localhost:8080/start-lab \
  -H "Content-Type: application/json" \
  -d '{"labType": "ubuntu"}'
```

### `POST /stop-lab`
Explicitly destroys the provisioned Cloud Run service.
```bash
curl -X POST http://localhost:8080/stop-lab \
  -H "Content-Type: application/json" \
  -d '{"sessionID": "..."}'
```

---

## Auto Cleanup & Lifespan
- The backend features a garbage-collection loop.
- Any requested container will automatically be deleted via the Google Cloud Run API exactly **5 minutes** after creation to prevent runaway hosting costs.
