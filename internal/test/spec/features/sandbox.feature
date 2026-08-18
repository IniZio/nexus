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

  # ── Scenario 2: PARTIAL (warning) ────────────────────────────────────────
  # Capability: top-level `nexus3 create` verb (target design).
  # Badge:      type="warning" text="partial"
  # Docs ref:   docs/site/ai-agents.md:74
  #               <Badge type="warning" text="partial" /> — current
  #               implementation uses `nexus3 sandbox create`; see CLI
  #               sandbox commands for the mapping.
  # Expected outcome: FAILS — `create` is not a registered top-level
  # command; the partial badge is correct and visible in output.
  @badge-partial
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
