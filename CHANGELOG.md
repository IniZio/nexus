# Changelog

## Unreleased

### Breaking changes

**Egress schema refactored: destination-scoped policies** (D-PDE-EGRESS)

The `egress.secrets` section no longer accepts `repo:` or `paths:` fields. These have moved to a new top-level `egress.policy` section, keyed by destination host.

- `egress.secrets` entries now only carry `env:` (credential name) and `hosts:` (destination hosts)
- `egress.policy` entries are destination-scoped: each entry targets one host and declares path restrictions via `paths:`
- GitHub hosts **still require** a policy entry (fail-closed guard: create is refused without one)
- Path policies (e.g. `/v4/projects/123/**`) move from secrets to policy entries

Migration:

OLD (before policy section):
```yaml
egress:
  secrets:
    - env: GH_TOKEN
      hosts: [github.com, api.github.com, uploads.github.com]
    - env: API_TOKEN
      hosts: [api.example.com]
```

NEW (required):
```yaml
egress:
  policy:
    - host: github.com
      paths: ["/owner/myrepo/**"]
    - host: api.github.com
      paths: ["/repos/owner/myrepo/**", "/repos/owner/myrepo", "/user"]
    - host: uploads.github.com
      paths: ["/**"]
    - host: api.example.com
      paths: ["/v4/projects/123/**"]
  secrets:
    - env: GH_TOKEN
      hosts: [github.com, api.github.com, uploads.github.com]
    - env: API_TOKEN
      hosts: [api.example.com]
```

Short form (`GH_TOKEN@host1,host2`) remains valid for non-GitHub secrets only (path policy support requires the long form).

**`--no-builtin-gh` flag removed** (D-PDE-02)

The flag was a no-op since the built-in GitHub token auto-injection was removed. It is now rejected as an unknown flag.

Migration: `nexus3 sandbox create --repo X` no longer auto-attaches a GitHub credential. To wire one, add `--secret GH_TOKEN@github.com,api.github.com,uploads.github.com` explicitly — one mechanism for all credential injection.

```sh
# Before (--no-builtin-gh was a deprecated no-op):
nexus3 create p/n --repo owner/repo

# After:
nexus3 create p/n --repo owner/repo --secret GH_TOKEN@github.com,api.github.com,uploads.github.com
```

Sandboxes created without `--repo` receive no GitHub credential (fail-closed default unchanged).
