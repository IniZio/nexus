---
description: Pause the boulder enforcement system
agent: build
subtask: true
---

Use the boulder-pause tool to pause the boulder enforcement system.

The tool will:
- Set status to "PAUSED" in .nexus/boulder/state.json
- Set stopRequested to true
- Report the new status

Execute the boulder-pause tool and return the result.
