# Debugging Ports

## Common Issues

### Port Already in Use

If you see an error like `port 3000 already in use`:

```bash
# Check what's using the port
lsof -i :3000

# Or use netstat
netstat -tulpn | grep :3000

# Check Nexus workspaces
nexus workspace list
```

### SSH Port Not Accessible

If SSH connection fails:

```bash
# Verify workspace is running
nexus workspace list

# Check container status
docker ps | grep nexus

# Get correct port
nexus workspace ports <workspace-name>
```

### Service Not Responding

If a service (web, api, postgres) is not responding:

```bash
# SSH into workspace
ssh -p <port> -i ~/.ssh/id_ed25519_nexus dev@localhost

# Check services inside container
docker exec -it nexus-<workspace-name> ps aux

# Check logs
docker logs nexus-<workspace-name>
```

## Port Commands

```bash
# List all workspaces with ports
nexus workspace list

# Get ports for specific workspace
nexus workspace ports <workspace-name>

# Check if port is in use
nexus workspace ports <workspace-name> --check 3000
```

## Port Reference

| Service | Internal Port | Default Range |
|---------|--------------|---------------|
| SSH | 22 | 32768+ |
| Web | 3000 | 32778+ |
| API | 5000 | 32779+ |
| PostgreSQL | 5432 | 32780+ |

## Reset Port Allocation

If port allocation gets corrupted:

```bash
nexus workspace down <workspace-name>
docker network prune
nexus workspace up <workspace-name>
```
