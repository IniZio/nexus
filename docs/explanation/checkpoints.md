# Workspace Checkpoints

Checkpoints capture container state (installed packages, filesystem) for later restoration.

## What is Preserved

- Installed packages (apt-get install, npm install, pip install, etc.)
- Files created in container (/workspace/* except bind mounts)
- Container configuration and image layers

## What is NOT Preserved

- Worktree changes (bind-mounted from host - these exist on the host, not in container)
- Environment variables set at runtime
- Running processes

## How Checkpoints Work

Checkpoints use Docker commit to create a new image from the container's current state.
When you restore a checkpoint, a new container is created from that image.

```
Container → docker commit → Checkpoint Image → docker run (new container)
```

## Usage

```bash
# Create a checkpoint
nexus checkpoint create myapp baseline

# List checkpoints
nexus checkpoint list myapp

# Restore a checkpoint
nexus checkpoint restore myapp checkpoint-id

# Delete a checkpoint
nexus checkpoint delete myapp checkpoint-id
```

## Persistence

Checkpoints are stored in the daemon's state directory:
```
<state-dir>/<workspace-id>/checkpoints/<checkpoint-id>.json
```

The checkpoint image is stored in Docker's local image registry.
