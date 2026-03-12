# Go Microservices Workspace

**Time:** 25 minutes  
**Stack:** Go 1.21, Chi Router, PostgreSQL, gRPC, API Gateway

## Problem

Developing microservices locally is painful:
- Running multiple services simultaneously
- Port conflicts between services
- Database per service pattern
- Testing inter-service communication

## Architecture

```
┌─────────────┐
│   API GW    │ Port 8080
│   (Chi)     │
└──────┬──────┘
       │
   ┌───┴───┐
   │       │
┌──┴──┐ ┌──┴──┐
│Auth │ │User │
│Svc  │ │Svc  │
│:8081│ │:8082│
└──┬──┘ └──┬──┘
   │       │
┌──┴───────┴──┐
│ PostgreSQL  │
└─────────────┘
```

## Setup

### Prerequisites

- Docker Desktop
- Nexus CLI
- Git repository

## Step 1: Create Workspace

```bash
nexus workspace create go-microservices --dind
```

## Step 2: Project Structure

Create this structure in `.worktrees/go-microservices/`:

```
go-microservices/
├── docker-compose.yml
├── go.mod
├── go.work
├── Dockerfile
└── services/
    ├── api-gateway/
    │   └── main.go
    ├── auth-service/
    │   └── main.go
    └── user-service/
        └── main.go
```

**go.work:**
```go
use (
    ./services/api-gateway
    ./services/auth-service
    ./services/user-service
)
```

**docker-compose.yml:**
```yaml
version: '3.8'
services:
  api-gateway:
    build:
      context: .
      dockerfile: Dockerfile
    ports:
      - "8080:8080"
    environment:
      - AUTH_SERVICE_URL=http://auth-service:8081
      - USER_SERVICE_URL=http://user-service:8082
    depends_on:
      - auth-service
      - user-service
  
  auth-service:
    build:
      context: .
      dockerfile: Dockerfile
    environment:
      - DATABASE_URL=postgres://postgres:postgres@postgres:5432/auth?sslmode=disable
      - JWT_SECRET=your-secret-key
    depends_on:
      - postgres
  
  user-service:
    build:
      context: .
      dockerfile: Dockerfile
    environment:
      - DATABASE_URL=postgres://postgres:postgres@postgres:5432/user?sslmode=disable
    depends_on:
      - postgres
  
  postgres:
    image: postgres:15-alpine
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: postgres
      POSTGRES_DB: postgres
    volumes:
      - postgres_data:/var/lib/postgresql/data
    ports:
      - "5432:5432"

volumes:
  postgres_data:
```

## Step 3: Start Services

```bash
# Enter workspace
nexus workspace ssh go-microservices

# Start all services
$ docker-compose up -d --build

# Check logs
$ docker-compose logs -f
```

## Step 4: Test Services

```bash
# On host
nexus workspace port add go-microservices 8080

# Test health
curl http://localhost:8080/health

# Test auth endpoint
curl -X POST http://localhost:8080/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"test@example.com","password":"password"}'
```

## Result

✅ **What you achieved:**
- Multi-service architecture in single workspace
- API Gateway with reverse proxy
- Individual service databases
- Hot reload for Go development
- Isolated microservice environment

## Development Tips

```bash
# Rebuild single service
$ docker-compose up -d --build auth-service

# View specific service logs
$ docker-compose logs -f user-service

# Run database migrations
$ docker-compose exec auth-service go run ./cmd/migrate
```

## Cleanup

```bash
nexus workspace delete go-microservices
```
