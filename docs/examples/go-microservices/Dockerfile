FROM golang:1.21-alpine

WORKDIR /workspace

# Install git and build tools
RUN apk add --no-cache git

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Default command (override in docker-compose)
CMD ["go", "run", "main.go"]
