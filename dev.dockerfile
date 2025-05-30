# Use the latest Go 1.22 image (or upgrade to 1.23 if needed)
FROM golang:1.24.1-alpine

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
RUN go mod download

EXPOSE 8080
ENTRYPOINT ["air"]