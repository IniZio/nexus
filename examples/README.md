# Nexus Examples

Test drive Nexus with these example projects.

## Available Examples

### 1. Blank Node Project
A minimal Express.js server for basic Nexus testing.
- **Path:** `examples/blank-node-project/`
- **Focus:** Basic file analysis, hot reload detection, endpoint discovery
- **Quick Start:**
  ```bash
  cd examples/blank-node-project
  npm install
  npm start
  ```

### 2. React Hot Reload
A Create React App for testing frontend hot reload capabilities.
- **Path:** `examples/react-hot-reload/`
- **Focus:** React component changes, CSS hot reload, state preservation
- **Quick Start:**
  ```bash
  cd examples/react-hot-reload
  npm install
  npm start
  ```

### 3. Complex Backend
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

### 4. Large File Operations
Generate and benchmark large files for performance testing.
- **Path:** `examples/large-file-operations/`
- **Focus:** Performance testing, memory usage, change detection at scale
- **Quick Start:**
  ```bash
  cd examples/large-file-operations
  node generate-large-files.js
  node benchmark.js
  ```

## Test Plans

Each example includes a `nexus-test-plan.md` with:
- Test objectives
- Step-by-step scenarios
- Expected results

## Running Tests

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

## Adding New Examples

To add a new example:
1. Create a directory under `examples/`
2. Include a `README.md` with setup instructions
3. Include a `nexus-test-plan.md` with test scenarios
4. Update this README to include your example
