---
name: advisor-strategy
description: Supplemental decision-quality overlay for executor workflows. Pairs normal task skills with strict advisor checkpoints at hard decisions. Use alongside brainstorming, planning, debugging, or implementation skills when architecture trade-offs, ambiguity, high-risk operations, or potential rework are present.
---

# Advisor Strategy

If you think there is even a 1% chance a skill might apply to what you are doing, you ABSOLUTELY MUST invoke the skill.

IF A SKILL APPLIES TO YOUR TASK, YOU DO NOT HAVE A CHOICE. YOU MUST USE IT.

## Purpose

Use an executor-first loop. Escalate only decision-heavy moments to an advisor, then continue execution with the advisor's guidance.

## Composition Rule (Supplemental)

This skill is a supplement, not a replacement.

1. Select and run the primary workflow skill(s) for the task (for example, brainstorming or writing-plans).
2. Layer advisor-strategy on top as decision checkpoints during that workflow.
3. Do not reject advisor-strategy just because another skill is a better primary fit.
4. For architecture/design requests, run primary design workflow first, then invoke `advisor` before locking major decisions.

## Non-Negotiable Rules

1. Keep one executor accountable for end-to-end progress.
2. Advisor gives guidance only: plan, correction, or stop signal.
3. Advisor does not own user-facing output and does not run task tools directly.
4. At escalation checkpoints, invoke the `advisor` subagent (project/user custom agent) for guidance.
5. Escalate only when the executor cannot confidently choose a safe next move.
6. Record each escalation reason and the chosen follow-up action.
7. Never treat "another skill applies" as a reason to skip advisor checkpoints when risk/ambiguity exists.

## Workflow

1. Start in executor mode and attempt the task normally.
2. At each checkpoint, ask: "Can I proceed confidently without escalation?"
3. If yes, continue execution.
4. If no, invoke the `advisor` subagent with concise context:
   - Goal and current status
   - Key constraints
   - Failed/considered options
   - Exact decision to make
5. Accept one of three advisor responses:
   - Plan: concrete next steps
   - Correction: course change
   - Stop: halt and surface blocker
6. Resume executor mode, implement, and verify outcomes.
7. Repeat until task completion.

## Escalation Checkpoints

Escalate when any of these are true:

- Architecture trade-off with high downstream cost
- Repeated failure after two materially different attempts
- Ambiguous requirements with multiple plausible interpretations
- Security, data-loss, migration, or destructive-operation risk
- Performance bottleneck where root cause is uncertain

Do not escalate for routine edits, straightforward refactors, or mechanical changes.

### 1% Chance Heuristic

- If there is even a 1% chance the current decision is high-impact, irreversible, ambiguous, or likely to cause rework, consult `advisor`.
- When in doubt, escalate once early instead of recovering from a bad branch of execution later.
- If not escalating, the executor should be able to state a concrete reason that risk is negligible.

## Response Contract (Claude-Style)

When the skill is active, structure responses in this order:

1. Objective and current state
2. 1-2 key clarifying questions (only if blocking)
3. Options with trade-offs
4. Recommendation
5. Next action

Keep responses concise and actionable.

## Implementation Notes

- For API agents: use the advisor tool (`advisor_20260301`) with bounded `max_uses`.
- For Cursor-style environments: delegate to the `advisor` subagent at each justified escalation checkpoint.
- Fallback only if `advisor` is unavailable: use a clearly labeled "simulated advisor checkpoint" and state why fallback was required.
- Track escalation count and avoid uncontrolled loops.

## Validation Checklist

- [ ] Executor remained primary actor end-to-end
- [ ] Escalations happened only at justified checkpoints
- [ ] Each escalation invoked `advisor` (or documented fallback reason)
- [ ] Advisor output was guidance, not execution
- [ ] Final recommendation and next action are explicit
- [ ] Any stop signal was clearly surfaced to the user

## Additional Resources

- See `reference.md` for templates and examples.
