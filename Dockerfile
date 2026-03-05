# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download all dependencies
RUN go mod download

# Copy the source code
COPY . .

# Build the application
# CGO_ENABLED=0 ensures a statically linked binary that runs on Alpine
RUN CGO_ENABLED=0 GOOS=linux go build -o main ./cmd/main.go

# Final stage
FROM alpine:latest

WORKDIR /app

# Install CA certificates to make secure API calls (crucial for connecting to GCP/Firestore)
RUN apk --no-cache add ca-certificates

# Copy the pre-built binary file from the previous stage
COPY --from=builder /app/main .

# Ensure the binary is executable
RUN chmod +x ./main

# Expose the application port
EXPOSE 8080

# Run the executable
CMD ["./main"]
