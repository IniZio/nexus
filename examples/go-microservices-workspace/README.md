# Go Microservices Workspace

A multi-service Go application demonstrating Nexus Workspace with microservice architecture and inter-service communication.

## Overview

This example showcases:
- **Multi-service Go architecture**: API Gateway, Auth Service, User Service
- **Docker Compose orchestration**: All services running together
- **Service communication**: REST API calls between services
- **Shared Go workspace**: Common code and dependencies
- **Port forwarding**: Access the API Gateway from host

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Nexus Workspace Container               │
│                                                              │
│  ┌─────────────────┐         ┌─────────────────┐           │
│  │  API Gateway    │────────▶│  Auth Service   │           │
│  │     :8080       │         │     :8081       │           │
│  │                 │         └─────────────────┘           │
│  │  • Routes       │                                       │
│  │  • Auth         │         ┌─────────────────┐           │
│  │  • Load Balance │────────▶│  User Service   │           │
│  │                 │         │     :8082       │           │
│  └─────────────────┘         └─────────────────┘           │
│                                                              │
│  ┌─────────────────────────────────────────────────────┐   │
│  │              PostgreSQL Database                    │   │
│  │     (User data, Auth tokens)                        │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                              │
└─────────────────────────────────────────────────────────────┘
         ↕
    Port Forwarding (:8080)
         ↕
   Host Machine
```

## Project Structure

```
go-microservices-workspace/
├── docker-compose.yml          # Service orchestration
├── go.work                     # Go workspace file
├── shared/                     # Shared code
│   ├── go.mod
│   ├── models/
│   │   └── user.go
│   └── middleware/
│       └── auth.go
├── services/
│   ├── api-gateway/           # API Gateway service
│   │   ├── main.go
│   │   ├── handlers/
│   │   └── go.mod
│   ├── auth-service/          # Authentication service
│   │   ├── main.go
│   │   ├── handlers/
│   │   └── go.mod
│   └── user-service/          # User management service
│       ├── main.go
│       ├── handlers/
│       └── go.mod
├── init-db/
│   └── init.sql
├── README.md                   # This file
└── nexus-workspace.md          # Nexus workspace guide
```

## Services

| Service | Port | Description |
|---------|------|-------------|
| API Gateway | 8080 | Entry point, routing, authentication |
| Auth Service | 8081 | JWT token generation and validation |
| User Service | 8082 | User CRUD operations |
| PostgreSQL | 5432 | Shared database |

## API Endpoints (via Gateway)

### Public Endpoints
- `POST /api/auth/register` - Register new user
- `POST /api/auth/login` - Login and get JWT token
- `GET /health` - Service health check

### Protected Endpoints (require JWT)
- `GET /api/users` - List all users
- `GET /api/users/:id` - Get user by ID
- `PUT /api/users/:id` - Update user
- `DELETE /api/users/:id` - Delete user
- `GET /api/me` - Get current user profile

## Running Locally (Without Nexus)

```bash
# Navigate to example
cd examples/go-microservices-workspace

# Start all services
docker-compose up -d

# Wait for services to start
sleep 10

# Check health
curl http://localhost:8080/health

# Register a user
curl -X POST http://localhost:8080/api/auth/register \
  -H "Content-Type: application/json" \
  -d '{"name":"Alice","email":"alice@example.com","password":"secret123"}'

# Login
curl -X POST http://localhost:8080/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"alice@example.com","password":"secret123"}'
```

## Development Workflow

### Making Changes

Each service is independent with its own `go.mod`:

```bash
# Edit auth service
cd services/auth-service
vim main.go

# Rebuild
docker-compose build auth-service
docker-compose up -d auth-service
```

### Shared Code

The `shared/` directory contains common types and middleware:

```go
// Import in any service
import "nexus/shared/models"

// Use shared types
user := models.User{ID: 1, Name: "Alice"}
```

### Testing Services

```bash
# Test auth service directly
curl http://localhost:8081/health

# Test user service directly
curl http://localhost:8082/health

# Test through gateway
curl http://localhost:8080/health
```

## Next Steps

See `nexus-workspace.md` for detailed instructions on running this in a Nexus workspace.
