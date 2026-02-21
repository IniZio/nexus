# Boulder Control

The boulder control system provides pause/resume functionality for the enforcement system.

## Commands

### Pause Boulder
```bash
./boulder pause [reason]
```
Temporarily disable boulder enforcement.

### Resume Boulder
```bash
./boulder resume
```
Re-enable boulder enforcement.

### Check Status
```bash
./boulder status
```
Show current boulder state.

### Build with Agent
```bash
./boulder build "task description"
```
Run a build task with the build agent.

### Run with Opencode
```bash
./boulder opencode "task description"
```
Run a task using opencode subtask.

## Integration

The boulder control system integrates with:
- Opencode CLI for subtask execution
- Build agent for automated builds
- State management for pause/resume tracking
