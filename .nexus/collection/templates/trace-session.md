# Trace Session Report

**Session ID:** `session-[YYYYMMDD]-[HHMMSS]`
**Start Time:** YYYY-MM-DD HH:MM:SS UTC
**End Time:** YYYY-MM-DD HH:MM:SS UTC
**Duration:** [X hours Y minutes]
**Recorder:** [Name]

---

## Environment

| Field | Value |
|-------|-------|
| OS | e.g., Ubuntu 22.04 |
| Shell | e.g., zsh 5.9 |
| Node Version | e.g., v20.10.0 |
| Nexus Version | e.g., v1.2.3 |
| Working Directory | `/path/to/project`

---

## Session Summary

[Brief 2-3 sentence summary of what was being tested or what happened during this session.]

---

## Commands Executed

| # | Command | Timestamp | Duration | Result |
|---|---------|-----------|----------|--------|
| 1 | `nexus analyze` | HH:MM:SS | 2.3s | Success |
| 2 | `nexus watch --dir ./src` | HH:MM:SS | -- | Running |
| ... | | | | |

---

## Detailed Output

### Command 1: `nexus analyze`

**Output:**
```bash
$ nexus analyze
[Full command output here]
```

**Errors/Warnings:**
```
[Any errors or warnings]
```

### Command 2: `nexus watch --dir ./src`

**Initial Output:**
```bash
$ nexus watch --dir ./src
[Full command output here]
```

**File Changes Detected:**

| Time | File | Change Type |
|------|------|-------------|
| HH:MM:SS | `src/index.js` | Modified |
| HH:MM:SS | `src/utils.js` | Created |

---

## Observations

### What Worked Well

- [Observation 1]
- [Observation 2]

### Friction Points

- [Friction 1]
- [Friction 2]

### Unexpected Behaviors

1. [Behavior description and impact]
2. [Behavior description and impact]

---

## Performance Metrics

| Operation | Duration | Memory |
|-----------|----------|--------|
| Initial analysis | 2.3s | 245 MB |
| Change detection | < 1s | 260 MB |
| Hot reload | 0.8s | 270 MB |

---

## Session Notes

[Additional notes, thoughts, or follow-up items.]

---

## Attachments

- [Link to screenshots if any]
- [Link to log files]
- [Link to relevant documentation]

---

*Generated from Nexus Friction Collection System*
