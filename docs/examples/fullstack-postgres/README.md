# Fullstack + PostgreSQL Workspace

**Time:** 20 minutes  
**Stack:** React frontend, Node.js API, PostgreSQL database

## Problem

Fullstack development requires orchestrating three layers:
- Frontend development server (Vite/Webpack)
- Backend API server
- Database with migrations and seeds

Managing all three locally leads to version conflicts and setup headaches.

## Architecture

```
┌─────────────────┐
│  React Frontend │ Port 5173
│   (Vite dev)    │
└────────┬────────┘
         │ API calls
┌────────▼────────┐
│   Node.js API   │ Port 3000
│  (Express.js)   │
└────────┬────────┘
         │ SQL
┌────────▼────────┐
│   PostgreSQL    │ Port 5432
└─────────────────┘
```

## Setup

### Prerequisites

- Docker Desktop
- Nexus CLI
- Git repository

## Step 1: Create Workspace

```bash
nexus workspace create fullstack-app
```

## Step 2: Project Structure

Create in `.worktrees/fullstack-app/`:

```
fullstack-app/
├── docker-compose.yml
├── Dockerfile
├── package.json (root)
├── frontend/
│   ├── package.json
│   ├── vite.config.js
│   └── src/
└── backend/
    ├── package.json
    └── src/
        └── server.js
```

**docker-compose.yml:**
```yaml
version: '3.8'
services:
  frontend:
    build: .
    ports:
      - "5173:5173"
    volumes:
      - ./frontend:/workspace/frontend
      - /workspace/frontend/node_modules
    environment:
      - VITE_API_URL=http://localhost:3000
    working_dir: /workspace/frontend
    command: npm run dev
  
  backend:
    build: .
    ports:
      - "3000:3000"
    volumes:
      - ./backend:/workspace/backend
      - /workspace/backend/node_modules
    environment:
      - DATABASE_URL=postgres://app:app@postgres:5432/app
      - PORT=3000
    working_dir: /workspace/backend
    command: npm run dev
    depends_on:
      - postgres
  
  postgres:
    image: postgres:15-alpine
    environment:
      POSTGRES_DB: app
      POSTGRES_USER: app
      POSTGRES_PASSWORD: app
    volumes:
      - postgres_data:/var/lib/postgresql/data
    ports:
      - "5432:5432"

volumes:
  postgres_data:
```

## Step 3: Start All Services

```bash
# Enter workspace
nexus workspace ssh fullstack-app

# Start everything
$ docker-compose up -d

# Check logs
$ docker-compose logs -f
```

## Step 4: Access from Host

```bash
# On host, inspect mapped ports
nexus workspace status fullstack-app
```

Open frontend/API using the reported host ports.

Test API: curl http://localhost:3000/api/users

## Result

✅ **What you achieved:**
- Full three-tier development environment
- Hot reload on both frontend and backend
- Database with auto-migrations
- All services isolated from host
- Single command to start everything

## Development Workflow

```bash
# Frontend changes automatically reload
# Backend changes - restart service
$ docker-compose restart backend

# Database console
$ docker-compose exec postgres psql -U app -d app

# View logs
$ docker-compose logs -f frontend
$ docker-compose logs -f backend
```

## Cleanup

```bash
nexus workspace delete fullstack-app
```
