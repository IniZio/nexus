Feature: Sandbox capabilities — doc-as-spec badge reconciliation
  # This feature file maps three badge states from the product manual to
  # executable scenarios so the Gherkin suite can mechanically reconcile
  # the docs against the Go code.

  # ── Scenario 1: BUILT ────────────────────────────────────────────────────
  # Capability: sandbox list returns an empty array on a fresh store.
  # Badge:      no danger/warning badge → built.
  # Docs ref:   docs/site/cli/sandbox-commands.md (sandbox list section;
  #             no badge means the capability is fully implemented).
  @badge-built
  Scenario: Listing sandboxes on a fresh store returns an empty list
    Given a fresh sandbox store
    When I list all sandboxes
    Then the list is empty

  # ── Scenario 2: BUILT (tip) ──────────────────────────────────────────────
  # Capability: top-level `nexus3 create` verb.
  # Badge:      type="tip" text="built"
  # Docs ref:   docs/site/cli/sandbox-commands.md
  #               ::: tip Both spellings work <Badge type="tip" text="built" />
  # Expected outcome: PASSES — `create` is a registered top-level command.
  #
  # This scenario was @badge-partial until 2026-08-20 and the stale-badge hook
  # caught the transition: the flat verbs shipped, the scenario started
  # passing, and the suite went RED demanding the badge be corrected. That is
  # the mechanism D-PD-72 exists for, doing its job on a real change.
  @badge-built
  Scenario: Target top-level create verb is available
    Given the nexus3 CLI
    When I look up the command "create"
    Then the command is registered

  # ── Scenario 3: NOT BUILT (danger) ───────────────────────────────────────
  # Capability: public agent launch surface (`nexus3 agent launch`).
  # Badge:      type="danger" text="not built"
  # Docs ref:   docs/site/ai-agents.md:96
  #               ### Public agent launch surface
  #               <Badge type="danger" text="not built" />
  # Expected outcome: PENDING — step is defined but returns godog.ErrPending;
  # the suite must NOT turn red.
  @badge-not-built
  Scenario: Public agent launch surface creates a sandbox for the task
    Given the nexus3 CLI
    When an agent requests a launch for task "my-task"
    Then a new sandbox is created for the agent task

  # ── Scenario 4: BUILT ────────────────────────────────────────────────────
  # Capability: D-PD-53 guard — fork and snapshot refuse any sandbox that
  #             carries live host-directory mounts.
  # Badge:      no danger/warning badge → built.
  # Docs ref:   docs/site/cli/sandbox-commands.md (fork / snapshot sections).
  # Expected outcome: PASS — service.Fork and service.Snapshot both return an
  #             error whose message cites "D-PD-53".
  @badge-built
  Scenario: Fork and snapshot refuse a sandbox with live host-directory mounts
    Given a sandbox with a live host-directory mount exists in the store
    When I fork the sandbox
    Then the error cites "D-PD-53"
    When I snapshot the sandbox
    Then the error cites "D-PD-53"

  # ── Scenario 5: BUILT (negative control) ─────────────────────────────────
  # Capability: D-PD-53 guard is not over-broad.
  # Badge:      no danger/warning badge → built.
  # Docs ref:   docs/site/cli/sandbox-commands.md (fork / snapshot sections).
  # Expected outcome: PASS — a sandbox with no live mounts reaches the driver
  #             layer without D-PD-53 refusal.  The fake driver then fails for
  #             an unrelated reason (ErrNoSubstrate) which does not cite D-PD-53.
  @badge-built
  Scenario: Fork and snapshot do not cite D-PD-53 for a sandbox with no live mounts
    Given a sandbox with no live mounts exists in the store
    When I fork the sandbox
    Then the error does not cite "D-PD-53"
    When I snapshot the sandbox
    Then the error does not cite "D-PD-53"
