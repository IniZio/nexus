# Node + React Environment

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
- Nexus CLI installed (`nexus cli-version`)
- Git repository initialized

## Step 1: Create Environment

```bash
nexus environment create react-app
```

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

**Dockerfile:** (Already in environment root)
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
# Enter environment
nexus environment ssh react-app

# Inside environment - install dependencies
$ npm install

# Start development server
$ npm run dev
```

## Step 4: Access from Host

If your environment exposes a mapped host port for the app, check it with:

```bash
nexus environment status react-app
```

Then open the reported host URL/port.

### Hot Reload

Changes you make in your editor (on host) automatically sync to the environment and trigger HMR.

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

Then run inside environment:
```bash
$ docker-compose up -d
```

## Result

✅ **What you achieved:**
- Isolated Node.js 20 environment
- Hot reload working at the host URL/port from `nexus environment status react-app`
- Optional PostgreSQL database
- Team can replicate your exact environment
- No local Node.js installation needed

## Cleanup

```bash
nexus environment delete react-app
```

This removes both the container and git worktree.
