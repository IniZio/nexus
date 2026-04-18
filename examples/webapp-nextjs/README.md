# webapp-nextjs

A minimal Next.js workspace example for Nexus.

## What it demonstrates

- Nexus workspace lifecycle hooks (`probe`, `start`)
- Port forwarding: guest `3000` → host `3000`
- Reading `NEXUS_WORKSPACE_*` environment variables inside the app

## Quick start

```bash
# From the repo root — create and enter the workspace
nexus create --project examples/webapp-nextjs
nexus exec <workspace-id> -- npm install

# The workspace start lifecycle command runs automatically:
#   npm run dev  (Next.js dev server on :3000)
```

Open [http://localhost:3000](http://localhost:3000) in your browser. You should see the Nexus Demo page showing the workspace name and ID injected via environment variables.

## Workspace config

`.nexus/workspace.json`:

| Field | Value |
|---|---|
| `lifecycle.probe` | `npm run health-check` (exits 0 — always healthy) |
| `lifecycle.start` | `npm run dev` |
| `ports` | guest 3000 → host 3000 |
