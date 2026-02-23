# Multi-User Support PRD

**Status:** Draft  
**Created:** 2026-02-23  
**Component:** Auth / Multi-User  
**Priority:** P1  

---

## 1. Overview

### 1.1 Problem Statement

Nexus currently operates in single-user mode. Teams cannot:
- Share workspaces for pair programming or handoffs
- Control resource usage across team members
- Audit who created/modified workspaces
- Manage access permissions

### 1.2 Goals

1. **Organization Management** - Multi-tenant support with org-level isolation
2. **Team Collaboration** - Share workspaces with fine-grained permissions
3. **Resource Governance** - Enforce quotas and limits per user/organization
4. **Audit Trail** - Track all workspace operations with actor attribution
5. **Simple Auth** - Start with local users, add SSO later

### 1.3 Non-Goals

- Complex RBAC with custom roles (Phase 3)
- SAML integration (use OAuth/OIDC)
- Multi-region organizations
- Billing integration (track usage only)

---

## 2. Architecture

### 2.1 System Overview

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         Nexus Multi-User System                             │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────────────┐     ┌─────────────────────┐     ┌───────────────┐ │
│  │   CLI / Dashboard   │────▶│   Auth Middleware   │────▶│   API Layer   │ │
│  │                     │     │   (JWT/Session)     │     │               │ │
│  └─────────────────────┘     └─────────────────────┘     └───────┬───────┘ │
│                                                                  │         │
│                    ┌─────────────────────────────────────────────┘         │
│                    │                                                        │
│                    ▼                                                        │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                    Permission Engine                                 │   │
│  │  ┌──────────────┐  ┌──────────────┐  ┌──────────────────────────┐  │   │
│  │  │   User Store │  │   Org Store  │  │   Resource Quotas        │  │   │
│  │  └──────────────┘  └──────────────┘  └──────────────────────────┘  │   │
│  └────────────────────────────────────────┬───────────────────────────┘   │
│                                           │                                 │
│                    ┌──────────────────────┼──────────────────────┐         │
│                    │                      │                      │         │
│                    ▼                      ▼                      ▼         │
│  ┌─────────────────────────┐  ┌─────────────────────┐  ┌───────────────┐   │
│  │    Workspace Manager    │  │   Audit Logger      │  │   Existing    │   │
│  │  ┌───────────────────┐  │  │  ┌───────────────┐  │  │  Docker       │   │
│  │  │  Ownership Check  │  │  │  │  Event Store  │  │  │  Backend      │   │
│  │  │  Permission Check │  │  │  └───────────────┘  │  │               │   │
│  │  └───────────────────┘  │  │                     │  │               │   │
│  └─────────────────────────┘  └─────────────────────┘  └───────────────┘   │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 2.2 Data Model

```
┌─────────────┐     ┌───────────────┐     ┌──────────────┐
│   User      │────▶│ Organization  │◀────│    Team      │
├─────────────┤     ├───────────────┤     ├──────────────┤
│ id          │     │ id            │     │ id           │
│ email       │     │ name          │     │ name         │
│ name        │     │ slug          │     │ org_id       │
│ created_at  │     │ owner_id      │     │ created_at   │
└─────────────┘     │ quotas        │     └──────────────┘
       │            └───────────────┘            │
       │                     │                   │
       │            ┌────────┴────────┐          │
       │            ▼                 ▼          │
       │     ┌─────────────┐    ┌────────────┐   │
       └────▶│ OrgMember   │    │ TeamMember │◀──┘
              ├─────────────┤    ├────────────┤
              │ user_id     │    │ team_id    │
              │ org_id      │    │ user_id    │
              │ role        │    │ role       │
              └─────────────┘    └────────────┘

┌──────────────┐     ┌───────────────────┐     ┌──────────────┐
│  Workspace   │◀────│ WorkspaceShare    │────▶│ Permission   │
├──────────────┤     ├───────────────────┤     ├──────────────┤
│ id           │     │ workspace_id      │     │ user_id      │
│ owner_id     │     │ user_id           │     │ workspace_id │
│ org_id       │     │ permission_level  │     │ level        │
│ name         │     │ shared_by         │     │ granted_by   │
│ status       │     │ created_at        │     │ expires_at   │
└──────────────┘     └───────────────────┘     └──────────────┘
```

### 2.3 Permission Model

**Permission Levels:**

| Level | Can Do | Cannot Do |
|-------|--------|-----------|
| **Owner** | Everything | - |
| **Editor** | Start/stop, SSH, exec, view logs | Delete, share, change config |
| **Viewer** | View status, logs | SSH, exec, modify |

**Permission Inheritance:**
```
Organization Admin → All workspaces in org
Team Member → Workspaces owned by team members (configurable)
Workspace Owner → Full control
Workspace Sharee → Limited by share permission
```

---

## 3. API Specification

### 3.1 Authentication

**Login:**
```http
POST /api/v1/auth/login
Content-Type: application/json

{
  "email": "user@example.com",
  "password": "***"
}
```

**Response:**
```json
{
  "token": "eyJhbGciOiJIUzI1NiIs...",
  "user": {
    "id": "usr-123",
    "email": "user@example.com",
    "name": "Jane Doe"
  },
  "organizations": [
    {
      "id": "org-456",
      "name": "Acme Corp",
      "role": "admin"
    }
  ]
}
```

### 3.2 Organizations

**Create Organization:**
```http
POST /api/v1/organizations
Authorization: Bearer <token>
Content-Type: application/json

{
  "name": "Acme Corp",
  "slug": "acme-corp"
}
```

**List Organizations:**
```http
GET /api/v1/organizations
Authorization: Bearer <token>
```

**Get Organization:**
```http
GET /api/v1/organizations/:id
Authorization: Bearer <token>
```

**Response:**
```json
{
  "id": "org-456",
  "name": "Acme Corp",
  "slug": "acme-corp",
  "owner_id": "usr-123",
  "quotas": {
    "max_workspaces": 20,
    "max_cpu_cores": 16,
    "max_memory_gb": 64,
    "max_storage_gb": 500
  },
  "usage": {
    "workspaces": 5,
    "cpu_cores": 8,
    "memory_gb": 20,
    "storage_gb": 100
  },
  "members": [
    {
      "user_id": "usr-123",
      "email": "admin@acme.com",
      "role": "admin",
      "joined_at": "2026-01-15T10:00:00Z"
    }
  ],
  "created_at": "2026-01-15T10:00:00Z"
}
```

### 3.3 Members

**Invite Member:**
```http
POST /api/v1/organizations/:id/invitations
Authorization: Bearer <token>
Content-Type: application/json

{
  "email": "newuser@acme.com",
  "role": "member"
}
```

**List Members:**
```http
GET /api/v1/organizations/:id/members
Authorization: Bearer <token>
```

**Update Member Role:**
```http
PATCH /api/v1/organizations/:id/members/:user_id
Authorization: Bearer <token>
Content-Type: application/json

{
  "role": "admin"
}
```

**Remove Member:**
```http
DELETE /api/v1/organizations/:id/members/:user_id
Authorization: Bearer <token>
```

### 3.4 Teams

**Create Team:**
```http
POST /api/v1/organizations/:id/teams
Authorization: Bearer <token>
Content-Type: application/json

{
  "name": "Backend Team"
}
```

**Add Team Member:**
```http
POST /api/v1/organizations/:id/teams/:team_id/members
Authorization: Bearer <token>
Content-Type: application/json

{
  "user_id": "usr-789"
}
```

### 3.5 Workspace Sharing

**Share Workspace:**
```http
POST /api/v1/workspaces/:id/shares
Authorization: Bearer <token>
Content-Type: application/json

{
  "user_email": "teammate@acme.com",
  "permission": "editor",
  "expires_at": "2026-03-01T00:00:00Z"
}
```

**List Shares:**
```http
GET /api/v1/workspaces/:id/shares
Authorization: Bearer <token>
```

**Revoke Share:**
```http
DELETE /api/v1/workspaces/:id/shares/:user_id
Authorization: Bearer <token>
```

### 3.6 Modified Workspace APIs

All workspace APIs now require authentication and respect permissions:

**List Workspaces (filtered by permissions):**
```http
GET /api/v1/workspaces
Authorization: Bearer <token>

Response:
{
  "workspaces": [
    {
      "id": "ws-123",
      "name": "feature-auth",
      "owner": {
        "id": "usr-123",
        "email": "owner@acme.com"
      },
      "shared_with": [
        {
          "user_id": "usr-456",
          "permission": "editor"
        }
      ],
      "status": "running",
      "resources": {
        "cpu_cores": 2,
        "memory_gb": 4
      }
    }
  ]
}
```

---

## 4. CLI Specification

### 4.1 Auth Commands

```bash
# Login
nexus auth login
# Prompts for email/password

# Login with token (CI/CD)
nexus auth login --token $NEXUS_TOKEN

# Logout
nexus auth logout

# Show current user
nexus auth whoami
```

### 4.2 Organization Commands

```bash
# Create organization
nexus org create "Acme Corp" --slug acme-corp

# List organizations
nexus org list

# Switch active organization
nexus org switch acme-corp

# Show organization details
nexus org show

# Delete organization
nexus org delete acme-corp --force
```

### 4.3 Member Commands

```bash
# Invite member
nexus org invite teammate@acme.com --role editor

# List members
nexus org members

# Update member role
nexus org members update teammate@acme.com --role admin

# Remove member
nexus org members remove teammate@acme.com
```

### 4.4 Team Commands

```bash
# Create team
nexus team create "Backend Team"

# List teams
nexus team list

# Add member to team
nexus team add-member "Backend Team" teammate@acme.com

# Remove member from team
nexus team remove-member "Backend Team" teammate@acme.com
```

### 4.5 Workspace Sharing Commands

```bash
# Share workspace
nexus workspace share feature-auth teammate@acme.com --permission editor

# List shares
nexus workspace shares feature-auth

# Revoke access
nexus workspace unshare feature-auth teammate@acme.com
```

---

## 5. Database Schema

### 5.1 SQLite Schema (Single-Node)

```sql
-- Users table
CREATE TABLE users (
    id TEXT PRIMARY KEY,
    email TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    name TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Organizations table
CREATE TABLE organizations (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT UNIQUE NOT NULL,
    owner_id TEXT NOT NULL REFERENCES users(id),
    max_workspaces INTEGER DEFAULT 20,
    max_cpu_cores INTEGER DEFAULT 16,
    max_memory_gb INTEGER DEFAULT 64,
    max_storage_gb INTEGER DEFAULT 500,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Organization members
CREATE TABLE organization_members (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id),
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    role TEXT NOT NULL CHECK (role IN ('admin', 'member')),
    joined_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, organization_id)
);

-- Teams
CREATE TABLE teams (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    name TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Team members
CREATE TABLE team_members (
    id TEXT PRIMARY KEY,
    team_id TEXT NOT NULL REFERENCES teams(id),
    user_id TEXT NOT NULL REFERENCES users(id),
    joined_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(team_id, user_id)
);

-- Workspaces (updated)
CREATE TABLE workspaces (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    owner_id TEXT NOT NULL REFERENCES users(id),
    organization_id TEXT REFERENCES organizations(id),
    status TEXT NOT NULL,
    cpu_cores INTEGER DEFAULT 2,
    memory_gb INTEGER DEFAULT 4,
    storage_gb INTEGER DEFAULT 20,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Workspace shares
CREATE TABLE workspace_shares (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id),
    permission TEXT NOT NULL CHECK (permission IN ('owner', 'editor', 'viewer')),
    shared_by TEXT NOT NULL REFERENCES users(id),
    expires_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(workspace_id, user_id)
);

-- Audit log
CREATE TABLE audit_logs (
    id TEXT PRIMARY KEY,
    actor_id TEXT NOT NULL REFERENCES users(id),
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    metadata TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Indexes
CREATE INDEX idx_workspaces_owner ON workspaces(owner_id);
CREATE INDEX idx_workspaces_org ON workspaces(organization_id);
CREATE INDEX idx_workspace_shares_workspace ON workspace_shares(workspace_id);
CREATE INDEX idx_workspace_shares_user ON workspace_shares(user_id);
CREATE INDEX idx_audit_logs_actor ON audit_logs(actor_id);
CREATE INDEX idx_audit_logs_resource ON audit_logs(resource_type, resource_id);
CREATE INDEX idx_audit_logs_created ON audit_logs(created_at);
```

---

## 6. Implementation Phases

### Phase 1: Foundation (Week 1-2)

- [ ] Create database schema
- [ ] Implement User store
- [ ] Implement Organization store
- [ ] Create auth middleware
- [ ] JWT token generation/validation

### Phase 2: Auth CLI (Week 3)

- [ ] `nexus auth login/logout`
- [ ] `nexus auth whoami`
- [ ] Token persistence in `~/.nexus/auth`
- [ ] Update all workspace commands to require auth

### Phase 3: Organizations (Week 4-5)

- [ ] `nexus org` commands
- [ ] Organization CRUD APIs
- [ ] Member invitation flow
- [ ] Email notifications (optional, can use CLI for MVP)

### Phase 4: Permissions (Week 6-7)

- [ ] Permission checking middleware
- [ ] Workspace ownership enforcement
- [ ] Workspace sharing APIs
- [ ] `nexus workspace share` commands

### Phase 5: Resource Quotas (Week 8)

- [ ] Quota enforcement on workspace create
- [ ] Usage tracking
- [ ] Quota exceeded errors with helpful messages
- [ ] `nexus org usage` command

### Phase 6: Audit Logging (Week 9)

- [ ] Audit log middleware
- [ ] Log all workspace operations
- [ ] `nexus audit` command to view logs
- [ ] Export audit logs

---

## 7. Configuration

### 7.1 User Config Updates

```yaml
# ~/.nexus/config.yaml
version: 2

auth:
  token: "eyJhbGciOiJIUzI1NiIs..."
  refresh_token: "..."
  expires_at: "2026-03-01T00:00:00Z"

user:
  id: "usr-123"
  email: "user@example.com"
  
organization:
  id: "org-456"
  slug: "acme-corp"
  
# Legacy config preserved
workspace:
  default_backend: docker
```

### 7.2 Server Config

```yaml
# /etc/nexus/server.yaml
auth:
  jwt_secret: "${JWT_SECRET}"
  token_ttl: "24h"
  
  # Future: OAuth providers
  # oauth:
  #   github:
  #     client_id: "..."
  #     client_secret: "..."

database:
  type: sqlite
  path: /var/lib/nexus/nexus.db
  
  # Future: PostgreSQL
  # type: postgres
  # host: localhost
  # port: 5432
  # database: nexus

quotas:
  enabled: true
  default_max_workspaces: 20
  default_max_cpu: 16
  default_max_memory_gb: 64
```

---

## 8. Security Considerations

### 8.1 Authentication

- Passwords: bcrypt with cost factor 12+
- JWT: HS256 with 256-bit secret, 24h expiration
- Token storage: Secure file permissions (0600)
- HTTPS: Required for all auth endpoints

### 8.2 Authorization

- Permission checks on every workspace operation
- No privilege escalation through API manipulation
- Workspace isolation: Users cannot access workspaces they don't own or have shared access to

### 8.3 Audit

- All workspace operations logged
- Actor attribution for all changes
- Immutable audit logs (append-only)
- Log retention: 90 days default

---

## 9. Success Criteria

- [ ] Users can create accounts and log in
- [ ] Organizations can be created and managed
- [ ] Members can be invited and assigned roles
- [ ] Workspaces can be shared with specific permissions
- [ ] Resource quotas are enforced
- [ ] All operations are audited
- [ ] Backward compatibility: Single-user mode works without auth (optional)

---

## 10. Future Enhancements

- **SSO Integration** - GitHub, Google, SAML
- **Custom Roles** - Define custom permission sets
- **Team Workspaces** - Default shared access within teams
- **Workspace Templates** - Org-level template sharing
- **Usage Billing** - Track and bill by usage

---

**Last Updated:** February 2026
