# fullstack-compose

A full-stack workspace example: Next.js + Postgres + Redis, orchestrated via Docker Compose.

## What it demonstrates

- Multi-service workspace lifecycle using `docker-compose up -d`
- Probe using `pg_isready` — workspace reports ready only when Postgres is accepting connections
- Port forwarding: guest `3000` → host `3001`, guest `5432` → host `5432`
- App reads `DATABASE_URL` and displays live Postgres connection status

## Services

| Service | Image | Port |
|---------|-------|------|
| `app` | Next.js (built from `./app`) | 3000 |
| `db` | postgres:16 | 5432 |
| `cache` | redis:7 | 6379 |

## Quick start

```bash
# From the repo root — create the workspace
nexus create --project examples/fullstack-compose

# The workspace start lifecycle command runs automatically:
#   docker-compose up -d
# The probe waits until pg_isready -h localhost succeeds.
```

Open [http://localhost:3001](http://localhost:3001) to see the connection status page.

Connect directly to Postgres from your local machine:

```bash
psql -h localhost -p 5432 -U nexus -d nexus
# password: nexus
```

## Workspace config

`.nexus/workspace.json`:

| Field | Value |
|---|---|
| `lifecycle.probe` | `pg_isready -h localhost` |
| `lifecycle.start` | `docker-compose up -d` |
| `ports` | guest 3000 → host 3001, guest 5432 → host 5432 |
