FROM golang:1.24.2-alpine

WORKDIR /app

RUN apk add --no-cache git curl && \
    curl -sSfL https://raw.githubusercontent.com/cosmtrek/air/master/install.sh | sh -s -- -b $(go env GOPATH)/bin

COPY go.mod go.sum ./
RUN go mod download

COPY pkg/ ./pkg/

RUN go get "github.com/hpcloud/tail" && \
    go get "github.com/prometheus/client_golang/prometheus" && \
    go get "github.com/mitchellh/go-ps" && \
    go get "github.com/sirupsen/logrus"

ENTRYPOINT ["air -v"]
