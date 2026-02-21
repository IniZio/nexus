---
description: Check boulder enforcement status
agent: build
subtask: true
---

Check the current boulder enforcement status.

Execute this shell command:
!`node -e "const fs=require('fs'),p='.nexus/boulder/state.json',s=JSON.parse(fs.readFileSync(p)); console.log('Status:',s.status); console.log('Stop requested:',s.stopRequested); console.log('Iteration:',s.iteration);"`

Report the status.
