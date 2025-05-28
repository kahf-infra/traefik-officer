# Use the same Go version for both stages to avoid compatibility issues
FROM golang:1.24.1-alpine AS builder

WORKDIR /app

# Copy dependency files first for better layer caching
COPY go.mod go.sum ./

# Install dependencies and build tools
RUN apk add --no-cache git make gcc musl-dev

# Download dependencies (go mod download is more efficient than go install for dependencies)
RUN go mod download

# Copy the rest of the application
COPY pkg/ ./pkg/

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o /traefikofficer ./pkg

# Final stage - use scratch or alpine for smallest image
FROM alpine:3.19

# Install CA certificates for HTTPS requests
RUN apk add --no-cache ca-certificates

# Copy the binary from builder
COPY --from=builder /traefikofficer /app/traefikofficer

# Set working directory
WORKDIR /app

# Run as non-root user
RUN adduser -D appuser && chown -R appuser /app
USER appuser

ENTRYPOINT [ "/app/traefikofficer" ]