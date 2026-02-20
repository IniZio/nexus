FROM golang:1.21-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o daemon ./cmd/daemon

FROM alpine:3.19

RUN apk --no-cache add ca-certificates

WORKDIR /workspace

COPY --from=builder /app/daemon /usr/local/bin/daemon

EXPOSE 8080

ENV PORT=8080
ENV WORKSPACE_DIR=/workspace
ENV TOKEN_FILE=/run/secrets/token

CMD ["daemon", "--port", "8080", "--workspace-dir", "/workspace"]
