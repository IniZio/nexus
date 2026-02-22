# 7. Roadmap

## 7.1 Implementation Phases

### Phase 1: Foundation (Weeks 1-4)

**Goals:**
- Core workspace lifecycle
- Docker backend
- Basic CLI

**Deliverables:**
- [ ] Workspace manager with state machine
- [ ] Docker provider (Create, Start, Stop, Destroy)
- [ ] Git worktree integration
- [ ] Port allocator
- [ ] Basic CLI: `create`, `up`, `down`, `list`, `destroy`
- [ ] Single config file (`~/.nexus/config.yaml`)

**Success Criteria:**
- Can create and manage Docker-based workspaces
- Basic context switch works (<5s target)
- Configuration in single YAML file

---

### Phase 2: Performance & UX (Weeks 5-8)

**Goals:**
- Sub-2s context switch
- State preservation
- Better UX

**Deliverables:**
- [ ] Optimized container lifecycle for fast switch
- [ ] Process state preservation
- [ ] Terminal history persistence
- [ ] Port conflict auto-resolution
- [ ] `switch` command with <2s target
- [ ] Better error messages and recovery hints
- [ ] Progress indicators for long operations

**Success Criteria:**
- Context switch consistently <2s
- Dev servers survive switch
- Clear, actionable error messages

---

### Phase 3: Advanced Features (Weeks 9-12)

**Goals:**
- Snapshots/checkpoints
- IDE integration
- Security hardening

**Deliverables:**
- [ ] Snapshot create/restore
- [ ] SSH agent forwarding
- [ ] Secret management (keychain integration)
- [ ] WebSocket daemon
- [ ] SDK (TypeScript)
- [ ] IDE plugins (OpenCode, Claude Code)

**Success Criteria:**
- Snapshots work reliably
- SSH keys accessible in workspaces
- IDE plugins can connect to workspaces

---

### Phase 4: Backend Expansion (Weeks 13-16)

**Goals:**
- Sprite backend
- Hybrid workflows
- Production readiness

**Deliverables:**
- [ ] Sprite provider implementation
- [ ] Backend switching (`--backend=sprite`)
- [ ] Workspace migration between backends
- [ ] Auto-fallback (Docker → Sprite)
- [ ] Comprehensive testing
- [ ] Documentation

**Success Criteria:**
- Both backends work with same UX
- Seamless backend switching
- 99.9% availability

---

### Phase 5: Polish & Scale (Weeks 17-20)

**Goals:**
- Performance optimization
- Enterprise features
- Real-world validation

**Deliverables:**
- [ ] Prebuilt images for common stacks
- [ ] Resource quotas and limits
- [ ] Team workspace sharing
- [ ] Advanced monitoring
- [ ] hanlun-lms real-world testing
- [ ] Migration guide from existing setups

**Success Criteria:**
- <30s cold start with prebuilt images
- Resource limits enforced
- Successfully handles hanlun-lms complexity

---

## 7.2 Migration Guide

### From Current State (Pre-Workspace)

```bash
# 1. Backup existing .nexus directory
cp -r .nexus .nexus.backup.$(date +%Y%m%d)

# 2. Create new config file
cat > ~/.nexus/config.yaml << 'EOF'
daemon:
  port: 8080

defaults:
  backend: docker
  idle_timeout: 30m
  resources: medium
EOF

# 3. Initialize workspace system
boulder workspace init
# - Migrates existing containers
# - Preserves configurations

# 4. Import existing worktrees
boulder workspace import --detect

# 5. Verify
boulder workspace list
```

### From Git Worktrees (No Containers)

```bash
# For each existing worktree
for worktree in .worktree/*; do
  name=$(basename $worktree)
  boulder workspace create $name --from-worktree=$worktree
done

# Worktrees now have containers
```

### From GitHub Codespaces

```bash
# Import devcontainer.json
boulder workspace create my-project \
  --devcontainer=.devcontainer/devcontainer.json \
  --dotfiles=https://github.com/user/dotfiles
```

---

## 7.3 Success Metrics

### Key Performance Indicators (KPIs)

| KPI | Target | Measurement |
|-----|--------|-------------|
| **Adoption rate** | 90% active users | % users with ≥2 workspaces |
| **Context switch time** | <2s (p95) | Telemetry timing |
| **Workspace availability** | 99.9% | Uptime monitoring |
| **Error rate** | <0.1% | Failed operations / total |
| **User satisfaction** | >4.0/5 | Survey (quarterly) |
| **Support tickets** | <1/100 ops | Tickets per workspace op |

### Leading Indicators

| Indicator | Target | Action if Below |
|-----------|--------|-----------------|
| First workspace creation | <5 min | Improve onboarding |
| Return rate (next day) | >80% | Check usability issues |
| Workspace switch frequency | >5/day | Promote parallel workflow |
| Snapshot usage | >30% of users | Feature awareness campaign |

---

## 7.4 Configuration Evolution

### Simplified Model (Current)

```yaml
# ~/.nexus/config.yaml
daemon:
  port: 8080

defaults:
  backend: docker
  idle_timeout: 30m

workspaces:
  hanlun:
    path: /Users/newman/code/hanlun
    ports: [3000, 5173]
```

**Key Changes from Previous Design:**
- ✅ Single config file
- ✅ Workspaces section in main config
- ✅ No separate workspace.yaml per workspace
- ✅ Auto-discovery supported
- ✅ Sensible defaults

---

## 7.5 Rollback Procedure

```bash
# If migration fails:

# 1. Stop all workspaces
boulder workspace list --running | xargs -I {} boulder workspace down {}

# 2. Restore backup
rm -rf .nexus
mv .nexus.backup.YYYYMMDD .nexus

# 3. Verify old state
boulder workspace list

# 4. Report issue
boulder admin support-bundle --submit
```

---

## 7.6 Post-Launch Checklist

- [ ] Monitor error rates (< 0.1%)
- [ ] Track context switch times (< 2s p95)
- [ ] Gather user feedback weekly
- [ ] Respond to support tickets (< 24h)
- [ ] Update documentation based on questions
- [ ] Plan Phase 6 features based on usage

---

## 7.7 Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| **Performance targets not met** | Medium | High | Early benchmarking, optimization sprints |
| **Docker daemon issues** | Medium | High | Health checks, auto-restart |
| **User adoption challenges** | Medium | High | UX focus, gradual rollout, training |
| **Security vulnerabilities** | Low | Critical | Security review, penetration testing |
| **Integration complexity** | High | Medium | Modular design, clear APIs |

---

## 7.8 Timeline Summary

| Phase | Duration | Key Deliverable |
|-------|----------|-----------------|
| Phase 1 | Weeks 1-4 | Core workspace lifecycle |
| Phase 2 | Weeks 5-8 | <2s context switch |
| Phase 3 | Weeks 9-12 | Snapshots, IDE integration |
| Phase 4 | Weeks 13-16 | Sprite backend |
| Phase 5 | Weeks 17-20 | Production readiness |

**Total:** 20 weeks to production-ready
