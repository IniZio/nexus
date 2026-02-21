---
description: Check boulder enforcement status
agent: build
subtask: true
---

Use the boulder-status tool to check the current boulder enforcement status.

The tool will read .nexus/boulder/state.json and report:
- Current status (PAUSED or CONTINUOUS)
- stopRequested value
- Current iteration
- Session ID

Execute the boulder-status tool and return the result.
