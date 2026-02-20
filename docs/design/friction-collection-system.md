# Nexus Friction Collection System Design

**Version:** 1.0  
**Status:** Active  
**Purpose:** Structured collection of agent usage traces and friction points without exposing credentials

---

## Overview

The Friction Collection System captures real-world usage patterns, performance bottlenecks, and usability issues encountered during Nexus development and testing.

**Key Principle:** Collect rich diagnostic data while **never** capturing credentials, tokens, or sensitive information.

---

## Architecture

### Data Flow

```
User Encounter Issue
       │
       ▼
┌─────────────────────────┐
│ 1. Local Detection      │
│    - SDK intercepts     │
│    - Plugin logs        │
│    - User reports       │
└──────────┬──────────────┘
           │
           ▼
┌─────────────────────────┐
│ 2. Sanitization Layer   │
│    - Remove credentials │
│    - Hash identifiers   │
│    - Redact paths       │
└──────────┬──────────────┘
           │
           ▼
┌─────────────────────────┐
│ 3. Local Storage        │
│    - .nexus/collection/ │
│    - Never committed    │
│    - User controls      │
└──────────┬──────────────┘
           │
           ▼ (optional)
┌─────────────────────────┐
│ 4. Submit Report        │
│    - Review by user     │
│    - Manual approval    │
│    - Encrypted upload   │
└─────────────────────────┘
```

---

## Data Collection Types

### 1. Friction Reports

**When:** User encounters difficulty, error, or slowdown

**Collected:**
```typescript
interface FrictionReport {
  id: string;                    // UUID
  timestamp: string;             // ISO 8601
  category: FrictionCategory;    // enum
  severity: 'low' | 'medium' | 'high' | 'critical';
  
  context: {
    workspaceId: string;         // Hashed
    agentType: string;           // 'opencode' | 'claude' | 'cursor'
    nexusVersion: string;
    sdkVersion: string;
  };
  
  description: string;           // User description
  reproduction: ReproductionSteps;
  
  technical: {
    errorCode?: string;
    stackTrace?: string;         // Sanitized
    latency?: number;            // ms
    operation: string;           // 'fs.readFile', 'exec', etc.
  };
  
  files: {
    logPath: string;             // Local path (not committed)
    screenshot?: string;         // If GUI issue
  };
}
```

**NOT Collected:**
- API keys, tokens, passwords
- File contents
- Environment variables with secrets
- Absolute paths (converted to relative)
- Usernames, emails, PII

### 2. Trace Sessions

**When:** Recording a full development session for analysis

**Collected:**
```typescript
interface TraceSession {
  id: string;
  startTime: string;
  endTime: string;
  
  metrics: {
    totalOperations: number;
    failedOperations: number;
    avgLatency: number;
    reconnections: number;
  };
  
  operations: OperationTrace[];  // Sampled (not all)
  
  summary: {
    filesAccessed: string[];     // Relative paths only
    commandsExecuted: string[];  // Command names only (no args)
    duration: number;            // seconds
  };
}

interface OperationTrace {
  timestamp: string;
  operation: string;             // 'fs.readFile', 'exec.bash'
  duration: number;              // ms
  success: boolean;
  errorCode?: string;
}
```

### 3. Issue Reproductions

**When:** User creates minimal reproduction case

**Collected:**
- Minimal code snippet
- Steps to reproduce
- Expected vs actual behavior
- Environment details (sanitized)

---

## Sanitization Rules

### Automatic Sanitization (Always Applied)

| Pattern | Replacement | Example |
|---------|-------------|---------|
| API Keys | `[REDACTED_API_KEY]` | `sk-abc123` → `[REDACTED_API_KEY]` |
| Tokens | `[REDACTED_TOKEN]` | `eyJhbG...` → `[REDACTED_TOKEN]` |
| Passwords | `[REDACTED_PASSWORD]` | `mypassword` → `[REDACTED_PASSWORD]` |
| Absolute Paths | Relative path | `/home/user/project` → `./project` |
| Environment Vars | `[ENV:NAME]` | Value of `$SECRET` → `[ENV:SECRET]` |
| Email Addresses | `[REDACTED_EMAIL]` | `user@example.com` → `[REDACTED_EMAIL]` |
| IP Addresses | `[REDACTED_IP]` | `192.168.1.1` → `[REDACTED_IP]` |

### Hashing Identifiers

Sensitive identifiers are hashed (SHA-256) for correlation without exposure:

```typescript
// Before: workspaceId = "proj-123-abc"
// After: workspaceId = "a1b2c3d4..." (hash)

function hashIdentifier(id: string): string {
  return crypto.createHash('sha256')
    .update(id + SALT)  // Salt prevents rainbow tables
    .digest('hex')
    .slice(0, 16);      // Truncate for readability
}
```

---

## Storage

### Local Storage (`.nexus/collection/`)

```
.nexus/collection/
├── data/                        # Never committed
│   ├── frictions/
│   │   ├── 2024-01-15/
│   │   │   └── friction-abc123.json
│   │   └── 2024-01-16/
│   ├── traces/
│   │   └── trace-session-xyz789.json
│   └── reproductions/
│
├── sessions/                    # Current session logs
│   └── current-session.log      # Rotated daily
│
├── export/                      # User-exported reports
│   └── report-2024-01-15.md
│
├── templates/                   # Committed to repo ✅
│   └── friction-report.md
│
└── schemas/                     # Committed to repo ✅
    └── friction.schema.json
```

**Gitignore Rules:**
```gitignore
# Collection data (may contain sensitive info)
.nexus/collection/data/
.nexus/collection/sessions/
.nexus/collection/export/
*.local.json
*.local.md
```

---

## User Control

### Opt-In by Default

Friction collection is **opt-in**. Users must explicitly enable:

```json
// .nexus/config.json
{
  "collection": {
    "enabled": true,
    "autoSubmit": false,      // Require manual review
    "includeTraces": true,
    "retentionDays": 30
  }
}
```

### Manual Review Required

Before any data leaves the local machine:

1. User runs `nexus collection export`
2. System generates report from templates
3. User reviews in editor
4. User approves with `nexus collection submit`
5. Data is encrypted and uploaded

### Immediate Deletion

Users can delete all local collection data:

```bash
nexus collection purge --all
nexus collection purge --older-than 7d
```

---

## Submission Workflow

### Step 1: Create Report

```bash
nexus collection export --friction abc123 --format markdown
```

Generates: `.nexus/collection/export/friction-report-abc123.md`

### Step 2: User Review

```markdown
# Friction Report (Preview)

## Summary
- **Category:** Performance
- **Severity:** High
- **Operation:** fs.readFile (large file)

## Technical Details
- **Latency:** 4523ms
- **File Size:** 100MB
- **Workspace:** a1b2c3d4 (hashed)

## Raw Log (excerpt)
```
[2024-01-15T10:30:00Z] Operation started
[2024-01-15T10:30:04Z] Operation completed (4523ms)
```

## User Description
"Reading large files is very slow"

---
**⚠️ Review before submitting:**
- [ ] No credentials visible
- [ ] No sensitive paths
- [ ] Accurate description

[Submit] [Edit] [Cancel]
```

### Step 3: Encrypt and Submit

```bash
nexus collection submit friction-abc123
```

- Data encrypted with project public key
- Uploaded to secure endpoint
- User receives confirmation with ticket ID

---

## Comparison: Old vs New System

| Aspect | Old (friction-log.md) | New (Collection System) |
|--------|----------------------|-------------------------|
| **Format** | Free-form markdown | Structured JSON + templates |
| **Sanitization** | Manual | Automatic |
| **Storage** | Git repo (⚠️ risk) | Local only (.gitignored) |
| **Credentials** | Could leak | Never collected |
| **User Control** | Limited | Full opt-in/opt-out |
| **Review** | None | Required before submit |
| **Encryption** | None | E2E encrypted |
| **Correlation** | Difficult | Hashed identifiers |
| **Search** | Text search | Structured queries |

---

## Implementation

### SDK Integration

```typescript
// Auto-collect on errors
client.onError((error) => {
  if (config.collection.enabled) {
    collector.recordFriction({
      category: categorizeError(error),
      severity: calculateSeverity(error),
      technical: sanitizeTechnicalDetails(error),
      context: hashContext(getContext())
    });
  }
});

// Manual collection
import { collector } from '@nexus/workspace-sdk';

collector.recordFriction({
  description: "File operations are slow",
  category: "performance"
});
```

### CLI Commands

```bash
# Collection management
nexus collection status              # Show collection stats
nexus collection list                # List local friction reports
nexus collection export <id>         # Export to review format
nexus collection submit <id>         # Submit after review
nexus collection purge               # Delete local data

# Trace sessions
nexus trace start                    # Start recording session
nexus trace stop                     # Stop and save
nexus trace list                     # List recorded sessions
```

---

## Privacy Compliance

### GDPR/CCPA Compliance

- ✅ **Right to be forgotten**: `nexus collection purge`
- ✅ **Data portability**: Export to standard formats
- ✅ **Consent management**: Opt-in by default
- ✅ **Data minimization**: Only collect necessary data
- ✅ **Purpose limitation**: Used only for improvement

### Security

- Local data encrypted at rest
- Transmission uses TLS 1.3
- Server-side encryption (AES-256)
- Access logs audited
- Data retention limits enforced

---

## Migration from friction-log.md

### Step 1: Remove from History

```bash
# Remove friction-log.md from git history
git filter-branch --force --tree-filter 'rm -f .nexus/dogfooding/friction-log.md' --tag-name-filter cat -- --all

# Clean up
git reflog expire --expire=now --all
git gc --prune=now --aggressive

# Force push (coordinate with team)
git push origin --force --all
```

### Step 2: Archive Old Data

```bash
# Move old friction logs to collection system
nexus collection import-friction-log .nexus/dogfooding/friction-log.md

# Review and sanitize
nexus collection review --all
```

### Step 3: Enable New System

```bash
# Initialize collection system
nexus collection init

# Configure
nexus config set collection.enabled true
nexus config set collection.autoSubmit false
```

---

## References

- [Friction Collection Templates](../.nexus/collection/templates/)
- [JSON Schemas](../.nexus/collection/schemas/)
- [Privacy Policy](./PRIVACY.md)
- [GDPR Compliance Guide](./GDPR.md)

---

**Version History:**
- v1.0 (2026-02-20): Initial design
