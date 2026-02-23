# Nexus v1.0.0 Release Checklist

## Pre-Release

- [ ] All tests passing
- [ ] Documentation complete
- [ ] Examples tested
- [ ] CHANGELOG.md updated
- [ ] Version bumped in all packages

## Version Bump

### Nexus CLI (Go)
```bash
cd packages/nexusd
# Update version in version.go or main.go
git commit -m "chore: bump version to v1.0.0"
```

### Package Managers

**Homebrew (macOS/Linux):**
```bash
# Create formula PR to homebrew-core
# Formula: nexus.rb
```

**npm (optional):**
```bash
cd packages/opencode
npm version 1.0.0
npm publish
```

## GitHub Release

1. Create release notes from CHANGELOG
2. Tag: `git tag -a v1.0.0 -m "Nexus v1.0.0"`
3. Push: `git push origin v1.0.0`
4. Create GitHub release with binaries

## Binaries

Build for all platforms:
```bash
# macOS AMD64
GOOS=darwin GOARCH=amd64 go build -o nexus-darwin-amd64

# macOS ARM64
GOOS=darwin GOARCH=arm64 go build -o nexus-darwin-arm64

# Linux AMD64
GOOS=linux GOARCH=amd64 go build -o nexus-linux-amd64

# Linux ARM64
GOOS=linux GOARCH=arm64 go build -o nexus-linux-arm64
```

## Post-Release

- [ ] Update documentation site
- [ ] Announce on social media
- [ ] Update install.sh with new version
- [ ] Monitor for issues
