FROM golang:1.21-alpine AS builder

WORKDIR /app

# Install dependencies
RUN apk add --no-cache git

# Copy shared module
COPY shared/ /app/shared/

# Copy service code
COPY services/user-service/ /app/services/user-service/

WORKDIR /app/services/user-service

# Build
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -o user-service .

# Final stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates wget

WORKDIR /root/

COPY --from=builder /app/services/user-service/user-service .

EXPOSE 8082

CMD ["./user-service"]
