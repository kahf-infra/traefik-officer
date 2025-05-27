FROM golang:1.22-alpine AS builder
RUN mkdir /app
WORKDIR /app
ADD pkg/ ./
ADD go.mod ./
ADD go.sum ./
RUN apk add git; \
    go install -u "github.com/hpcloud/tail@latest"; \
    go install -u "github.com/prometheus/client_golang/prometheus@latest"; \
    go install -u "github.com/mitchellh/go-ps@latest"; \
    go install -u "github.com/sirupsen/logrus@latest"; \
    go build -o traefikofficer .

FROM golang:1.13-alpine

COPY --from=builder /app/traefikofficer ./

ENTRYPOINT [ "./traefikofficer" ]
