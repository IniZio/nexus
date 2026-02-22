FROM golang:1.21-alpine AS builder

WORKDIR /app

# Install dependencies
RUN apk add --no-cache git

# Copy shared module
COPY shared/ /app/shared/

# Copy service code
COPY services/api-gateway/ /app/services/api-gateway/

WORKDIR /app/services/api-gateway

# Build
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -o api-gateway .

# Final stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates wget

WORKDIR /root/

COPY --from=builder /app/services/api-gateway/api-gateway .

EXPOSE 8080

CMD ["./api-gateway"]
