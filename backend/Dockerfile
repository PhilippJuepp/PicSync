# syntax=docker/dockerfile:1

FROM golang:1.25.3-alpine AS builder
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o picsync ./cmd/server

FROM alpine:latest
WORKDIR /app

COPY --from=builder /app/picsync .

RUN apk --no-cache add ca-certificates

EXPOSE 8080

CMD ["./picsync"]