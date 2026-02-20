# Multiple Parallel Boulders Design

## Problem

Current boulder design tracks **one global state** per project:
- Single iteration counter
- Single lastActivity timestamp
- Single cooldown period

This limits us to **one boulder enforcement at a time**.

## Goal

Support **multiple parallel boulders** like nested user stories:
- Parent boulder (main task)
- Child boulders (subtasks)
- Each with independent iteration tracking
- Each with independent cooldown
- Visual hierarchy in enforcement messages

## Use Cases

### Use Case 1: Nested User Stories
```
Parent: "Implement authentication system"
  ├─ Child 1: "Set up OAuth provider"
  ├─ Child 2: "Create login UI"
  └─ Child 3: "Add session management"
```

Each child can have its own boulder enforcement while parent continues.

### Use Case 2: Parallel Workstreams
```
Workstream A: "Frontend refactoring" (Boulder A)
Workstream B: "Backend optimization" (Boulder B)
Workstream C: "Documentation updates" (Boulder C)
```

### Use Case 3: Hierarchical Tasks
```
Epic: "Build e-commerce platform"
  ├─ Feature: "Shopping cart" 
  │   └─ Boulder tracks cart iteration
  ├─ Feature: "Payment integration"
  │   └─ Boulder tracks payment iteration
  └─ Feature: "Inventory management"
      └─ Boulder tracks inventory iteration
```

## Architecture Changes

### Current (Single Boulder)
```
.nexus/boulder/state.json
{
  "iteration": 139,
  "lastActivity": 1771571092362,
  "lastEnforcement": 1771570934709,
  ...
}
```

### Proposed (Multiple Boulders)
```
.nexus/boulder/
├── state.json (global config)
├── boulders/
│   ├── main.json (parent boulder)
│   ├── oauth-setup.json (child boulder)
│   ├── login-ui.json (child boulder)
│   └── session-mgmt.json (child boulder)
└── active-boulders.json (registry)
```

### State Structure per Boulder
```json
{
  "id": "oauth-setup",
  "name": "Set up OAuth provider",
  "parentId": "main",
  "iteration": 5,
  "lastActivity": 1771571092362,
  "lastEnforcement": 1771570934709,
  "status": "CONTINUOUS",
  "depth": 1,
  "path": "main.oauth-setup"
}
```

## Implementation Plan

### Phase 1: Multi-Boulder State Manager

**New File:** `.opencode/plugins/nexus-enforcer/multi-boulder-state.js`

```javascript
class MultiBoulderStateManager {
  constructor(boulderId, parentId = null) {
    this.boulderId = boulderId;
    this.parentId = parentId;
    this.state = this.loadState();
  }
  
  loadState() {
    const path = `.nexus/boulder/boulders/${this.boulderId}.json`;
    // Load or create new state
  }
  
  getHierarchy() {
    // Get parent chain: main → feature → task
  }
  
  getActiveChildren() {
    // Get all child boulders that are active
  }
}
```

### Phase 2: Hierarchical Enforcement

**Enforcement with Context:**
```javascript
[BOULDER ENFORCEMENT] Main Task → Subtask OAuth
Iteration 5 (Parent: 12)

The boulder never stops at any level.

Parent progress: 12 iterations
This subtask: 5 iterations
Depth: 2
```

### Phase 3: Parallel Detection

**Detect which boulder should enforce:**
- Check ALL active boulders every 5 seconds
- Each independently tracks idle time
- Each has its own cooldown
- Parent boulder only enforces when ALL children complete

### Phase 4: UI Integration

**Visual Hierarchy in Messages:**
```
🪨 Boulder [Parent: 12] → [Child: 5]

Task: Set up OAuth provider
Parent: Implement authentication system

Continue working on this subtask.
Parent is waiting for completion.
```

## Configuration

**opencode.json:**
```json
{
  "plugin": ["./.opencode/plugins/nexus-enforcer.js"],
  "boulder": {
    "mode": "hierarchical",
    "maxDepth": 3,
    "parallelLimit": 5,
    "inheritCooldowns": false
  }
}
```

## Migration Path

1. Keep existing single-boulder as default
2. Add `boulder create <name>` command
3. Add `boulder list` to show active boulders
4. Add `boulder switch <id>` to change context
5. Auto-create child boulders from task decomposition

## Benefits

1. **Parallel Work** - Multiple workstreams with independent enforcement
2. **Granular Tracking** - Each subtask has its own iteration history
3. **Visual Hierarchy** - Clear parent-child relationships
4. **Flexible Organization** - Match team's workflow structure
5. **Better Focus** - Child boulders keep subtasks on track

## Implementation Complexity

| Component | Effort | Priority |
|-----------|--------|----------|
| Multi-state storage | Medium | High |
| Hierarchy tracking | Medium | High |
| Parallel detection | High | Medium |
| UI hierarchy display | Medium | Low |
| Migration tools | Low | Low |

## Next Steps

1. ✅ Verify single boulder works (DONE)
2. Design multi-boulder state schema
3. Implement MultiBoulderStateManager
4. Update plugin to support hierarchy
5. Test with 2-3 parallel boulders
6. Add task decomposition integration

## Questions for Product Owner

1. Should child boulders inherit parent cooldown?
2. Should parent boulder pause while children active?
3. Max depth of hierarchy? (suggest 3)
4. Max parallel boulders? (suggest 5)
5. How to trigger child boulder creation? (auto from todos?)
