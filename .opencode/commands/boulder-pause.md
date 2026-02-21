---
description: Pause the boulder enforcement system
agent: build
subtask: true
---

Pause the boulder enforcement system.

Execute this shell command:
!`node -e "const fs=require('fs'),p='.nexus/boulder/state.json',s=JSON.parse(fs.readFileSync(p)); s.status='PAUSED'; s.stopRequested=true; fs.writeFileSync(p,JSON.stringify(s,null,2)); console.log('✅ Boulder paused')"`

Report the result.
