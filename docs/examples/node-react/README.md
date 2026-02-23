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

# Note: package.json will be mounted from host via sync
# Dependencies installed at runtime

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
