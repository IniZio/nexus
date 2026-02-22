FROM golang:1.21-alpine AS builder

WORKDIR /app

# Install dependencies
RUN apk add --no-cache git

# Copy shared module
COPY shared/ /app/shared/

# Copy service code
COPY services/auth-service/ /app/services/auth-service/

WORKDIR /app/services/auth-service

# Build
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -o auth-service .

# Final stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates wget

WORKDIR /root/

COPY --from=builder /app/services/auth-service/auth-service .

EXPOSE 8081

CMD ["./auth-service"]
