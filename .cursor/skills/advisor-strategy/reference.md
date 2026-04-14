# Advisor Strategy Reference

## 1% Chance Escalation Rule

Use this decision rule before committing to a risky path:

- If there is even a 1% chance that the next decision is high-impact, ambiguous, or hard to reverse, invoke `advisor`.
- Prefer one early advisor checkpoint over late-stage rollback or rework.
- If skipping advisor, document why risk is negligible.

## Subagent Invocation Record (Required)

For each escalation checkpoint, append this record:

```markdown
## Advisor Invocation Record
Timestamp: <YYYY-MM-DD_HH:MM:SS>
Trigger: <checkpoint category>
Why now: <what created uncertainty/risk>
Invocation target: advisor
Fallback used: yes|no
Fallback reason (if yes): <why advisor was unavailable>
Decision requested: <single concrete question>
Advisor result type: PLAN | CORRECTION | STOP
Executor follow-up: <what was executed next>
```

## Advisor Request Template

Use this payload when escalating:

```markdown
## Advisor Request
Goal: <target outcome>
Current status: <what has been tried>
Constraints: <time/risk/perf/security requirements>
Options considered: <A/B/... and why unresolved>
Decision needed: <single concrete question>
```

## Advisor Response Template

Expected advisor response:

```markdown
## Advisor Guidance
Type: PLAN | CORRECTION | STOP
Rationale: <short reason>
Actions:
1. <step one>
2. <step two>
Risks to watch:
- <risk>
```

## Executor Update Template

After receiving guidance:

```markdown
## Executor Update
Chosen path: <what will be executed>
Why: <why this guidance was selected>
Immediate next action: <first concrete step>
Verification: <how success will be checked>
```

## Example: Architecture Decision

1. Executor hits uncertainty choosing cache strategy.
2. Escalates with constraints (latency target, memory cap, invalidation complexity).
3. Advisor returns PLAN: start in-memory with interface boundary and eviction metrics.
4. Executor implements boundary + metrics, validates latency, reports outcome.

## Example: Stop Signal

1. Executor detects destructive migration risk without rollback.
2. Escalates for decision.
3. Advisor returns STOP with requirement: define rollback and data backup procedure first.
4. Executor halts execution and asks user for approval/constraints.
