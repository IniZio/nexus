# Auto-Update System PRD

**Status:** Draft  
**Created:** 2026-02-23  
**Component:** CLI / Update  
**Priority:** P2  

---

## 1. Overview

### 1.1 Problem Statement

Users currently must manually check for and install Nexus updates:
- No notification when new versions are available
- Manual download and installation process
- Risk of running outdated versions with security issues
- No rollback mechanism if update fails

### 1.2 Goals

1. **Automatic Checks** - Check for updates on CLI startup (configurable)
2. **One-Command Update** - `nexus update install` to self-update
3. **Secure Distribution** - Signed, verified binaries only
4. **Atomic Updates** - All-or-nothing with automatic rollback
5. **Channel Support** - stable, beta, and nightly channels

### 1.3 Non-Goals

- Background auto-install (security risk)
- Delta/patch updates (complexity vs benefit)
- Windows MSI/EXE installers (single binary only)
- Package manager integration (Homebrew, apt, etc. - separate effort)

---

## 2. Architecture

### 2.1 System Overview

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                       Auto-Update System                                     │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────────────┐     ┌─────────────────────┐     ┌───────────────┐ │
│  │   Nexus CLI         │────▶│   GitHub Releases   │────▶│   Download    │ │
│  │   (client)          │     │   API               │     │   & Verify    │ │
│  │                     │     │                     │     │               │ │
│  │  - Check version    │     │  - Version list     │     │  - HTTPS      │ │
│  │  - Download binary  │     │  - Release notes    │     │  - Checksum   │ │
│  │  - Verify signature │     │  - Signed artifacts │     │  - Signature  │ │
│  │  - Atomic swap      │     │                     │     │  - Atomic     │ │
│  └─────────────────────┘     └─────────────────────┘     └───────────────┘ │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 2.2 Update Flow

```
┌──────────┐     ┌──────────┐     ┌──────────┐     ┌──────────┐     ┌──────────┐
│  Check   │────▶│  Fetch   │────▶│  Verify  │────▶│  Backup  │────▶│ Install  │
│  Version │     │  Binary  │     │  Sig/Hash│     │  Current │     │  New     │
└──────────┘     └──────────┘     └──────────┘     └──────────┘     └──────────┘
                                                                            │
┌──────────┐     ┌──────────┐     ┌──────────┐     ┌──────────┐            │
│   Done   │◀────│  Cleanup │◀────│  Verify  │◀────│   Test   │◀───────────┘
│          │     │  Backup  │     │  Launch  │     │  Binary  │
└──────────┘     └──────────┘     └──────────┘     └──────────┘
                                    │
                                    ▼
                              ┌──────────┐
                              │ Rollback │◀── On any failure
                              │  Backup  │
                              └──────────┘
```

---

## 3. Release Infrastructure

### 3.1 GitHub Releases

**Release Assets:**
```
nexus-v1.2.3-darwin-amd64.tar.gz
nexus-v1.2.3-darwin-amd64.tar.gz.sha256
nexus-v1.2.3-darwin-amd64.tar.gz.sig
nexus-v1.2.3-darwin-arm64.tar.gz
nexus-v1.2.3-linux-amd64.tar.gz
nexus-v1.2.3-linux-arm64.tar.gz
nexus-v1.2.3-windows-amd64.zip
nexus-v1.2.3-windows-amd64.zip.sha256
nexus-v1.2.3-windows-amd64.zip.sig
```

**Release Metadata:**
```json
{
  "tag_name": "v1.2.3",
  "name": "Nexus 1.2.3",
  "body": "## Changelog\n- Fixed SSH connection issue...",
  "published_at": "2026-02-23T10:00:00Z",
  "prerelease": false,
  "assets": [
    {
      "name": "nexus-v1.2.3-darwin-arm64.tar.gz",
      "browser_download_url": "https://github.com/...",
      "size": 15234567
    }
  ]
}
```

### 3.2 Signing Strategy

**Option A: Cosign (Recommended)**
- Uses Sigstore for keyless signing
- Transparent, auditable
- No key management burden

```bash
# Signing during CI
cosign sign-blob \
  --output-signature nexus-v1.2.3-darwin-arm64.tar.gz.sig \
  nexus-v1.2.3-darwin-arm64.tar.gz
```

**Option B: Minisign**
- Simpler than GPG
- Ed25519 signatures
- Static public key embedded in binary

```bash
# Generate keypair
minisign -G

# Sign
minisign -Sm nexus-v1.2.3-darwin-arm64.tar.gz

# Verify (embedded in CLI)
minisign -Vm nexus-v1.2.3-darwin-arm64.tar.gz -P <public_key>
```

**Decision:** Use Minisign for simplicity. Embed public key in binary at build time.

---

## 4. API Specification

### 4.1 Check for Updates

**GitHub Releases API:**
```http
GET https://api.github.com/repos/inizio/nexus/releases/latest

Response:
{
  "tag_name": "v1.2.3",
  "published_at": "2026-02-23T10:00:00Z",
  "body": "...",
  "assets": [...]
}
```

**With Channel (beta/nightly):**
```http
GET https://api.github.com/repos/inizio/nexus/releases
?per_page=10

# Filter client-side by tag pattern:
# stable: v1.2.3 (semver)
# beta: v1.2.3-beta.1
# nightly: v1.2.3-nightly-20260223
```

### 4.2 Version Manifest

Optional: Hosted version manifest for faster checks:

```http
GET https://releases.nexus.dev/manifest.json

Response:
{
  "channels": {
    "stable": {
      "version": "1.2.3",
      "published_at": "2026-02-23T10:00:00Z",
      "url": "https://github.com/.../v1.2.3",
      "release_notes": "## Changelog..."
    },
    "beta": {
      "version": "1.3.0-beta.1",
      "published_at": "2026-02-22T10:00:00Z",
      "url": "https://github.com/.../v1.3.0-beta.1"
    }
  }
}
```

---

## 5. CLI Specification

### 5.1 Update Commands

```bash
# Check for updates
nexus update check
# Output: 
# Current version: 1.2.2
# Latest version: 1.2.3
# Release notes: https://github.com/...
# Run `nexus update install` to update

# Install update
nexus update install [--channel stable|beta|nightly]
# Output:
# Downloading Nexus 1.2.3 for darwin/arm64...
# Verifying signature... ✓
# Verifying checksum... ✓
# Installing... ✓
# Successfully updated to 1.2.3!

# Show update status
nexus update status
# Output:
# Current version: 1.2.2
# Latest version: 1.2.3
# Channel: stable
# Update available: Yes
# Last checked: 2026-02-23 14:30:00

# View release notes
nexus update notes
# Opens or displays release notes for latest version
```

### 5.2 Automatic Checks

**On CLI startup (configurable):**
```yaml
# ~/.nexus/config.yaml
update:
  check_on_startup: true        # Check for updates on startup
  check_interval: 24h          # Minimum time between checks
  channel: stable              # Update channel
  auto_install: false          # Never auto-install (security)
  notify_only: true            # Just show notification
```

**Startup behavior:**
```
$ nexus workspace list
📦 Workspaces:
...

ℹ️  A new version of Nexus is available: 1.2.3
   Current: 1.2.2
   Run `nexus update install` to update.
```

### 5.3 Exit Codes

| Code | Description |
|------|-------------|
| 0 | Success |
| 1 | Network error |
| 2 | Verification failed |
| 3 | Already up to date |
| 4 | Installation failed |
| 5 | Rollback performed |

---

## 6. Implementation Details

### 6.1 Go Implementation

```go
// pkg/update/updater.go
package update

type Updater struct {
    currentVersion string
    platform       string
    arch           string
    channel        string
    githubRepo     string
    httpClient     *http.Client
}

type ReleaseInfo struct {
    Version      string    `json:"version"`
    PublishedAt  time.Time `json:"published_at"`
    DownloadURL  string    `json:"download_url"`
    ChecksumURL  string    `json:"checksum_url"`
    SignatureURL string    `json:"signature_url"`
    ReleaseNotes string    `json:"release_notes"`
}

func (u *Updater) Check(ctx context.Context) (*ReleaseInfo, error) {
    // Fetch latest release from GitHub
    // Compare with currentVersion
    // Return release info if newer
}

func (u *Updater) Install(ctx context.Context, release *ReleaseInfo) error {
    // 1. Download binary to temp location
    // 2. Verify checksum
    // 3. Verify signature
    // 4. Backup current binary
    // 5. Atomic replace
    // 6. Test new binary
    // 7. Cleanup backup
    // 8. On failure: rollback to backup
}

func (u *Updater) Rollback() error {
    // Restore backup binary
}
```

### 6.2 Update Process Steps

```go
func (u *Updater) Install(ctx context.Context, release *ReleaseInfo) error {
    // Step 1: Download
    tempFile := filepath.Join(os.TempDir(), fmt.Sprintf("nexus-update-%d", time.Now().Unix()))
    if err := u.download(ctx, release.DownloadURL, tempFile); err != nil {
        return fmt.Errorf("download failed: %w", err)
    }
    defer os.Remove(tempFile)
    
    // Step 2: Verify checksum
    if err := u.verifyChecksum(tempFile, release.ChecksumURL); err != nil {
        return fmt.Errorf("checksum verification failed: %w", err)
    }
    
    // Step 3: Verify signature
    if err := u.verifySignature(tempFile, release.SignatureURL); err != nil {
        return fmt.Errorf("signature verification failed: %w", err)
    }
    
    // Step 4: Backup current binary
    currentBinary, _ := os.Executable()
    backupPath := currentBinary + ".backup"
    if err := copyFile(currentBinary, backupPath); err != nil {
        return fmt.Errorf("backup failed: %w", err)
    }
    
    // Step 5: Extract and replace
    if err := u.extractAndReplace(tempFile, currentBinary); err != nil {
        // Rollback on failure
        copyFile(backupPath, currentBinary)
        return fmt.Errorf("installation failed (rolled back): %w", err)
    }
    
    // Step 6: Test new binary
    if err := u.testBinary(currentBinary); err != nil {
        // Rollback on failure
        copyFile(backupPath, currentBinary)
        return fmt.Errorf("verification failed (rolled back): %w", err)
    }
    
    // Step 7: Cleanup backup after 24h (async)
    go u.scheduleBackupCleanup(backupPath)
    
    return nil
}
```

### 6.3 Version Comparison

```go
// Semantic version comparison
func shouldUpdate(current, latest string) bool {
    cv, err := semver.Parse(current)
    if err != nil {
        return true // Update if current version is invalid
    }
    
    lv, err := semver.Parse(latest)
    if err != nil {
        return false // Don't update if latest is invalid
    }
    
    return lv.GT(cv)
}
```

---

## 7. Security Model

### 7.1 Threat Model

| Threat | Mitigation |
|--------|------------|
| MITM attack | HTTPS only, certificate pinning optional |
| Malicious binary | Signature verification with embedded public key |
| Corrupted download | SHA256 checksum verification |
| Failed update | Atomic replacement, automatic rollback |
| Privilege escalation | User-level binary only (no root required) |

### 7.2 Verification Chain

```
1. HTTPS connection to GitHub
   ↓
2. Download binary + .sha256 + .sig
   ↓
3. Verify SHA256 checksum
   ↓
4. Verify Ed25519 signature (minisign)
   ↓
5. Check embedded public key matches expected
   ↓
6. Test launch of new binary
   ↓
7. Atomic replace
```

### 7.3 Embedded Public Key

```go
// Embedded at build time via ldflags
var (
    UpdatePublicKey = "RWRpublickeybase64..." // Set by CI
)

func verifySignature(binaryPath, sigPath string) error {
    publicKey, _ := minisign.PublicKeyFromString(UpdatePublicKey)
    signature, _ := minisign.NewSignatureFromFile(sigPath)
    return minisign.Verify(publicKey, binaryPath, signature)
}
```

---

## 8. Configuration

### 8.1 User Configuration

```yaml
# ~/.nexus/config.yaml
update:
  # Check settings
  check_on_startup: true
  check_interval: 24h  # Don't check more often than this
  
  # Channel selection
  channel: stable  # stable | beta | nightly
  
  # Behavior
  notify_only: true  # Never auto-install, just notify
  
  # Advanced (rarely changed)
  github_repo: "inizio/nexus"
  manifest_url: "https://releases.nexus.dev/manifest.json"
```

### 8.2 Build-Time Configuration

```bash
# Makefile
build:
    go build \
        -ldflags "-X github.com/inizio/nexus/pkg/update.PublicKey=$(UPDATE_PUBLIC_KEY) \
                  -X github.com/inizio/nexus/pkg/update.Version=$(VERSION)" \
        -o nexus \
        ./cmd/nexus
```

---

## 9. Implementation Phases

### Phase 1: Version Check (Week 1)

- [ ] Implement `nexus update check`
- [ ] GitHub API client
- [ ] Semantic version comparison
- [ ] Configurable check interval
- [ ] Startup check integration

### Phase 2: Download & Verify (Week 2)

- [ ] Binary download with progress
- [ ] SHA256 checksum verification
- [ ] Minisign signature verification
- [ ] Embedded public key setup

### Phase 3: Installation (Week 3)

- [ ] Current binary backup
- [ ] Atomic replacement
- [ ] New binary verification (test launch)
- [ ] Automatic rollback on failure
- [ ] Backup cleanup scheduling

### Phase 4: Channels (Week 4)

- [ ] Channel selection (stable/beta/nightly)
- [ ] Version manifest support
- [ ] `nexus update status` command
- [ ] Release notes display

### Phase 5: Polish (Week 5)

- [ ] Progress indicators
- [ ] Better error messages
- [ ] Skip update for session
- [ ] E2E tests

---

## 10. CI/CD Integration

### 10.1 Release Workflow

```yaml
# .github/workflows/release.yml
name: Release

on:
  push:
    tags:
      - 'v*'

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      
      - name: Build binaries
        run: make build-all
        
      - name: Generate checksums
        run: |
          for f in dist/*; do
            sha256sum "$f" > "$f.sha256"
          done
          
      - name: Sign binaries
        run: |
          for f in dist/*.tar.gz; do
            minisign -Sm "$f" -s <(echo "$MINISIGN_KEY")
          done
        env:
          MINISIGN_KEY: ${{ secrets.MINISIGN_KEY }}
          
      - name: Create release
        uses: softprops/action-gh-release@v1
        with:
          files: dist/*
```

### 10.2 Signing Key Management

- Private key: GitHub Secret (`MINISIGN_KEY`)
- Public key: Embedded in binary at build time
- Key rotation: New major version with new key

---

## 11. Success Criteria

- [ ] Update check completes in < 3 seconds
- [ ] Update download with progress indicator
- [ ] Signature verification prevents malicious updates
- [ ] Failed update automatically rolls back
- [ ] Works on macOS, Linux, Windows
- [ ] No privileges required (user-level install)
- [ ] Update doesn't interrupt running workspaces

---

## 12. Future Enhancements

- **Delta Updates** - Only download changed parts
- **Background Download** - Download while using CLI
- **Scheduled Updates** - Update during idle time
- **Enterprise Proxy** - Support corporate proxies
- **Offline Updates** - Install from local file

---

**Last Updated:** February 2026
