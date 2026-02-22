# Docker Compose Multi-Service Workspace

A full-stack application demonstrating Nexus Workspace with multi-service orchestration.

## Overview

This example showcases:
- **Multi-service architecture**: PostgreSQL, Redis, Node.js API, React frontend
- **Docker-in-Docker**: Running docker-compose inside Nexus workspace
- **Port forwarding**: Accessing services from host machine
- **Service dependencies**: Proper startup order with health checks
- **Persistent data**: Database volumes that survive restarts

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Nexus Workspace Container               │
│                                                              │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐ │
│  │ PostgreSQL  │  │   Redis     │  │   Node.js API       │ │
│  │   :5432     │  │   :6379     │  │   :3000             │ │
│  └─────────────┘  └─────────────┘  └─────────────────────┘ │
│                                                              │
│  ┌─────────────────────────────────────────────────────┐   │
│  │           React Frontend (Vite)                     │   │
│  │                   :5173                             │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                              │
└─────────────────────────────────────────────────────────────┘
         ↕
    Port Forwarding
         ↕
   Host Machine (localhost)
```

## Project Structure

```
docker-compose-workspace/
├── docker-compose.yml          # Multi-service orchestration
├── Dockerfile                  # Custom API image
├── api/                        # Node.js API service
│   ├── package.json
│   ├── server.js
│   └── routes/
│       ├── health.js
│       └── users.js
├── frontend/                   # React frontend
│   ├── package.json
│   ├── vite.config.js
│   └── src/
│       ├── App.jsx
│       └── main.jsx
├── init-db/                    # Database initialization
│   └── init.sql
├── README.md                   # This file
└── nexus-workspace.md          # Nexus workspace guide
```

## Services

| Service | Port | Description |
|---------|------|-------------|
| PostgreSQL | 5432 | Primary database with persistent storage |
| Redis | 6379 | Caching and session store |
| Node.js API | 3000 | REST API with health checks |
| React Frontend | 5173 | Vite-based React development server |

## Running Locally (Without Nexus)

```bash
# Clone/navigate to example
cd examples/docker-compose-workspace

# Start all services
docker-compose up -d

# View logs
docker-compose logs -f

# Access services:
# - Frontend: http://localhost:5173
# - API: http://localhost:3000
# - API Health: http://localhost:3000/health
```

## API Endpoints

- `GET /health` - Service health check
- `GET /api/users` - List all users
- `GET /api/users/:id` - Get user by ID
- `POST /api/users` - Create new user
- `GET /api/cache/test` - Test Redis cache

## Frontend Features

- User list with real-time updates
- Add new users form
- Health status dashboard
- Redis cache testing interface

## Development Workflow

```bash
# Start services
docker-compose up -d

# Watch API logs
docker-compose logs -f api

# Run database migrations
docker-compose exec api npm run migrate

# Restart a service
docker-compose restart frontend
```

## Data Persistence

PostgreSQL data persists in a Docker volume:

```yaml
volumes:
  postgres_data:
    driver: local
```

To reset data:
```bash
docker-compose down -v  # Remove volumes
docker-compose up -d     # Fresh start
```

## Next Steps

See `nexus-workspace.md` for detailed instructions on running this in a Nexus workspace with checkpointing, port forwarding, and SSH access.
