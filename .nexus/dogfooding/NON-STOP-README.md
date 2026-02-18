# Nexus Non-Stop Dogfooding
## Inspired by oh-my-opencode: Zero Friction, Persistent Execution

### Philosophy

**Don't ask what's next. Just keep iterating.**

Like oh-my-opencode removes friction from AI-assisted coding, we remove friction from dogfooding:
- No approval gates
- No "phases" or "milestones"
- No waiting for permission
- Just continuous improvement

### The Loop

```
┌─────────────────────────────────────────────────────────────┐
│                    INFINITE ITERATION                        │
├─────────────────────────────────────────────────────────────┤
│  1. Check Telemetry → 2. Analyze Friction → 3. Auto-Task  │
│         ↑                                            │      │
│         └──────────── 4. Execute ────────────────────┘      │
│                    (Sleep 60s, repeat)                       │
└─────────────────────────────────────────────────────────────┘
```

### Auto-Generation Rules

The system automatically creates tasks based on friction patterns:

| Friction Pattern | Auto-Generated Task |
|------------------|---------------------|
| SQL NULL errors | Fix telemetry null handling |
| Command missing | Add CLI command |
| Slow performance | Optimize critical path |
| Test failures | Fix broken tests |
| Build errors | Resolve compilation issues |
| User confusion | Improve documentation |

### Usage

```bash
# Start non-stop dogfooding
./.nexus/dogfooding/non-stop-loop.sh

# Or run in background
tmux new-session -d -s nexus-dogfood './.nexus/dogfooding/non-stop-loop.sh'

# Check status
tmux attach -t nexus-dogfood
```

### Friction Logging (Automatic)

Every iteration logs:
- Test results
- Build status
- Performance metrics
- Auto-generated tasks
- Completion status

### No Human Intervention Required

The system:
- ✅ Auto-creates workspaces
- ✅ Auto-generates tasks from friction
- ✅ Auto-runs tests
- ✅ Auto-builds
- ✅ Auto-commits progress
- ✅ Auto-sleeps and repeats

### When to Stop?

**Never.** 

Just like oh-my-opencode doesn't stop until the job is done, dogfooding never stops. The product continuously improves itself.

### Emergency Stop

```bash
Ctrl+C  # Stop loop
tmux kill-session -t nexus-dogfood  # Kill background session
```

### Success Metrics

The loop succeeds when:
- [ ] Friction points decrease over time
- [ ] Auto-generated tasks resolve issues
- [ ] Tests pass consistently
- [ ] Build never fails
- [ ] Telemetry shows improvement trends

### Remember

> "The boulder never stops rolling." - Sisyphus

Keep iterating. Keep improving. Never ask "what's next?" - just execute.
