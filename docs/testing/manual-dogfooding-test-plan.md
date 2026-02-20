# Nexus Workspace SDK - Manual Dogfooding Test Plan

**Status:** Ready for Execution  
**Purpose:** Real-world stress testing by using Nexus to develop actual projects  
**Approach:** Similar to Sprites - create workspaces, develop in them, verify services work

---

## Test Philosophy

> "You don't know if it works until you use it to build something real."

This test plan involves:
1. Setting up Nexus components locally
2. Creating workspaces with real projects
3. Actually developing those projects using OpenCode + Nexus
4. Finding and fixing real issues
5. Verifying services/deployments work

---

## Test Environment Setup

### Prerequisites

```bash
# Local machine requirements
- Node.js 18+
- Go 1.21+
- Docker & Docker Compose
- OpenCode CLI installed
- Git configured

# Clone test repositories (for realistic projects)
git clone https://github.com/oursky/hanlun-lms.git /tmp/test-repos/hanlun-lms
git clone https://github.com/facebook/react.git /tmp/test-repos/react
```

### Nexus Components Setup

```bash
# 1. Start Workspace Daemon
cd packages/workspace-daemon
go build -o workspace-daemon ./cmd/daemon
docker build -t nexus-workspace-daemon:latest .

# Run daemon locally
docker run -d \
  --name nexus-daemon \
  -p 8080:8080 \
  -v /tmp/nexus-workspaces:/workspace \
  -e NEXUS_TOKEN=test-token \
  nexus-workspace-daemon:latest \
  --port 8080 --workspace-dir /workspace

# 2. Configure OpenCode
cat > /tmp/test-opencode/opencode.json << 'EOF'
{
  "$schema": "https://opencode.ai/config.json",
  "plugin": ["@nexus/opencode-plugin"],
  "nexus": {
    "workspace": {
      "endpoint": "ws://localhost:8080",
      "workspaceId": "test-workspace",
      "token": "test-token"
    }
  },
  "command": {
    "nexus-connect": {
      "template": "Connect to Nexus workspace",
      "description": "Connect to remote workspace"
    }
  }
}
EOF
```

---

## Test Scenarios

### Test 1: Blank Project - Full Development Cycle

**Goal:** Verify basic file operations, npm init, development server

**Steps:**

1. **Create blank workspace**
   ```bash
   mkdir -p /tmp/nexus-workspaces/blank-project
   cd /tmp/nexus-workspaces/blank-project
   git init
   ```

2. **Start OpenCode with Nexus plugin**
   ```bash
   cd /tmp/test-opencode
   opencode
   # Should load @nexus/opencode-plugin
   ```

3. **Test file operations**
   ```
   User: "Create a package.json for a new Node.js project"
   
   Expected: OpenCode creates file via Nexus SDK → Daemon writes to workspace
   Verify: Check /tmp/nexus-workspaces/blank-project/package.json exists
   ```

4. **Test npm install**
   ```
   User: "Install express and create a simple server"
   
   Expected: npm install runs in workspace, node_modules created
   Verify: ls /tmp/nexus-workspaces/blank-project/node_modules/express
   ```

5. **Test development server**
   ```
   User: "Create server.js with Express and start it on port 3000"
   
   Expected: Server runs in workspace, accessible via port forward
   Verify: curl http://localhost:3000 (via port forwarding)
   ```

6. **Test hot reload**
   ```
   User: "Add a /health endpoint and test it"
   
   Expected: File edited, server restarts or hot reloads
   Verify: curl http://localhost:3000/health returns 200
   ```

**Issues to Watch For:**
- [ ] File writes not persisting
- [ ] npm install too slow (network issues)
- [ ] Port forwarding not working
- [ ] Process not starting/stopping correctly

---

### Test 2: Real Project - Hanlun LMS

**Goal:** Test with complex, real-world project

**Repository:** `git@github.com:oursky/hanlun-lms.git`

**Steps:**

1. **Clone into workspace**
   ```bash
   cd /tmp/nexus-workspaces
   git clone git@github.com:oursky/hanlun-lms.git
   ```

2. **Start OpenCode in workspace**
   ```bash
   cd hanlun-lms
   opencode
   ```

3. **Install dependencies**
   ```
   User: "Install all dependencies and verify the project builds"
   
   Expected: npm/yarn install works, build succeeds
   Time: Should complete in reasonable time (< 5 min for deps)
   ```

4. **Run database migrations**
   ```
   User: "Set up the database and run migrations"
   
   Expected: Database commands execute in workspace
   Verify: Tables created, migrations table populated
   ```

5. **Start development server**
   ```
   User: "Start the development server"
   
   Expected: Server starts, accessible via forwarded port
   Verify: Can access web UI at http://localhost:PORT
   ```

6. **Make code changes**
   ```
   User: "Add a new API endpoint for /api/health"
   
   Expected: File edited, server reloads, endpoint works
   Verify: curl http://localhost:PORT/api/health
   ```

7. **Run tests**
   ```
   User: "Run the test suite"
   
   Expected: Tests execute in workspace, results shown
   Time: Should complete, not hang
   ```

**Issues to Watch For:**
- [ ] Git operations (clone, push) fail
- [ ] Large file operations timeout
- [ ] Database connections fail
- [ ] Environment variables not available
- [ ] Build tools missing in workspace

---

### Test 3: React Development - Complex Frontend

**Goal:** Test frontend development with hot reload, build process

**Repository:** Facebook React (or create-react-app project)

**Steps:**

1. **Create React app in workspace**
   ```bash
   cd /tmp/nexus-workspaces
   npx create-react-app react-test
   ```

2. **Develop with OpenCode**
   ```
   User: "Start the React development server"
   
   Expected: CRA dev server starts on port 3000
   Verify: Browser can access http://localhost:3000
   ```

3. **Test hot module replacement**
   ```
   User: "Change the App.js component and see it hot reload"
   
   Expected: File change detected, browser updates without refresh
   Verify: UI updates, WebSocket HMR works
   ```

4. **Build for production**
   ```
   User: "Build the app for production and verify the build output"
   
   Expected: Build succeeds, build/ directory created
   Verify: Can serve static files from build/
   ```

**Issues to Watch For:**
- [ ] WebSocket HMR not working
- [ ] File watching (chokidar/inotify) not detecting changes
- [ ] Build process too slow
- [ ] Port conflicts

---

### Test 4: Multi-Workspace Switching

**Goal:** Test switching between multiple workspaces

**Steps:**

1. **Create multiple workspaces**
   ```bash
   mkdir -p /tmp/nexus-workspaces/{project-a,project-b,project-c}
   # Initialize each with different projects
   ```

2. **Switch between workspaces**
   ```
   User: "/nexus-disconnect"
   User: Update opencode.json to point to project-b
   User: "/nexus-connect"
   
   Expected: Clean switch, new workspace loaded
   Verify: Files from project-b are accessible
   ```

3. **Concurrent workspaces**
   ```
   Open 3 terminals
   Each with different workspace
   Make changes in all simultaneously
   
   Expected: No cross-contamination between workspaces
   Verify: Each workspace has its own files/processes
   ```

---

### Test 5: Stress Test - Large Files & Many Operations

**Goal:** Test performance with large files and many operations

**Steps:**

1. **Large file operations**
   ```
   Create 100MB file in workspace
   Read it back
   Copy it
   Delete it
   
   Expected: Operations complete in reasonable time
   Time: < 30 seconds for each
   ```

2. **Many small files**
   ```
   Create 1000 small files (1KB each)
   List directory
   Delete all
   
   Expected: No timeouts, reasonable performance
   ```

3. **Rapid operations**
   ```bash
   # Script rapid file operations
   for i in {1..100}; do
     echo "content $i" > file$i.txt
   done
   
   Expected: All succeed, no dropped connections
   ```

---

### Test 6: Error Recovery & Edge Cases

**Goal:** Test resilience to errors

**Steps:**

1. **Network disconnection**
   ```
   Start operation (long build)
   Disconnect network
   Reconnect
   
   Expected: Auto-reconnect, operation resumes or fails gracefully
   ```

2. **Workspace daemon restart**
   ```
   docker restart nexus-daemon
   
   Expected: SDK reconnects, operations continue
   ```

3. **Invalid operations**
   ```
   Try to read non-existent file
   Try to write to system directory
   Try to execute invalid command
   
   Expected: Clear error messages, no crashes
   ```

---

## Test Execution Log

### Day 1: Setup & Blank Project

| Time | Activity | Status | Issues Found |
|------|----------|--------|--------------|
| 09:00 | Setup daemon and SDK | ⏳ Pending | |
| 10:00 | Test 1: Blank project init | ⏳ Pending | |
| 11:00 | Test 1: npm install | ⏳ Pending | |
| 12:00 | Test 1: Dev server | ⏳ Pending | |
| 14:00 | Test 1: Hot reload | ⏳ Pending | |
| 15:00 | Fix issues found | ⏳ Pending | |

### Day 2: Real Project (Hanlun LMS)

| Time | Activity | Status | Issues Found |
|------|----------|--------|--------------|
| 09:00 | Clone and setup | ⏳ Pending | |
| 10:00 | Dependency install | ⏳ Pending | |
| 11:00 | Database setup | ⏳ Pending | |
| 12:00 | Dev server start | ⏳ Pending | |
| 14:00 | Code changes | ⏳ Pending | |
| 15:00 | Run tests | ⏳ Pending | |
| 16:00 | Fix issues | ⏳ Pending | |

### Day 3: Frontend & Stress

| Time | Activity | Status | Issues Found |
|------|----------|--------|--------------|
| 09:00 | React project setup | ⏳ Pending | |
| 10:00 | Hot reload testing | ⏳ Pending | |
| 11:00 | Production build | ⏳ Pending | |
| 14:00 | Large file tests | ⏳ Pending | |
| 15:00 | Stress testing | ⏳ Pending | |
| 16:00 | Error recovery | ⏳ Pending | |

---

## Success Criteria

**Must Pass (Blocker):**
- [ ] File operations work reliably (read/write/delete)
- [ ] npm install works for real projects
- [ ] Development servers can start and accept connections
- [ ] OpenCode can use workspace without crashing

**Should Pass (Critical):**
- [ ] Hot reload works for frontend
- [ ] Git operations work (clone, commit, push)
- [ ] Tests can run in workspace
- [ ] Port forwarding works

**Nice to Pass (Enhancement):**
- [ ] Sub-second file operations
- [ ] No noticeable lag in development
- [ ] Handles large files gracefully
- [ ] Auto-recovers from network issues

---

## Issue Tracking Template

When issues are found, document:

```markdown
### Issue #{N}: {Title}

**Test:** {Which test scenario}
**Severity:** {Blocker/Critical/Minor}

**Description:**
{What happened}

**Steps to Reproduce:**
1. {Step 1}
2. {Step 2}
3. {Step 3}

**Expected Behavior:**
{What should happen}

**Actual Behavior:**
{What actually happened}

**Logs:**
```
{Relevant logs}
```

**Fix:**
{How it was fixed (or plan to fix)}
```

---

## Post-Test Actions

After all tests complete:

1. **Fix critical issues** found during testing
2. **Update documentation** with any workarounds
3. **Write regression tests** for fixed issues
4. **Performance benchmarks** - measure and document
5. **User guide** - write based on actual usage

---

## Reference: Sprites Approach

How Sprites does it:
- Creates persistent VMs (microVMs)
- User runs `sprite exec` commands
- Files persist between commands
- HTTP access for web services
- Auto-wake on request

What we're testing:
- Similar but with SDK integration
- More transparent (feels like local)
- Works with existing agents (OpenCode)
- No command wrappers needed

---

**Start Date:** ___

**Tester:** ___

**Notes:** ___
