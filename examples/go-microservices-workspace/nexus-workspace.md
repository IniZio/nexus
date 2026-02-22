# Nexus Workspace Guide: Go Microservices

This guide explains how to develop the Go microservices example with Nexus workspaces.

## Quick Start with Nexus

### 1. Create the Workspace

```bash
# From the repository root
nexus workspace create go-microservices \
  --display-name "Go Microservices Demo" \
  --dind  # Enable Docker-in-Docker
```

### 2. Start the Services

```bash
# SSH into the workspace
nexus workspace console go-microservices

# Navigate to the example
cd examples/go-microservices-workspace

# Build and start all services
docker-compose up -d --build

# Check status
docker-compose ps
```

### 3. Set Up Port Forwarding

From your **host machine** (in a new terminal):

```bash
# Forward API Gateway (main entry point)
nexus workspace port add go-microservices 8080

# Optional: Forward individual services for direct testing
nexus workspace port add go-microservices 8081  # Auth service
nexus workspace port add go-microservices 8082  # User service
nexus workspace port add go-microservices 5432  # PostgreSQL

# Verify
nexus workspace port list go-microservices
```

### 4. Test the API

```bash
# Health check
curl http://localhost:8080/health

# Register user
curl -X POST http://localhost:8080/api/auth/register \
  -H "Content-Type: application/json" \
  -d '{"name":"Demo User","email":"demo@example.com","password":"password123"}'

# Login
curl -X POST http://localhost:8080/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"demo@example.com","password":"password123"}'

# Use the returned token for protected endpoints
export TOKEN="your-jwt-token"

# Get current user
curl http://localhost:8080/api/me \
  -H "Authorization: Bearer $TOKEN"
```

## Working with the Workspace

### Development Workflow

```bash
# 1. Start workspace
nexus workspace start go-microservices

# 2. Forward ports
nexus workspace port add go-microservices 8080

# 3. SSH in for development
nexus workspace console go-microservices

# 4. Edit code (from workspace or sync)
```

### Making Code Changes

Since the workspace uses file sync, you can edit code from your host IDE:

1. **Edit on Host**: Modify files in `.worktrees/go-microservices/`
2. **Changes Sync**: Automatically synced to workspace
3. **Rebuild**: From workspace console:
   ```bash
   docker-compose build api-gateway
   docker-compose up -d api-gateway
   ```

### Hot Reload Development

For faster development, run services outside Docker:

```bash
# SSH into workspace
nexus workspace console go-microservices

cd examples/go-microservices-workspace

# Start PostgreSQL only
docker-compose up -d postgres

# Run services directly with hot reload
# (Install air: go install github.com/cosmtrek/air@latest)

cd services/api-gateway
air

# In another terminal
cd services/auth-service
air
```

## Checkpointing in Microservices

### Why Checkpoints Matter

Microservices have complex state. Checkpoints help:
1. **Database Migrations**: Checkpoint before schema changes
2. **API Changes**: Save working state before refactoring
3. **Dependency Updates**: Test updates safely

### Creating Checkpoints

```bash
# After initial setup
nexus workspace checkpoint create go-microservices --name "services-running"

# After database setup with test data
nexus workspace checkpoint create go-microservices --name "db-seeded"

# Before major refactoring
nexus workspace checkpoint create go-microservices --name "before-api-v2"
```

### Rolling Back

```bash
# If a change breaks everything
nexus workspace restore go-microservices <before-api-v2-id>

# Services restart with old code and data
nexus workspace exec go-microservices -- docker-compose up -d
```

## Service-Specific Development

### Working on Auth Service

```bash
# From workspace
nexus workspace exec go-microservices -- docker-compose logs -f auth-service

# Restart just auth service after changes
nexus workspace exec go-microservices -- docker-compose restart auth-service

# Test directly
nexus workspace exec go-microservices -- curl http://localhost:8081/health
```

### Database Migrations

```bash
# SSH into workspace
nexus workspace console go-microservices

# Run migrations (if you have a migration tool)
docker-compose exec postgres psql -U postgres -d microservices -f /migrations/v2.sql

# Or use psql directly
docker-compose exec postgres psql -U postgres -d microservices
```

### Adding a New Service

1. **Create service directory**:
   ```bash
   mkdir services/inventory-service
   cd services/inventory-service
   go mod init nexus/inventory-service
   ```

2. **Add to docker-compose.yml**:
   ```yaml
   inventory:
     build: ./services/inventory-service
     ports:
       - "8083:8083"
   ```

3. **Update API Gateway** to route to new service

4. **Checkpoint before testing**:
   ```bash
   nexus workspace checkpoint create go-microservices --name "before-inventory-service"
   ```

## Debugging

### View Service Logs

```bash
# All services
nexus workspace services logs go-microservices api-gateway
nexus workspace services logs go-microservices auth-service
nexus workspace services logs go-microservices user-service

# Follow logs
nexus workspace exec go-microservices -- docker-compose logs -f
```

### Check Service Health

```bash
nexus workspace health go-microservices
```

### Network Issues

```bash
# Test connectivity between services
nexus workspace exec go-microservices -- docker-compose exec api-gateway wget -O- http://auth-service:8081/health

# View network
docker network ls
docker network inspect go-microservices_workspace-network
```

## Advanced Usage

### Load Testing

```bash
# Install hey or similar on host
hey -n 1000 -c 10 http://localhost:8080/health

# Or from workspace
nexus workspace exec go-microservices -- apt-get install -y apache2-utils
nexus workspace exec go-microservices -- ab -n 1000 -c 10 http://localhost:8080/health
```

### Database Management

```bash
# Connect to PostgreSQL from host (with port 5432 forwarded)
psql -h localhost -U postgres -d microservices

# Or from workspace
nexus workspace exec go-microservices -- docker-compose exec postgres psql -U postgres -d microservices
```

### Environment Variables

Each service can have its own environment:

```yaml
# docker-compose.yml
services:
  api-gateway:
    environment:
      - AUTH_SERVICE_URL=http://auth-service:8081
      - USER_SERVICE_URL=http://user-service:8082
      - JWT_SECRET=your-secret-key
```

## Workspace Lifecycle

### Daily Start

```bash
# Quick start script
#!/bin/bash
nexus workspace start go-microservices
nexus workspace port add go-microservices 8080
echo "Go to http://localhost:8080"
```

### Pausing During Breaks

```bash
nexus workspace pause go-microservices
# Later...
nexus workspace resume go-microservices
# Services still running!
```

### Cleanup

```bash
# Stop and destroy
nexus workspace stop go-microservices
nexus workspace destroy go-microservices

# Or force destroy (lose all data)
nexus workspace destroy go-microservices --force
```

## Best Practices

### 1. Checkpoint Strategy

```bash
# Before any schema change
nexus workspace checkpoint create go-microservices --name "schema-v1"

# Before API contract changes
nexus workspace checkpoint create go-microservices --name "api-v1-stable"

# After successful deployment simulation
nexus workspace checkpoint create go-microservices --name "deploy-ready"
```

### 2. Service Isolation

Test services individually:

```bash
# Start just the service you're working on
docker-compose up -d auth-service

# Test it directly
curl http://localhost:8081/health
```

### 3. Shared Code Management

The `shared/` directory is mounted in all services:

```dockerfile
# In service Dockerfile
COPY shared/ /app/shared/
COPY services/my-service/ /app/
```

### 4. Configuration Management

Use environment variables for service URLs:

```go
// In your service code
authURL := os.Getenv("AUTH_SERVICE_URL")
if authURL == "" {
    authURL = "http://auth-service:8081" // default
}
```

### 5. Health Checks

Always implement health endpoints:

```go
// In each service
func healthHandler(w http.ResponseWriter, r *http.Request) {
    json.NewEncoder(w).Encode(map[string]string{
        "status": "healthy",
        "service": "auth-service",
    })
}
```

## Troubleshooting

### Service Won't Start

```bash
# Check logs
nexus workspace services logs go-microservices <service-name>

# Rebuild
docker-compose build --no-cache <service-name>
docker-compose up -d <service-name>
```

### Database Connection Issues

```bash
# Verify PostgreSQL is running
nexus workspace exec go-microservices -- docker-compose ps postgres

# Check connection from service
nexus workspace exec go-microservices -- docker-compose exec api-gateway wget -O- postgres:5432
```

### Port Conflicts

```bash
# Use different ports
nexus workspace port add go-microservices 9090  # Instead of 8080

# Update docker-compose.yml to match
```

## Example Development Session

```bash
# 1. Create workspace
nexus workspace create go-microservices --dind

# 2. Start services
nexus workspace console go-microservices
cd examples/go-microservices-workspace
docker-compose up -d --build

# 3. Forward port and test
nexus workspace port add go-microservices 8080
curl http://localhost:8080/health

# 4. Create baseline checkpoint
nexus workspace checkpoint create go-microservices --name "baseline"

# 5. Edit code on host (in .worktrees/go-microservices/)
# 6. Changes auto-sync to workspace

# 7. Rebuild and test
nexus workspace exec go-microservices -- docker-compose up -d --build api-gateway

# 8. Checkpoint progress
nexus workspace checkpoint create go-microservices --name "feature-x-complete"

# 9. Pause at end of day
nexus workspace pause go-microservices

# 10. Resume next day
nexus workspace resume go-microservices
nexus workspace port add go-microservices 8080
```

## See Also

- [Main README](./README.md) - Project overview
- [Go Modules Reference](https://go.dev/ref/mod)
- [Docker Compose Documentation](https://docs.docker.com/compose/)
