# Simplified Nexus & Examples Section Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Remove template support from CLI, create comprehensive examples section with 5+ complete examples, and prepare for v1.0.0 release

**Architecture:** Simplify the CLI by removing template complexity (users provide Dockerfiles instead), create curated examples demonstrating real-world use cases, structure documentation like TanStack Router examples

**Tech Stack:** Go (CLI), Markdown (docs), Docker (examples), Shell scripts (demos)

---

## Task 1: Remove Template Support from CLI

**Files:**
- Modify: `packages/nexusd/internal/cli/workspace.go:615` - Remove template flag definition
- Modify: `packages/nexusd/internal/cli/workspace.go:87-89` - Remove template handling in create command
- Modify: `packages/nexusd/internal/cli/workspace.go:16` - Remove templateFlag variable
- Update: `docs/reference/nexus-cli.md` - Remove template references

**Step 1: Remove template flag variable**

Remove line 16 in workspace.go:
```go
// REMOVE: templateFlag string
```

**Step 2: Remove template flag definition**

Remove or comment out line 615:
```go
// REMOVED: workspaceCreateCmd.Flags().StringVarP(&templateFlag, "template", "t", "", "Template (node, python, go, rust, blank)")
```

**Step 3: Remove template handling in create command**

Remove lines 87-89:
```go
// REMOVED:
// if templateFlag != "" {
//     req.Labels = map[string]string{"template": templateFlag}
// }
```

**Step 4: Update CLI documentation**

Edit `docs/reference/nexus-cli.md`:
- Remove any template references
- Add note: "Nexus uses your project's Dockerfile automatically"

**Step 5: Test CLI build**

```bash
cd packages/nexusd
go build -o nexus ./cmd/cli/main.go
./nexus workspace create --help
# Verify: --template flag should NOT appear
```

**Step 6: Commit**

```bash
git add packages/nexusd/internal/cli/workspace.go docs/reference/nexus-cli.md
git commit -m "feat: remove template support from workspace create

Users now provide Dockerfile directly instead of using templates.
This simplifies the CLI and gives users full control over their environment.

BREAKING CHANGE: --template/-t flag removed from workspace create"
```

---

## Task 2: Create Examples Directory Structure

**Files:**
- Create: `docs/examples/README.md` - Main examples index
- Create: `docs/examples/quickstart/` - 5-min getting started
- Create: `docs/examples/node-react/` - React + Node setup
- Create: `docs/examples/python-django/` - Python web app
- Create: `docs/examples/go-microservices/` - Go + Docker Compose
- Create: `docs/examples/fullstack-postgres/` - Frontend + Backend + DB
- Create: `docs/examples/remote-server/` - Run nexusd remotely

**Step 1: Create main examples README**

Create `docs/examples/README.md`:
```markdown
# Nexus Examples

Complete, production-ready examples for real-world projects.

## Quick Start (5 minutes)
- **[Quickstart](./quickstart/)** - Your first Nexus workspace

## By Language/Framework
- **[Node + React](./node-react/)** - Modern frontend development
- **[Python + Django](./python-django/)** - Python web applications
- **[Go Microservices](./go-microservices/)** - Multi-service Go architecture

## Full Stack
- **[Fullstack + PostgreSQL](./fullstack-postgres/)** - Frontend + Backend + Database

## Infrastructure
- **[Remote Server](./remote-server/)** - Run Nexus on remote infrastructure

---

## Example Structure

Each example includes:
- **README.md** - Step-by-step guide
- **Dockerfile** - Workspace configuration
- **docker-compose.yml** - Multi-service setup (if applicable)
- **demo.sh** - Automated demo script
- **Architecture diagram** - Visual overview
```

**Step 2: Create quickstart example**

Create `docs/examples/quickstart/README.md`:
```markdown
# Quickstart: Your First Nexus Workspace

**Time:** 5 minutes  
**Prerequisites:** Docker, Git

## Problem

You want to start a new project without polluting your local machine with dependencies.

## Setup

### 1. Install Nexus

```bash
# macOS
brew install nexus

# Linux
curl -fsSL https://nexus.dev/install.sh | bash

# Verify
nexus --version
```

### 2. Create Your First Workspace

```bash
# From any git repository
nexus workspace create my-first-workspace
```

This creates:
- A git worktree at `.worktrees/my-first-workspace/`
- A Docker container with your repository
- File sync between host and container

### 3. Start Developing

```bash
# Enter the workspace
nexus workspace ssh my-first-workspace

# You're now inside the container!
$ cat /etc/os-release
# Ubuntu 22.04 LTS

# Install what you need
$ apt-get update && apt-get install -y nodejs npm

# Your code is at /workspace
$ cd /workspace && ls
```

### 4. Exit and Return

```bash
# Exit SSH
$ exit

# Your workspace keeps running
nexus workspace list

# Re-enter anytime
nexus workspace ssh my-first-workspace
```

## Result

You now have:
- ✅ Isolated development environment
- ✅ Your code synced between host and container
- ✅ Ability to install any tools without affecting your host
- ✅ A clean workspace you can destroy and recreate anytime

## Next Steps

- [Node + React Example](../node-react/) - Build a modern web app
- [Fullstack + PostgreSQL](../fullstack-postgres/) - Add a database
```

Create `docs/examples/quickstart/Dockerfile`:
```dockerfile
FROM ubuntu:22.04

# Install basic tools
RUN apt-get update && apt-get install -y \
    curl \
    git \
    vim \
    nano \
    htop \
    && rm -rf /var/lib/apt/lists/*

# Set working directory
WORKDIR /workspace

# Default command
CMD ["/bin/bash"]
```

Create `docs/examples/quickstart/demo.sh`:
```bash
#!/bin/bash
set -e

echo "🚀 Nexus Quickstart Demo"
echo "========================="

# Create workspace
echo "Creating workspace..."
nexus workspace create quickstart-demo

# Show it's running
echo -e "\n📋 Workspace list:"
nexus workspace list

# SSH in and run commands
echo -e "\n🔧 Running inside workspace:"
nexus workspace exec quickstart-demo -- cat /etc/os-release

echo -e "\n✅ Demo complete!"
echo "Enter the workspace: nexus workspace ssh quickstart-demo"
```

**Step 3: Commit quickstart**

```bash
git add docs/examples/README.md docs/examples/quickstart/
git commit -m "docs: add quickstart example

5-minute getting started guide with Dockerfile and demo script"
```

---

## Task 3: Create Node + React Example

**Files:**
- Create: `docs/examples/node-react/README.md`
- Create: `docs/examples/node-react/Dockerfile`
- Create: `docs/examples/node-react/docker-compose.yml`
- Create: `docs/examples/node-react/demo.sh`

**Step 1: Create README with case study format**

Create `docs/examples/node-react/README.md`:
```markdown
# Node + React Workspace

**Time:** 15 minutes  
**Stack:** Node.js 20, React 18, Vite, PostgreSQL (optional)

## Problem

Setting up a modern React development environment with:
- Hot module replacement (HMR)
- Consistent Node.js version across team
- Optional PostgreSQL database
- No "works on my machine" issues

## Setup

### Prerequisites

- Docker Desktop installed
- Nexus CLI installed (`nexus --version`)
- Git repository initialized

## Step 1: Create Workspace

```bash
# Create workspace with Docker-in-Docker for running containers inside
nexus workspace create react-app --dind
```

The `--dind` flag enables Docker inside your workspace, so you can run `docker-compose` commands within it.

## Step 2: Configure Environment

**Option A: Copy existing project**

If you have an existing React project:
```bash
# Copy your project files
cp -r /path/to/my-react-app/* .worktrees/react-app/
```

**Option B: Start from scratch**

Create these files in `.worktrees/react-app/`:

**package.json:**
```json
{
  "name": "react-app",
  "private": true,
  "version": "0.0.0",
  "type": "module",
  "scripts": {
    "dev": "vite --host 0.0.0.0",
    "build": "tsc && vite build",
    "preview": "vite preview"
  },
  "dependencies": {
    "react": "^18.2.0",
    "react-dom": "^18.2.0"
  },
  "devDependencies": {
    "@types/react": "^18.2.43",
    "@types/react-dom": "^18.2.17",
    "@vitejs/plugin-react": "^4.2.1",
    "typescript": "^5.2.2",
    "vite": "^5.0.8"
  }
}
```

**Dockerfile:** (Already in workspace root)
```dockerfile
FROM node:20-slim

# Install git for development
RUN apt-get update && apt-get install -y git && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

# Install dependencies
COPY package*.json ./
RUN npm install

# Copy source
COPY . .

EXPOSE 5173

CMD ["npm", "run", "dev"]
```

## Step 3: Start Developing

```bash
# Enter workspace
nexus workspace ssh react-app

# Inside workspace - install dependencies
$ npm install

# Start development server
$ npm run dev
```

## Step 4: Access from Host

The dev server runs inside the workspace. To access it from your host browser:

```bash
# On your HOST machine (new terminal)
nexus workspace port add react-app 5173
```

Now open http://localhost:5173 in your browser.

### Hot Reload

Changes you make in your editor (on host) automatically sync to the workspace and trigger HMR.

## Adding PostgreSQL (Optional)

Create `docker-compose.yml` in `.worktrees/react-app/`:

```yaml
version: '3.8'
services:
  app:
    build: .
    ports:
      - "5173:5173"
    volumes:
      - .:/workspace
      - /workspace/node_modules
    environment:
      - DATABASE_URL=postgres://user:pass@db:5432/app
  
  db:
    image: postgres:15-alpine
    environment:
      POSTGRES_USER: user
      POSTGRES_PASSWORD: pass
      POSTGRES_DB: app
    volumes:
      - postgres_data:/var/lib/postgresql/data

volumes:
  postgres_data:
```

Then run inside workspace:
```bash
$ docker-compose up -d
```

## Result

✅ **What you achieved:**
- Isolated Node.js 20 environment
- Hot reload working at localhost:5173
- Optional PostgreSQL database
- Team can replicate your exact environment
- No local Node.js installation needed

## Cleanup

```bash
nexus workspace delete react-app
```

This removes both the container and git worktree.
```

**Step 2: Create supporting files**

Create `docs/examples/node-react/Dockerfile`:
```dockerfile
FROM node:20-slim

# Install git for development
RUN apt-get update && apt-get install -y git && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

# Note: package.json will be mounted from host via sync
# Dependencies installed at runtime

EXPOSE 5173

CMD ["npm", "run", "dev"]
```

Create `docs/examples/node-react/docker-compose.yml`:
```yaml
version: '3.8'
services:
  app:
    build: .
    ports:
      - "5173:5173"
    volumes:
      - .:/workspace
      - /workspace/node_modules
    environment:
      - NODE_ENV=development
  
  db:
    image: postgres:15-alpine
    environment:
      POSTGRES_USER: dev
      POSTGRES_PASSWORD: devpass
      POSTGRES_DB: appdb
    volumes:
      - postgres_data:/var/lib/postgresql/data
    ports:
      - "5432:5432"

volumes:
  postgres_data:
```

Create `docs/examples/node-react/demo.sh`:
```bash
#!/bin/bash
set -e

echo "🚀 Node + React Workspace Demo"
echo "==============================="

WORKSPACE_NAME="${1:-react-demo}"

echo "Step 1: Creating workspace with DinD..."
nexus workspace create "$WORKSPACE_NAME" --dind

echo -e "\nStep 2: Copying project files..."
# In real usage, user would have their own files
# This is just for the demo
nexus workspace exec "$WORKSPACE_NAME" -- mkdir -p /workspace/src

cat > /tmp/package.json << 'EOF'
{
  "name": "react-demo",
  "private": true,
  "version": "0.0.0",
  "type": "module",
  "scripts": {
    "dev": "vite --host 0.0.0.0"
  },
  "dependencies": {
    "react": "^18.2.0",
    "react-dom": "^18.2.0"
  },
  "devDependencies": {
    "@vitejs/plugin-react": "^4.2.1",
    "vite": "^5.0.8"
  }
}
EOF

# Copy package.json to workspace
nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/package.json' < /tmp/package.json

echo -e "\nStep 3: Installing dependencies..."
nexus workspace exec "$WORKSPACE_NAME" -- npm install

echo -e "\nStep 4: Creating sample App.jsx..."
cat > /tmp/App.jsx << 'EOF'
function App() {
  return (
    <div style={{ padding: '2rem', fontFamily: 'sans-serif' }}>
      <h1>🚀 React on Nexus</h1>
      <p>Your development workspace is ready!</p>
    </div>
  )
}
export default App
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/src/App.jsx' < /tmp/App.jsx

echo -e "\nStep 5: Creating main.jsx..."
cat > /tmp/main.jsx << 'EOF'
import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App.jsx'

ReactDOM.createRoot(document.getElementById('root')).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/src/main.jsx' < /tmp/main.jsx

echo -e "\nStep 6: Creating index.html..."
cat > /tmp/index.html << 'EOF'
<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Nexus React App</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.jsx"></script>
  </body>
</html>
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/index.html' < /tmp/index.html

echo -e "\n✅ Workspace ready!"
echo "Start dev server: nexus workspace exec $WORKSPACE_NAME -- npm run dev"
echo "Add port forward: nexus workspace port add $WORKSPACE_NAME 5173"
echo "Then open: http://localhost:5173"
```

**Step 3: Commit Node + React**

```bash
git add docs/examples/node-react/
git commit -m "docs: add node-react example

Complete React development environment with:
- Dockerfile for Node.js 20
- Optional docker-compose with PostgreSQL
- Demo script for automated setup"
```

---

## Task 4: Create Python + Django Example

**Files:**
- Create: `docs/examples/python-django/README.md`
- Create: `docs/examples/python-django/Dockerfile`
- Create: `docs/examples/python-django/docker-compose.yml`
- Create: `docs/examples/python-django/demo.sh`
- Create: `docs/examples/python-django/requirements.txt`

**Step 1: Create README**

Create `docs/examples/python-django/README.md`:
```markdown
# Python + Django Workspace

**Time:** 20 minutes  
**Stack:** Python 3.11, Django 5, PostgreSQL, Redis

## Problem

Python development environments are notoriously tricky:
- System Python conflicts with project requirements
- Virtualenvs litter your filesystem
- Different projects need different Python versions
- Database setup is repetitive

## Setup

### Prerequisites

- Docker Desktop
- Nexus CLI
- Git repository

## Step 1: Create Workspace

```bash
nexus workspace create django-app --dind
```

## Step 2: Configure Environment

Create these files in `.worktrees/django-app/`:

**Dockerfile:**
```dockerfile
FROM python:3.11-slim

# Install system dependencies
RUN apt-get update && apt-get install -y \
    gcc \
    libpq-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

# Install Python dependencies
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy project
COPY . .

EXPOSE 8000

CMD ["python", "manage.py", "runserver", "0.0.0.0:8000"]
```

**requirements.txt:**
```
django>=5.0,<5.1
psycopg2-binary
redis
celery
django-debug-toolbar
pytest-django
```

**docker-compose.yml:**
```yaml
version: '3.8'
services:
  web:
    build: .
    command: python manage.py runserver 0.0.0.0:8000
    volumes:
      - .:/workspace
    ports:
      - "8000:8000"
    environment:
      - DEBUG=1
      - DATABASE_URL=postgres://django:django@db:5432/django
      - REDIS_URL=redis://redis:6379/0
    depends_on:
      - db
      - redis
  
  db:
    image: postgres:15-alpine
    environment:
      POSTGRES_DB: django
      POSTGRES_USER: django
      POSTGRES_PASSWORD: django
    volumes:
      - postgres_data:/var/lib/postgresql/data
  
  redis:
    image: redis:7-alpine
    volumes:
      - redis_data:/data
  
  celery:
    build: .
    command: celery -A myproject worker -l info
    volumes:
      - .:/workspace
    environment:
      - DATABASE_URL=postgres://django:django@db:5432/django
      - REDIS_URL=redis://redis:6379/0
    depends_on:
      - db
      - redis

volumes:
  postgres_data:
  redis_data:
```

## Step 3: Initialize Django Project

```bash
# Enter workspace
nexus workspace ssh django-app

# Create Django project
$ django-admin startproject myproject .

# Verify it works
$ python manage.py runserver 0.0.0.0:8000
```

## Step 4: Access from Host

```bash
# On host
nexus workspace port add django-app 8000
```

Open http://localhost:8000

## Result

✅ **What you achieved:**
- Isolated Python 3.11 environment
- Django development server with auto-reload
- PostgreSQL database in separate container
- Redis for caching/task queue
- Celery worker ready for background tasks
- No Python version conflicts on your host

## Development Workflow

```bash
# Start all services
$ docker-compose up -d

# Run migrations
$ python manage.py migrate

# Create superuser
$ python manage.py createsuperuser

# Run tests
$ pytest

# Check logs
$ docker-compose logs -f
```

## Cleanup

```bash
nexus workspace delete django-app
```
```

**Step 2: Create supporting files**

Create `docs/examples/python-django/Dockerfile`:
```dockerfile
FROM python:3.11-slim

# Install system dependencies
RUN apt-get update && apt-get install -y \
    gcc \
    libpq-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

EXPOSE 8000

CMD ["python", "manage.py", "runserver", "0.0.0.0:8000"]
```

Create `docs/examples/python-django/requirements.txt`:
```
django>=5.0,<5.1
psycopg2-binary
redis
celery
django-debug-toolbar
pytest-django
black
flake8
```

Create `docs/examples/python-django/docker-compose.yml`:
```yaml
version: '3.8'
services:
  web:
    build: .
    command: python manage.py runserver 0.0.0.0:8000
    volumes:
      - .:/workspace
    ports:
      - "8000:8000"
    environment:
      - DEBUG=1
      - DATABASE_URL=postgres://django:django@db:5432/django
      - REDIS_URL=redis://redis:6379/0
    depends_on:
      - db
      - redis
  
  db:
    image: postgres:15-alpine
    environment:
      POSTGRES_DB: django
      POSTGRES_USER: django
      POSTGRES_PASSWORD: django
    volumes:
      - postgres_data:/var/lib/postgresql/data
    ports:
      - "5432:5432"
  
  redis:
    image: redis:7-alpine
    volumes:
      - redis_data:/data

volumes:
  postgres_data:
  redis_data:
```

Create `docs/examples/python-django/demo.sh`:
```bash
#!/bin/bash
set -e

echo "🐍 Python + Django Workspace Demo"
echo "=================================="

WORKSPACE_NAME="${1:-django-demo}"

echo "Step 1: Creating workspace with DinD..."
nexus workspace create "$WORKSPACE_NAME" --dind

echo -e "\nStep 2: Copying configuration files..."

# Copy requirements.txt
nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/requirements.txt' < requirements.txt

# Copy Dockerfile
nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/Dockerfile' < Dockerfile

# Copy docker-compose.yml
nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/docker-compose.yml' < docker-compose.yml

echo -e "\nStep 3: Installing dependencies..."
nexus workspace exec "$WORKSPACE_NAME" -- pip install -r /workspace/requirements.txt

echo -e "\nStep 4: Creating Django project..."
nexus workspace exec "$WORKSPACE_NAME" -- django-admin startproject myproject /workspace

echo -e "\nStep 5: Running initial migration..."
nexus workspace exec "$WORKSPACE_NAME" -- python /workspace/manage.py migrate

echo -e "\n✅ Django workspace ready!"
echo "Start server: nexus workspace exec $WORKSPACE_NAME -- python manage.py runserver 0.0.0.0:8000"
echo "Add port: nexus workspace port add $WORKSPACE_NAME 8000"
echo "URL: http://localhost:8000"
echo ""
echo "Or start all services:"
echo "nexus workspace ssh $WORKSPACE_NAME"
echo "docker-compose up -d"
```

**Step 3: Commit Python + Django**

```bash
git add docs/examples/python-django/
git commit -m "docs: add python-django example

Complete Django development environment with:
- Python 3.11
- PostgreSQL and Redis
- Docker Compose orchestration
- Demo script"
```

---

## Task 5: Create Go Microservices Example

**Files:**
- Create: `docs/examples/go-microservices/README.md`
- Create: `docs/examples/go-microservices/Dockerfile`
- Create: `docs/examples/go-microservices/docker-compose.yml`
- Create: `docs/examples/go-microservices/demo.sh`
- Create: `docs/examples/go-microservices/go.mod`

**Step 1: Create README**

Create `docs/examples/go-microservices/README.md`:
```markdown
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
    │   ├── main.go
    │   └── Dockerfile
    ├── auth-service/
    │   ├── main.go
    │   └── Dockerfile
    └── user-service/
        ├── main.go
        └── Dockerfile
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
      dockerfile: ./services/api-gateway/Dockerfile
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
      dockerfile: ./services/auth-service/Dockerfile
    environment:
      - DATABASE_URL=postgres://auth:auth@postgres:5432/auth?sslmode=disable
      - JWT_SECRET=your-secret-key
    depends_on:
      - postgres
  
  user-service:
    build:
      context: .
      dockerfile: ./services/user-service/Dockerfile
    environment:
      - DATABASE_URL=postgres://user:user@postgres:5432/user?sslmode=disable
    depends_on:
      - postgres
  
  postgres:
    image: postgres:15-alpine
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: postgres
      POSTGRES_MULTIPLE_DATABASES: auth,user
    volumes:
      - ./init-scripts:/docker-entrypoint-initdb.d
      - postgres_data:/var/lib/postgresql/data
    ports:
      - "5432:5432"

volumes:
  postgres_data:
```

**services/api-gateway/main.go:**
```go
package main

import (
    "net/http"
    "net/http/httputil"
    "net/url"
    "os"
    
    "github.com/go-chi/chi/v5"
    "github.com/go-chi/chi/v5/middleware"
)

func main() {
    r := chi.NewRouter()
    r.Use(middleware.Logger)
    r.Use(middleware.Recoverer)
    
    // Health check
    r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
        w.Write([]byte("OK"))
    })
    
    // Proxy to auth service
    authURL, _ := url.Parse(os.Getenv("AUTH_SERVICE_URL"))
    r.Mount("/auth", httputil.NewSingleHostReverseProxy(authURL))
    
    // Proxy to user service
    userURL, _ := url.Parse(os.Getenv("USER_SERVICE_URL"))
    r.Mount("/users", httputil.NewSingleHostReverseProxy(userURL))
    
    http.ListenAndServe(":8080", r)
}
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
```

**Step 2: Create supporting files**

Create `docs/examples/go-microservices/Dockerfile`:
```dockerfile
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
```

Create `docs/examples/go-microservices/go.mod`:
```go
module github.com/example/go-microservices

go 1.21

require (
    github.com/go-chi/chi/v5 v5.0.10
    github.com/golang-jwt/jwt/v5 v5.2.0
    github.com/lib/pq v1.10.9
)
```

Create `docs/examples/go-microservices/docker-compose.yml`:
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
      - JWT_SECRET=dev-secret-key
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

Create `docs/examples/go-microservices/demo.sh`:
```bash
#!/bin/bash
set -e

echo "🚀 Go Microservices Workspace Demo"
echo "==================================="

WORKSPACE_NAME="${1:-go-microservices}"

echo "Step 1: Creating workspace with DinD..."
nexus workspace create "$WORKSPACE_NAME" --dind

echo -e "\nStep 2: Setting up Go workspace..."

# Copy go.mod
nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/go.mod' < go.mod

# Copy main Dockerfile
nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/Dockerfile' < Dockerfile

# Copy docker-compose
nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/docker-compose.yml' < docker-compose.yml

echo -e "\nStep 3: Creating service directories..."
nexus workspace exec "$WORKSPACE_NAME" -- mkdir -p /workspace/services/api-gateway
nexus workspace exec "$WORKSPACE_NAME" -- mkdir -p /workspace/services/auth-service
nexus workspace exec "$WORKSPACE_NAME" -- mkdir -p /workspace/services/user-service

echo -e "\nStep 4: Creating API Gateway..."
cat > /tmp/gateway.go << 'EOF'
package main

import (
    "fmt"
    "net/http"
    "os"
)

func main() {
    http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
        fmt.Fprintf(w, "API Gateway OK")
    })
    
    http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        fmt.Fprintf(w, "Go Microservices API Gateway\n")
    })
    
    port := os.Getenv("PORT")
    if port == "" {
        port = "8080"
    }
    
    fmt.Printf("API Gateway starting on port %s\n", port)
    http.ListenAndServe(":"+port, nil)
}
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/services/api-gateway/main.go' < /tmp/gateway.go

echo -e "\nStep 5: Creating Auth Service..."
cat > /tmp/auth.go << 'EOF'
package main

import (
    "fmt"
    "net/http"
    "os"
)

func main() {
    http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
        fmt.Fprintf(w, "Auth Service OK")
    })
    
    http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
        fmt.Fprintf(w, "Login endpoint\n")
    })
    
    port := os.Getenv("PORT")
    if port == "" {
        port = "8081"
    }
    
    fmt.Printf("Auth Service starting on port %s\n", port)
    http.ListenAndServe(":"+port, nil)
}
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/services/auth-service/main.go' < /tmp/auth.go

echo -e "\nStep 6: Creating User Service..."
cat > /tmp/user.go << 'EOF'
package main

import (
    "fmt"
    "net/http"
    "os"
)

func main() {
    http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
        fmt.Fprintf(w, "User Service OK")
    })
    
    http.HandleFunc("/users", func(w http.ResponseWriter, r *http.Request) {
        fmt.Fprintf(w, "Users endpoint\n")
    })
    
    port := os.Getenv("PORT")
    if port == "" {
        port = "8082"
    }
    
    fmt.Printf("User Service starting on port %s\n", port)
    http.ListenAndServe(":"+port, nil)
}
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/services/user-service/main.go' < /tmp/user.go

echo -e "\n✅ Go microservices workspace ready!"
echo "Start services: nexus workspace ssh $WORKSPACE_NAME && docker-compose up -d"
echo "Add port: nexus workspace port add $WORKSPACE_NAME 8080"
echo "Test: curl http://localhost:8080/health"
```

**Step 3: Commit Go Microservices**

```bash
git add docs/examples/go-microservices/
git commit -m "docs: add go-microservices example

Microservices architecture with:
- Go 1.21
- API Gateway pattern
- Multi-service Docker Compose
- PostgreSQL per service pattern"
```

---

## Task 6: Create Fullstack + PostgreSQL Example

**Files:**
- Create: `docs/examples/fullstack-postgres/README.md`
- Create: `docs/examples/fullstack-postgres/Dockerfile`
- Create: `docs/examples/fullstack-postgres/docker-compose.yml`
- Create: `docs/examples/fullstack-postgres/demo.sh`

**Step 1: Create README**

Create `docs/examples/fullstack-postgres/README.md`:
```markdown
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
nexus workspace create fullstack-app --dind
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
    build:
      context: ./frontend
      dockerfile: ../Dockerfile
    ports:
      - "5173:5173"
    volumes:
      - ./frontend:/workspace
      - /workspace/node_modules
    environment:
      - VITE_API_URL=http://localhost:3000
    command: npm run dev
  
  backend:
    build:
      context: ./backend
      dockerfile: ../Dockerfile
    ports:
      - "3000:3000"
    volumes:
      - ./backend:/workspace
      - /workspace/node_modules
    environment:
      - DATABASE_URL=postgres://app:app@postgres:5432/app
      - PORT=3000
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
      - ./backend/migrations:/docker-entrypoint-initdb.d
    ports:
      - "5432:5432"

volumes:
  postgres_data:
```

**backend/src/server.js:**
```javascript
const express = require('express');
const { Pool } = require('pg');
const cors = require('cors');

const app = express();
app.use(cors());
app.use(express.json());

const pool = new Pool({
  connectionString: process.env.DATABASE_URL
});

// Health check
app.get('/health', (req, res) => {
  res.json({ status: 'OK', timestamp: new Date() });
});

// Get users
app.get('/api/users', async (req, res) => {
  try {
    const result = await pool.query('SELECT * FROM users');
    res.json(result.rows);
  } catch (err) {
    res.status(500).json({ error: err.message });
  }
});

// Create user
app.post('/api/users', async (req, res) => {
  try {
    const { name, email } = req.body;
    const result = await pool.query(
      'INSERT INTO users (name, email) VALUES ($1, $2) RETURNING *',
      [name, email]
    );
    res.json(result.rows[0]);
  } catch (err) {
    res.status(500).json({ error: err.message });
  }
});

const PORT = process.env.PORT || 3000;
app.listen(PORT, '0.0.0.0', () => {
  console.log(`API server running on port ${PORT}`);
});
```

**backend/migrations/001_init.sql:**
```sql
CREATE TABLE IF NOT EXISTS users (
  id SERIAL PRIMARY KEY,
  name VARCHAR(100) NOT NULL,
  email VARCHAR(100) UNIQUE NOT NULL,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Seed data
INSERT INTO users (name, email) VALUES 
  ('Alice', 'alice@example.com'),
  ('Bob', 'bob@example.com');
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
# On host
nexus workspace port add fullstack-app 5173  # Frontend
nexus workspace port add fullstack-app 3000  # API
nexus workspace port add fullstack-app 5432  # DB (optional)
```

Open http://localhost:5173 for frontend

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
```

**Step 2: Create supporting files**

Create `docs/examples/fullstack-postgres/Dockerfile`:
```dockerfile
FROM node:20-slim

# Install git
RUN apt-get update && apt-get install -y git && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

EXPOSE 3000 5173

CMD ["npm", "run", "dev"]
```

Create `docs/examples/fullstack-postgres/docker-compose.yml`:
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

Create `docs/examples/fullstack-postgres/demo.sh`:
```bash
#!/bin/bash
set -e

echo "🚀 Fullstack + PostgreSQL Workspace Demo"
echo "========================================="

WORKSPACE_NAME="${1:-fullstack-demo}"

echo "Step 1: Creating workspace with DinD..."
nexus workspace create "$WORKSPACE_NAME" --dind

echo -e "\nStep 2: Setting up project structure..."

# Copy Dockerfile
nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/Dockerfile' < Dockerfile

# Copy docker-compose
nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/docker-compose.yml' < docker-compose.yml

# Create directories
nexus workspace exec "$WORKSPACE_NAME" -- mkdir -p /workspace/frontend/src
nexus workspace exec "$WORKSPACE_NAME" -- mkdir -p /workspace/backend/src /workspace/backend/migrations

echo -e "\nStep 3: Creating frontend..."

# Frontend package.json
cat > /tmp/frontend-package.json << 'EOF'
{
  "name": "frontend",
  "private": true,
  "version": "0.0.0",
  "type": "module",
  "scripts": {
    "dev": "vite --host 0.0.0.0 --port 5173"
  },
  "dependencies": {
    "react": "^18.2.0",
    "react-dom": "^18.2.0"
  },
  "devDependencies": {
    "@vitejs/plugin-react": "^4.2.1",
    "vite": "^5.0.8"
  }
}
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/frontend/package.json' < /tmp/frontend-package.json

# Frontend App.jsx
cat > /tmp/App.jsx << 'EOF'
import { useState, useEffect } from 'react'

function App() {
  const [users, setUsers] = useState([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    fetch(import.meta.env.VITE_API_URL + '/api/users')
      .then(r => r.json())
      .then(data => {
        setUsers(data)
        setLoading(false)
      })
  }, [])

  if (loading) return <div>Loading...</div>

  return (
    <div style={{ padding: '2rem', fontFamily: 'sans-serif' }}>
      <h1>🚀 Fullstack Nexus App</h1>
      <h2>Users from Database:</h2>
      <ul>
        {users.map(user => (
          <li key={user.id}>{user.name} ({user.email})</li>
        ))}
      </ul>
    </div>
  )
}

export default App
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/frontend/src/App.jsx' < /tmp/App.jsx

# Frontend main.jsx
cat > /tmp/main.jsx << 'EOF'
import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App.jsx'

ReactDOM.createRoot(document.getElementById('root')).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/frontend/src/main.jsx' < /tmp/main.jsx

# Frontend index.html
cat > /tmp/index.html << 'EOF'
<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Nexus Fullstack</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.jsx"></script>
  </body>
</html>
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/frontend/index.html' < /tmp/index.html

echo -e "\nStep 4: Creating backend..."

# Backend package.json
cat > /tmp/backend-package.json << 'EOF'
{
  "name": "backend",
  "version": "1.0.0",
  "scripts": {
    "dev": "node src/server.js"
  },
  "dependencies": {
    "express": "^4.18.2",
    "cors": "^2.8.5",
    "pg": "^8.11.3"
  }
}
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/backend/package.json' < /tmp/backend-package.json

# Backend server.js
cat > /tmp/server.js << 'EOF'
const express = require('express')
const cors = require('cors')

const app = express()
app.use(cors())
app.use(express.json())

// Health check
app.get('/health', (req, res) => {
  res.json({ status: 'OK', timestamp: new Date() })
})

// Get users (mock - would connect to real DB)
app.get('/api/users', (req, res) => {
  res.json([
    { id: 1, name: 'Alice', email: 'alice@example.com' },
    { id: 2, name: 'Bob', email: 'bob@example.com' }
  ])
})

const PORT = process.env.PORT || 3000
app.listen(PORT, '0.0.0.0', () => {
  console.log(`API server on port ${PORT}`)
})
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/backend/src/server.js' < /tmp/server.js

# Database init
cat > /tmp/001_init.sql << 'EOF'
CREATE TABLE IF NOT EXISTS users (
  id SERIAL PRIMARY KEY,
  name VARCHAR(100) NOT NULL,
  email VARCHAR(100) UNIQUE NOT NULL,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO users (name, email) VALUES 
  ('Alice', 'alice@example.com'),
  ('Bob', 'bob@example.com')
ON CONFLICT DO NOTHING;
EOF

nexus workspace exec "$WORKSPACE_NAME" -- sh -c 'cat > /workspace/backend/migrations/001_init.sql' < /tmp/001_init.sql

echo -e "\n✅ Fullstack workspace ready!"
echo "Start services: nexus workspace ssh $WORKSPACE_NAME && docker-compose up -d"
echo "Add ports:"
echo "  nexus workspace port add $WORKSPACE_NAME 5173  # Frontend"
echo "  nexus workspace port add $WORKSPACE_NAME 3000  # API"
echo "  nexus workspace port add $WORKSPACE_NAME 5432  # Database"
echo ""
echo "Open: http://localhost:5173"
```

**Step 3: Commit Fullstack**

```bash
git add docs/examples/fullstack-postgres/
git commit -m "docs: add fullstack-postgres example

Three-tier architecture with:
- React frontend
- Node.js backend
- PostgreSQL database
- Docker Compose orchestration"
```

---

## Task 7: Create Remote Server Example

**Files:**
- Create: `docs/examples/remote-server/README.md`
- Create: `docs/examples/remote-server/nexus-server.service`
- Create: `docs/examples/remote-server/install.sh`
- Create: `docs/examples/remote-server/demo.sh`

**Step 1: Create README**

Create `docs/examples/remote-server/README.md`:
```markdown
# Remote Server Workspace

**Time:** 30 minutes  
**Use Case:** Run Nexus on cloud/remote infrastructure

## Problem

Your local machine lacks:
- Sufficient RAM/CPU for large projects
- Persistent 24/7 availability
- Fast network for large downloads
- Ability to work from multiple devices

## Solution

Run `nexusd` (Nexus daemon) on a remote server (AWS, GCP, VPS, homelab) and connect from anywhere.

## Architecture

```
┌──────────────┐         ┌──────────────────┐
│   Laptop     │  SSH    │   Remote Server  │
│  (nexus CLI) │◄───────►│   (nexusd)       │
└──────────────┘         └────────┬─────────┘
                                  │
                    ┌─────────────┼─────────────┐
                    ▼             ▼             ▼
              ┌─────────┐  ┌─────────┐  ┌─────────┐
              │Docker   │  │Git      │  │File     │
              │Container│  │Worktree │  │Sync     │
              └─────────┘  └─────────┘  └─────────┘
```

## Setup

### Step 1: Provision Remote Server

Requirements:
- Ubuntu 22.04 LTS (recommended)
- 4+ CPU cores
- 8GB+ RAM
- 50GB+ disk
- Docker installed

Example: AWS EC2 t3.large, DigitalOcean droplet, or Hetzner CX31

### Step 2: Install Nexus on Server

SSH into your remote server:

```bash
# SSH to remote server
ssh user@your-server-ip

# Install Nexus
curl -fsSL https://nexus.dev/install.sh | bash

# Verify installation
nexus --version
```

### Step 3: Configure Nexus Daemon

Create systemd service for persistent operation:

```bash
# Create service file
sudo tee /etc/systemd/system/nexusd.service << 'EOF'
[Unit]
Description=Nexus Daemon
After=docker.service
Requires=docker.service

[Service]
Type=simple
ExecStart=/usr/local/bin/nexusd
Restart=always
RestartSec=5
User=ubuntu
Environment="HOME=/home/ubuntu"

[Install]
WantedBy=multi-user.target
EOF

# Reload and start
sudo systemctl daemon-reload
sudo systemctl enable nexusd
sudo systemctl start nexusd

# Check status
sudo systemctl status nexusd
```

### Step 4: Configure Local CLI

On your local machine:

```bash
# Point Nexus to remote server
export NEXUS_HOST=your-server-ip
export NEXUS_PORT=8080

# Or use config file
mkdir -p ~/.config/nexus
cat > ~/.config/nexus/config.yaml << EOF
server:
  host: your-server-ip
  port: 8080
EOF

# Test connection
nexus status
```

### Step 5: Create Remote Workspace

```bash
# Create workspace on remote server
nexus workspace create remote-dev

# This creates the workspace on the REMOTE server
# Files are synced back to your local machine
```

## SSH Key Authentication

For passwordless access:

```bash
# Copy your SSH key to remote server
ssh-copy-id user@your-server-ip

# Configure Nexus to use key
nexus workspace inject-key remote-dev
```

## Development Workflow

```bash
# 1. Connect to remote workspace
nexus workspace ssh remote-dev

# 2. Work normally - all compute happens remotely
$ npm install
$ npm run build
$ docker-compose up -d

# 3. Exit - workspace keeps running
$ exit

# 4. Check status from anywhere
nexus workspace status remote-dev
```

## Port Forwarding

Access services running on remote workspace locally:

```bash
# Forward remote port 3000 to local port 3000
nexus workspace port add remote-dev 3000

# Now access http://localhost:3000 on your laptop
# Requests go to remote workspace!
```

## Multi-Device Workflow

```
Laptop at office:
  nexus workspace ssh remote-dev
  # do some work
  exit

Laptop at home:
  nexus workspace ssh remote-dev
  # continue where you left off
  # all state preserved on remote
```

## Security Considerations

1. **Firewall**: Only expose Nexus port to your IP
   ```bash
   sudo ufw allow from YOUR_IP to any port 8080
   ```

2. **Authentication**: Use API tokens
   ```bash
   nexus auth login
   ```

3. **SSH**: Always use key-based auth

4. **Updates**: Keep Nexus updated
   ```bash
   nexus update
   ```

## Result

✅ **What you achieved:**
- Development environment accessible from anywhere
- Consistent performance regardless of local machine
- Work persists across device switches
- Can use lightweight laptop for heavy compute tasks
- 24/7 available development environment

## Troubleshooting

### Connection refused
```bash
# Check if daemon is running on server
ssh user@server "sudo systemctl status nexusd"

# Check firewall
ssh user@server "sudo ufw status"
```

### Slow sync
```bash
# Check sync status
nexus sync status remote-dev

# Pause sync for large operations
nexus sync pause remote-dev
# ... do work ...
nexus sync resume remote-dev
```

## Cleanup

```bash
# Delete remote workspace
nexus workspace delete remote-dev

# Stop daemon on server
ssh user@server "sudo systemctl stop nexusd"
```
```

**Step 2: Create supporting files**

Create `docs/examples/remote-server/nexus-server.service`:
```ini
[Unit]
Description=Nexus Daemon
After=docker.service
Requires=docker.service

[Service]
Type=simple
ExecStart=/usr/local/bin/nexusd
Restart=always
RestartSec=5
User=%USER%
Environment="HOME=%HOME%"

[Install]
WantedBy=multi-user.target
```

Create `docs/examples/remote-server/install.sh`:
```bash
#!/bin/bash
set -e

echo "🚀 Nexus Remote Server Setup"
echo "=============================="

# Check if running as root for systemd setup
if [ "$EUID" -ne 0 ]; then 
  echo "Please run with sudo for systemd setup"
  exit 1
fi

# Detect user
REAL_USER=${SUDO_USER:-$USER}
REAL_HOME=$(eval echo ~$REAL_USER)

echo "Setting up Nexus for user: $REAL_USER"

# Install Nexus if not present
if ! command -v nexus &> /dev/null; then
    echo "Installing Nexus..."
    curl -fsSL https://nexus.dev/install.sh | bash
fi

# Create systemd service
echo "Creating systemd service..."
cat > /etc/systemd/system/nexusd.service << EOF
[Unit]
Description=Nexus Daemon
After=docker.service
Requires=docker.service

[Service]
Type=simple
ExecStart=/usr/local/bin/nexusd
Restart=always
RestartSec=5
User=$REAL_USER
Environment="HOME=$REAL_HOME"

[Install]
WantedBy=multi-user.target
EOF

# Enable and start
echo "Enabling and starting Nexus daemon..."
systemctl daemon-reload
systemctl enable nexusd
systemctl start nexusd

# Check status
echo -e "\n✅ Nexus daemon status:"
systemctl status nexusd --no-pager

echo -e "\n📝 Next steps:"
echo "1. Configure firewall: ufw allow 8080"
echo "2. From local machine: export NEXUS_HOST=$(curl -s ifconfig.me)"
echo "3. Test: nexus status"
```

Create `docs/examples/remote-server/demo.sh`:
```bash
#!/bin/bash
set -e

echo "🌐 Remote Server Workspace Demo"
echo "================================"

echo "This demo shows how to set up Nexus on a remote server."
echo ""
echo "Prerequisites:"
echo "  - Remote server (Ubuntu 22.04 recommended)"
echo "  - SSH access to the server"
echo "  - Docker installed on server"
echo ""
echo "Step 1: SSH to remote server"
echo "  ssh user@your-server-ip"
echo ""
echo "Step 2: Run install script on server"
echo "  curl -fsSL https://nexus.dev/install.sh | bash"
echo ""
echo "Step 3: Start nexusd"
echo "  nexus daemon start"
echo "  # Or use systemd for persistence"
echo ""
echo "Step 4: Configure local CLI"
echo "  export NEXUS_HOST=your-server-ip"
echo "  export NEXUS_PORT=8080"
echo ""
echo "Step 5: Create workspace on remote"
echo "  nexus workspace create remote-dev"
echo ""
echo "Step 6: Connect to remote workspace"
echo "  nexus workspace ssh remote-dev"
echo ""
echo "✅ You're now developing on a remote server!"
echo ""
echo "Useful commands:"
echo "  nexus workspace list          # List remote workspaces"
echo "  nexus workspace port add ...  # Forward ports locally"
echo "  nexus sync status ...         # Check file sync"
```

**Step 3: Commit Remote Server**

```bash
git add docs/examples/remote-server/
git commit -m "docs: add remote-server example

Complete guide for running Nexus on remote infrastructure:
- systemd service configuration
- Installation script
- Multi-device workflow
- Security best practices"
```

---

## Task 8: Update Documentation Navigation

**Files:**
- Modify: `docs/index.md` - Add examples section link
- Modify: `mkdocs.yml` - Add examples to navigation

**Step 1: Update main docs index**

Add to `docs/index.md`:
```markdown
## Quick Start

New to Nexus? Start here:

1. **[Installation](tutorials/installation.md)** - Get Nexus running
2. **[Quickstart Example](examples/quickstart/)** - 5-minute introduction
3. **[Workspace Guide](tutorials/workspace-quickstart.md)** - Full tutorial

## Examples by Stack

- **[Node + React](examples/node-react/)** - Modern frontend development
- **[Python + Django](examples/python-django/)** - Python web applications
- **[Go Microservices](examples/go-microservices/)** - Multi-service architecture
- **[Fullstack + PostgreSQL](examples/fullstack-postgres/)** - Three-tier application
- **[Remote Server](examples/remote-server/)** - Cloud development environments
```

**Step 2: Update MkDocs navigation**

Edit `mkdocs.yml`, add examples section:
```yaml
nav:
  - Home: index.md
  - Tutorials:
    - Installation: tutorials/installation.md
    - Workspace Quickstart: tutorials/workspace-quickstart.md
  - Examples:
    - Overview: examples/README.md
    - Quickstart: examples/quickstart/README.md
    - Node + React: examples/node-react/README.md
    - Python + Django: examples/python-django/README.md
    - Go Microservices: examples/go-microservices/README.md
    - Fullstack + Postgres: examples/fullstack-postgres/README.md
    - Remote Server: examples/remote-server/README.md
  - Reference:
    - Nexus CLI: reference/nexus-cli.md
    - Enforcer Config: reference/enforcer-config.md
  - Explanation:
    - Architecture: explanation/architecture.md
    - Boulder System: explanation/boulder-system.md
```

**Step 3: Commit documentation updates**

```bash
git add docs/index.md mkdocs.yml
git commit -m "docs: update navigation with examples section

Add examples to main index and MkDocs navigation"
```

---

## Task 9: Create Release Preparation

**Files:**
- Create: `docs/dev/RELEASE.md` - Release checklist
- Modify: `package.json` - Version bump
- Create: `.github/RELEASE_TEMPLATE.md` - GitHub release template

**Step 1: Create release checklist**

Create `docs/dev/RELEASE.md`:
```markdown
# Nexus v1.0.0 Release Checklist

## Pre-Release

- [ ] All tests passing
- [ ] Documentation complete
- [ ] Examples tested
- [ ] CHANGELOG.md updated
- [ ] Version bumped in all packages

## Version Bump

### Nexus CLI (Go)
```bash
cd packages/nexusd
# Update version in version.go or main.go
git commit -m "chore: bump version to v1.0.0"
```

### Package Managers

**Homebrew (macOS/Linux):**
```bash
# Create formula PR to homebrew-core
# Formula: nexus.rb
```

**npm (optional):**
```bash
cd packages/opencode
npm version 1.0.0
npm publish
```

## GitHub Release

1. Create release notes from CHANGELOG
2. Tag: `git tag -a v1.0.0 -m "Nexus v1.0.0"`
3. Push: `git push origin v1.0.0`
4. Create GitHub release with binaries

## Binaries

Build for all platforms:
```bash
# macOS AMD64
GOOS=darwin GOARCH=amd64 go build -o nexus-darwin-amd64

# macOS ARM64
GOOS=darwin GOARCH=arm64 go build -o nexus-darwin-arm64

# Linux AMD64
GOOS=linux GOARCH=amd64 go build -o nexus-linux-amd64

# Linux ARM64
GOOS=linux GOARCH=arm64 go build -o nexus-linux-arm64
```

## Post-Release

- [ ] Update documentation site
- [ ] Announce on social media
- [ ] Update install.sh with new version
- [ ] Monitor for issues
```

**Step 2: Create GitHub release template**

Create `.github/RELEASE_TEMPLATE.md`:
```markdown
## Nexus v{VERSION}

### What's New

-{CHANGELOG_EXCERPT}

### Installation

**macOS/Linux (Homebrew):**
\`\`\`bash
brew install nexus
\`\`\`

**Linux (curl):**
\`\`\`bash
curl -fsSL https://nexus.dev/install.sh | bash
\`\`\`

**Manual:**
Download binary for your platform from Assets below.

### Getting Started

\`\`\`bash
# Create your first workspace
nexus workspace create my-project

# Enter it
nexus workspace ssh my-project

# Start developing!
\`\`\`

### Resources

- [Documentation](https://nexus.dev/docs)
- [Examples](https://nexus.dev/docs/examples)
- [Changelog](https://github.com/nexus/nexus/blob/main/CHANGELOG.md)

### Contributors

Thanks to all contributors!
```

**Step 3: Update CHANGELOG**

Add to `CHANGELOG.md`:
```markdown
## [1.0.0] - 2024-XX-XX

### Breaking Changes
- Removed `--template` flag from `nexus workspace create`. Users now provide Dockerfile directly.

### Added
- Comprehensive examples section with 6 complete examples
- Quickstart guide (5 minutes to first workspace)
- Node + React example with HMR
- Python + Django example with PostgreSQL
- Go Microservices example
- Fullstack + PostgreSQL three-tier example
- Remote Server deployment guide

### Changed
- Simplified CLI by removing template system
- Documentation restructured with examples focus
```

**Step 4: Commit release prep**

```bash
git add docs/dev/RELEASE.md .github/RELEASE_TEMPLATE.md CHANGELOG.md
git commit -m "chore: prepare for v1.0.0 release

- Release checklist
- GitHub release template
- Changelog updated"
```

---

## Task 10: Final Review and Integration

**Step 1: Verify all files created**

```bash
# Check examples directory structure
ls -la docs/examples/

# Should show:
# - README.md
# - quickstart/
# - node-react/
# - python-django/
# - go-microservices/
# - fullstack-postgres/
# - remote-server/

# Verify each example has required files
for dir in docs/examples/*/; do
  echo "Checking $dir..."
  test -f "$dir/README.md" && echo "  ✓ README.md" || echo "  ✗ README.md missing"
  test -f "$dir/Dockerfile" && echo "  ✓ Dockerfile" || echo "  ⚠ Dockerfile (may be optional)"
  test -f "$dir/demo.sh" && echo "  ✓ demo.sh" || echo "  ✗ demo.sh missing"
done
```

**Step 2: Test CLI build**

```bash
cd packages/nexusd
go build -o nexus ./cmd/cli/main.go
./nexus workspace create --help
# Confirm: No --template flag
```

**Step 3: Final commit**

```bash
git add -A
git commit -m "feat: simplified Nexus with comprehensive examples

Major changes:
- Removed template support from CLI (users provide Dockerfile)
- Created 6 complete examples:
  * Quickstart (5-min getting started)
  * Node + React (modern frontend)
  * Python + Django (Python web apps)
  * Go Microservices (multi-service)
  * Fullstack + PostgreSQL (three-tier)
  * Remote Server (cloud deployment)
- Each example includes README, Dockerfile, demo.sh
- Updated documentation navigation
- Prepared for v1.0.0 release

BREAKING CHANGE: --template/-t flag removed"
```

---

## Summary

This implementation:
1. ✅ Removes template complexity from CLI
2. ✅ Creates 6 complete, tested examples
3. ✅ Follows case study format (Problem → Setup → Steps → Result)
4. ✅ Includes automated demo scripts
5. ✅ Updates all documentation
6. ✅ Prepares for v1.0.0 release

**Estimated effort:** 2-3 hours
**Breaking change:** Yes (removes --template flag)
**User impact:** Positive - simpler, more flexible workflow
