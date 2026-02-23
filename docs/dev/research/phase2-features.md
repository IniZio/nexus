# Nexus Next Phase Research Summary

**Date:** February 2026  
**Purpose:** Research findings for Phase 2 roadmap features

---

## Executive Summary

Based on analysis of the current Nexus architecture and industry patterns, the next phase should focus on **three high-value features**:

1. **Multi-User Support** - Enable team collaboration with workspace sharing
2. **Web Dashboard** - Real-time monitoring and management interface
3. **Auto-Update System** - Seamless, secure CLI updates

These features build directly on the existing foundation (Docker workspaces, SSH access, nexusd daemon) and align with Nexus philosophy: deterministic, traceable, production-ready.

---

## 1. Multi-User Support Research

### Problem Analysis

Current Nexus is single-user only. Teams need:
- Shared workspace access for pair programming
- Resource quotas to prevent one user from consuming all resources
- Permission levels (admin, developer, viewer)
- Workspace ownership and sharing

### Industry Patterns

| Tool | Multi-User Approach | Key Insight |
|------|---------------------|-------------|
| **Gitpod** | Organization-based with teams | Shared workspaces via URL |
| **GitHub Codespaces** | Repo-based permissions | Inherits GitHub access control |
| **Docker Desktop** | No multi-user | Single-user only |
| **Portainer** | RBAC with teams | Team → User → Resource mapping |
| **Kubernetes** | Namespace isolation | Strong isolation, complex setup |

### Recommended Approach: Simple RBAC

```
User → Organization → Team → Workspace
```

**Key Design Decisions:**
1. **Organization** as top-level container (like GitHub orgs)
2. **Teams** for grouping (e.g., "backend", "frontend")
3. **Workspace ownership** - creator is owner, can share
4. **Permission levels:**
   - `owner`: Full control, can delete, manage sharing
   - `editor`: Start/stop, SSH access, cannot delete
   - `viewer`: View status only, no SSH

### Resource Quotas

Per-organization limits:
- Max workspaces: 20 (configurable)
- Max CPU: 16 cores
- Max memory: 64GB
- Max disk: 500GB

Per-workspace defaults:
- CPU: 2 cores
- Memory: 4GB
- Disk: 20GB

---

## 2. Web Dashboard Research

### Problem Analysis

Current Nexus is CLI-only. Users need:
- Visual workspace status overview
- Real-time resource monitoring (CPU, memory, disk)
- Easy workspace creation (click instead of type)
- Team visibility (who's using what)

### Industry Patterns

| Dashboard | Strengths | Weaknesses |
|-----------|-----------|------------|
| **Kubernetes Dashboard** | Real-time updates, resource graphs | Complex for beginners |
| **Portainer** | Clean UI, Docker-focused | Limited workspace concepts |
| **Gitpod Dashboard** | Simple, task-focused | Less resource visibility |
| **VS Code Server** | IDE in browser | Not a management dashboard |

### Recommended Approach: Real-Time React Dashboard

**Architecture:**
```
Browser ← WebSocket → nexusd API ←→ Docker
         ↓
    React + TypeScript + Tailwind
```

**Key Features:**
1. **Workspace List View** - Status, owner, resources at a glance
2. **Real-Time Metrics** - CPU/memory graphs via WebSocket
3. **One-Click Actions** - Start/stop/SSH via browser
4. **Team View** - See teammate workspaces (if shared)
5. **Activity Logs** - Audit trail of workspace events

**Technical Stack:**
- Frontend: React 18 + TypeScript + Tailwind CSS
- State: React Query + Zustand
- Real-time: WebSocket (existing nexusd support)
- Charts: Recharts or Chart.js
- Build: Vite

---

## 3. Auto-Update System Research

### Problem Analysis

Nexus CLI needs updates for:
- Security patches
- New features
- Bug fixes

Current pain points:
- Manual download and install
- No update notifications
- Risk of running outdated versions

### Industry Patterns

| Tool | Update Mechanism | Security Model |
|------|-----------------|----------------|
| **VS Code** | Background download, restart to apply | Signed packages, staged rollout |
| **Docker Desktop** | Auto-check, manual install | Signed DMG/PKG |
| **Homebrew** | Package manager | GPG-signed bottles |
| **Rustup** | Self-update binary | HTTPS + checksums |

### Recommended Approach: In-Place Binary Update

**Mechanism:**
```
nexus update check    → Query GitHub releases API
nexus update install  → Download → Verify → Replace → Restart
```

**Security Model:**
1. **HTTPS only** - All downloads over TLS
2. **Checksum verification** - SHA256 of downloaded binary
3. **Signature verification** - Cosign or minisign signatures
4. **Atomic replacement** - Download to temp, swap on success
5. **Rollback** - Keep previous version for 24h

**Update Channels:**
- `stable` - Production-ready (default)
- `beta` - Feature preview
- `nightly` - Latest commits

---

## 4. Feature Prioritization

### Priority Matrix

| Feature | User Value | Implementation Complexity | Priority |
|---------|-----------|---------------------------|----------|
| Web Dashboard | High | Medium | **P1** |
| Multi-User Support | High | Medium | **P1** |
| Auto-Update | Medium | Low | **P2** |
| Additional Backends (Firecracker) | Medium | High | P3 |
| Workspace Templates Marketplace | Medium | High | P3 |
| CI/CD Integration | Low | High | P4 |

### Recommended Phase 2 Scope

**Q1 2026:**
1. Web Dashboard (MVP)
2. Auto-Update System

**Q2 2026:**
3. Multi-User Support (MVP)
4. Dashboard enhancements

---

## 5. Architecture Implications

### Database Requirements

Current: File-based state storage (`~/.nexus/state/`)

Next phase needs:
- **Users table** - IDs, emails, org associations
- **Organizations table** - Teams, quotas
- **Permissions table** - User-workspace mappings
- **Audit logs** - Who did what when

**Recommendation:** SQLite for single-node, PostgreSQL option for multi-node

### API Changes

Add authentication middleware:
```go
// Current
func (s *Server) handleWorkspaces(w http.ResponseWriter, r *http.Request)

// Next phase
func (s *Server) handleWorkspaces(authCtx *AuthContext, w http.ResponseWriter, r *http.Request)
```

### Configuration Changes

New `~/.nexus/config.yaml` fields:
```yaml
auth:
  provider: local  # or sso, github, google
  
organization:
  id: org-123
  name: "Acme Corp"
  
quotas:
  max_workspaces: 20
  max_cpu: 16
  max_memory_gb: 64
```

---

## 6. Success Criteria

### Web Dashboard
- [ ] Load time < 2 seconds
- [ ] Real-time updates within 1 second
- [ ] Works on mobile (responsive)
- [ ] All CLI features accessible via UI

### Multi-User Support
- [ ] Invite team members via email
- [ ] Share workspace with specific permissions
- [ ] Resource quotas enforced
- [ ] Activity audit logs

### Auto-Update
- [ ] Check for updates on startup (configurable)
- [ ] One-command update install
- [ ] Automatic rollback on failure
- [ ] Signed/verified binaries only

---

## 7. Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Multi-user complexity | High | Start with simple owner/editor/viewer model |
| Dashboard performance | Medium | Use pagination, WebSocket for updates only |
| Update failures | High | Atomic replacement, automatic rollback |
| Auth complexity | Medium | Support local auth first, SSO later |

---

**Next Steps:**
1. Create detailed PRDs for top 3 features
2. Update roadmap.md with new priorities
3. Design database schema for multi-user
4. Create dashboard wireframes

---

**Last Updated:** February 2026
