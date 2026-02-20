# Service Port Awareness

When running services in a Nexus workspace (like RESTful APIs, databases, etc.), the workspace needs to be aware of which ports are mapped and how to access them.

## Port Discovery

The workspace daemon exposes a port discovery mechanism:

### Automatic Port Detection

Services started via lifecycle hooks (docker-compose, etc.) are automatically detected:

```json
{
  "version": "1.0",
  "hooks": {
    "post-start": [
      {
        "name": "start-api",
        "command": "docker",
        "args": ["compose", "up", "-d", "api"],
        "ports": [3000, 3001]
      }
    ]
  },
  "services": {
    "api": {
      "port": 3000,
      "healthCheck": "/health"
    },
    "postgres": {
      "port": 5432,
      "internal": true
    }
  }
}
```

### Environment Variables

Services can expose their URLs via environment variables:

```json
{
  "services": {
    "api": {
      "port": 3000,
      "env": {
        "API_URL": "http://localhost:3000",
        "API_INTERNAL": "http://api:3000"
      }
    }
  }
}
```

These are available to:
- Lifecycle hooks
- SDK clients
- Agent tools

## RESTful API Case

For a typical RESTful API service:

### 1. Configure Service in lifecycle.json

```json
{
  "version": "1.0",
  "hooks": {
    "post-start": [
      {
        "name": "start-api",
        "command": "docker",
        "args": ["compose", "up", "-d", "api"]
      }
    ]
  },
  "services": {
    "api": {
      "name": "My API",
      "port": 3000,
      "scheme": "http",
      "healthCheck": {
        "path": "/health",
        "interval": 5,
        "timeout": 3
      }
    }
  }
}
```

### 2. Access from Agent

The OpenCode plugin exposes service info to the agent:

```typescript
// Agent can query available services
const services = await workspaceClient.getServices();
// Returns: [{ name: "api", url: "http://localhost:3000", healthy: true }]
```

### 3. Port Mapping Awareness

When services run in Docker, the daemon tracks port mappings:

```
Workspace Container:
  - Port 8080: WebSocket daemon
  - Port 3000: Mapped to api:3000
  - Port 5432: Mapped to postgres:5432
```

The SDK provides the external port to agents:

```javascript
const apiPort = await workspaceClient.getServicePort('api');
// Returns: 3000 (even if internally mapped to 3000)
```

## Dynamic Port Allocation

For development scenarios where ports may conflict:

```json
{
  "services": {
    "api": {
      "portRange": [3000, 3100],
      "autoAssign": true
    }
  }
}
```

The daemon will:
1. Find an available port in the range
2. Map the service to that port
3. Update configuration
4. Notify connected agents

## Port Information API

Query service ports via the SDK:

```typescript
// Get all services
const services = await client.getServices();
// [
//   { name: "api", port: 3000, url: "http://localhost:3000", status: "healthy" },
//   { name: "postgres", port: 5432, internal: true, status: "healthy" }
// ]

// Get specific service
const api = await client.getService('api');
// { port: 3000, url: "http://localhost:3000", health: { status: "up" } }
```

## Docker Compose Integration

When using docker-compose, ports are automatically extracted:

```yaml
# docker-compose.yml
services:
  api:
    image: my-api
    ports:
      - "3000:3000"  # Automatically detected
    environment:
      - PORT=3000
```

The daemon parses docker-compose.yml and extracts port mappings.

## Security Considerations

- Internal services (database, cache) should not be exposed externally
- Use `internal: true` in service config
- Health checks should not expose sensitive endpoints

## Example: Full REST API Setup

```json
{
  "version": "1.0",
  "hooks": {
    "post-start": [
      {
        "name": "start-services",
        "command": "docker",
        "args": ["compose", "up", "-d"]
      }
    ]
  },
  "services": {
    "api": {
      "name": "REST API",
      "port": 3000,
      "scheme": "http",
      "healthCheck": {
        "path": "/api/health",
        "interval": 5
      },
      "env": {
        "API_URL": "http://localhost:3000"
      }
    },
    "postgres": {
      "name": "PostgreSQL",
      "port": 5432,
      "internal": true,
      "env": {
        "DATABASE_URL": "postgres://user:pass@localhost:5432/db"
      }
    }
  }
}
```

## Accessing Services from Code

```javascript
// In your application code
const apiUrl = process.env.API_URL || 'http://localhost:3000';
const dbUrl = process.env.DATABASE_URL;

// Make API calls
fetch(`${apiUrl}/users`)
  .then(res => res.json())
  .then(users => console.log(users));
```
