---
description: Resume the boulder enforcement system
agent: build
subtask: true
---

Resume the boulder enforcement system.

Execute this shell command:
!`node -e "const fs=require('fs'),p='.nexus/boulder/state.json',s=JSON.parse(fs.readFileSync(p)); s.status='CONTINUOUS'; s.stopRequested=false; fs.writeFileSync(p,JSON.stringify(s,null,2)); console.log('✅ Boulder resumed')"`

Report the result.
