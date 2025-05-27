# Use the latest Go 1.22 image (or upgrade to 1.23 if needed)
FROM golang:1.22-alpine

WORKDIR /app

# Install development tools
RUN apk add --no-cache \
    git \
    curl \
    make \
    gcc \
    musl-dev

# Install Air for live reloading
RUN curl -sSfL https://raw.githubusercontent.com/cosmtrek/air/master/install.sh | \
    sh -s -- -b $(go env GOPATH)/bin

# Copy dependency files first for better caching
COPY go.mod go.sum ./

# Download Go modules with specific versions
RUN go mod download -x && \
    go get golang.org/x/sys@v0.17.0 && \
    go get gopkg.in/fsnotify.v1@v1.4.7 && \
    go get gopkg.in/tomb.v1@v1.0.0-20141024135613-dd632973f1e7

# Create and use non-root user
RUN adduser -D devuser && chown -R devuser /app
USER devuser

EXPOSE 8080
ENTRYPOINT ["air"]