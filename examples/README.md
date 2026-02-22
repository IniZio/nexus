# Nexus Examples

Test drive Nexus with these example projects showcasing various workspace capabilities.

## Available Examples

### Basic Examples

#### 1. Blank Node Project
A minimal Express.js server for basic Nexus testing.
- **Path:** `examples/blank-node-project/`
- **Focus:** Basic file analysis, hot reload detection, endpoint discovery
- **Quick Start:**
  ```bash
  cd examples/blank-node-project
  npm install
  npm start
  ```

#### 2. React Hot Reload
A Create React App for testing frontend hot reload capabilities.
- **Path:** `examples/react-hot-reload/`
- **Focus:** React component changes, CSS hot reload, state preservation
- **Quick Start:**
  ```bash
  cd examples/react-hot-reload
  npm install
  npm start
  ```

#### 3. Complex Backend
Express + PostgreSQL backend with full API layer.
- **Path:** `examples/complex-backend/`
- **Focus:** Database schema analysis, authentication/authorization, complex queries
- **Quick Start:**
  ```bash
  cd examples/complex-backend
  npm install
  cp .env.example .env
  npm run migrate
  npm start
  ```

#### 4. Large File Operations
Generate and benchmark large files for performance testing.
- **Path:** `examples/large-file-operations/`
- **Focus:** Performance testing, memory usage, change detection at scale
- **Quick Start:**
  ```bash
  cd examples/large-file-operations
  node generate-large-files.js
  node benchmark.js
  ```

---

## Workspace Examples

These examples demonstrate Nexus Workspace capabilities including Docker-in-Docker, port forwarding, checkpointing, and multi-service orchestration.

### 5. Docker Compose Multi-Service Workspace
A full-stack application with PostgreSQL, Redis, Node.js API, and React frontend.
- **Path:** `examples/docker-compose-workspace/`
- **Features:**
  - Multi-service Docker Compose setup
  - Docker-in-Docker (DinD) support
  - Port forwarding for all services
  - Checkpoint/restore functionality
- **Services:**
  - PostgreSQL (port 5432)
  - Redis (port 6379)
  - Node.js API (port 3000)
  - React Frontend (port 5173)
- **Quick Start:**
  ```bash
  cd examples/docker-compose-workspace
  nexus workspace create fullstack-demo --dind
  nexus workspace console fullstack-demo
  # Inside workspace:
  docker-compose up -d
  ```
- **Docs:** See [nexus-workspace.md](docker-compose-workspace/nexus-workspace.md) for detailed usage

### 6. Python Data Science Workspace
A complete data science environment with Jupyter Lab, PostgreSQL, and ML libraries.
- **Path:** `examples/python-datascience-workspace/`
- **Features:**
  - Jupyter Lab for interactive notebooks
  - Scientific Python stack (Pandas, NumPy, Scikit-learn)
  - PostgreSQL for data storage
  - Persistent data volumes
  - Sample notebooks included
- **Services:**
  - Jupyter Lab (port 8888)
  - PostgreSQL (port 5432)
- **Quick Start:**
  ```bash
  cd examples/python-datascience-workspace
  nexus workspace create datascience-demo --dind
  nexus workspace console datascience-demo
  # Inside workspace:
  docker-compose up -d
  ```
- **Docs:** See [nexus-workspace.md](python-datascience-workspace/nexus-workspace.md) for detailed usage

### 7. Go Microservices Workspace
A multi-service Go application demonstrating microservice architecture.
- **Path:** `examples/go-microservices-workspace/`
- **Features:**
  - Multiple Go services with shared workspace
  - API Gateway pattern
  - JWT authentication
  - Inter-service communication
  - Go modules workspace
- **Services:**
  - API Gateway (port 8080)
  - Auth Service (port 8081)
  - User Service (port 8082)
  - PostgreSQL (port 5432)
- **Quick Start:**
  ```bash
  cd examples/go-microservices-workspace
  nexus workspace create go-microservices --dind
  nexus workspace console go-microservices
  # Inside workspace:
  docker-compose up -d --build
  ```
- **Docs:** See [nexus-workspace.md](go-microservices-workspace/nexus-workspace.md) for detailed usage

---

## Test Plans

Each basic example includes a `nexus-test-plan.md` with:
- Test objectives
- Step-by-step scenarios
- Expected results

Workspace examples include comprehensive `nexus-workspace.md` guides with:
- Workspace creation commands
- Port forwarding setup
- Checkpoint/restore workflows
- Best practices

## Running Basic Tests

1. Start the example project
2. In a separate terminal, run Nexus:
   ```bash
   cd /path/to/example
   nexus analyze
   # or
   nexus watch
   ```
3. Follow the scenarios in `nexus-test-plan.md`
4. Document any friction or issues in `.nexus/collection/`

## Running Workspace Examples

1. Ensure you have Nexus workspace CLI installed
2. Create a workspace with `--dind` flag for Docker support:
   ```bash
   nexus workspace create my-workspace --dind
   ```
3. Set up port forwarding:
   ```bash
   nexus workspace port add my-workspace <port>
   ```
4. Access services via `localhost:<port>` on your host machine

## Adding New Examples

To add a new example:
1. Create a directory under `examples/`
2. Include a `README.md` with setup instructions
3. For workspace examples, include `nexus-workspace.md` with detailed workspace usage
4. Include working code, docker-compose.yml where applicable
5. Update this README to include your example
