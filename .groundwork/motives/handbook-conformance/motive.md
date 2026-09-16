# motive: handbook-conformance

## Objective

Bring nexus3 into line with the Oursky handbook `dev-project-setup` and `dev-development` rules that apply to a Go CLI/daemon, closing the gaps identified in the 2026-09-16 audit.

## Notes

### Audit scope — 2026-09-16

Handbook sections consulted: `dev-project-setup` (make-targets.md, engineering-project-checklist.md), `dev-development` (github-actions.md). Rules that concern languages or stacks not present in this repo (Node, Python, mobile) were skipped.

### Passed items (baselines)

These items already satisfy the handbook and are recorded here so a future audit can confirm they have not regressed:

- Conventional commits enforced (semantic-release config present, release pipeline proven).
- CI runs on push and pull request.
- Unit tests extensive and passing under `make test`.

### CI workflows note

`.github/workflows/ci.yml` currently runs `go build ./...` and `go test ./...` directly. This bypasses the memory-cap guards documented in CLAUDE.md (`choom -n 1000`, `systemd-run` cgroup scope, capped package/test parallelism). The handbook violation (`github-actions.md:103-104`) and the internal safety violation are the same underlying issue; fixing the CI to call `make build` and `make test` closes both at once.

### Verification 2026-09-16

`TMPDIR=/tmp make ci` exit code: **2** (lint failed; audit passed, format and test did not run).

`make format` (run separately): exit 2 — `internal/clientagent/startup.go` (committed at 3ec9683, pre-baseline; pre-existing).

`docker build -t nexus3-dev .` exit code: **0**.

`actionlint .github/workflows/ci.yml` (via `go run`): exit **0**.

Probe file `internal/cli/zz_lintprobe_scratch.go`: **absent** (OK).

#### Lint attribution (76 findings, new-from-rev 47884b2)

| file:line | linter | commit | status |
|---|---|---|---|
| cmd/nexus3-agent/sample_vmstat.go:53 | errcheck | ee50de0 | committed |
| cmd/nexus3-agent/sample_vmstat.go:94 | errcheck | d6d2226 | committed |
| cmd/nexus3-agent/sample_vmstat_test.go:128 | gocognit | ee50de0 | committed |
| internal/cli/cmd_config_test.go:35 | staticcheck | f295012 | committed |
| internal/cli/cmd_herdr_default_shell_chain_test.go:304 | gocognit | 629ba9a | committed |
| internal/cli/cmd_herdr_default_shell_chain_test.go:30 | errcheck | 629ba9a | committed |
| internal/cli/cmd_herdr_default_shell_gate_test.go:135 | errcheck | 0b7a4ef | committed |
| internal/cli/cmd_herdr_default_shell_gate_test.go:185 | errcheck | 0b7a4ef | committed |
| internal/cli/cmd_herdr_default_shell_gate_test.go:197 | gocognit | 0b7a4ef | committed |
| internal/cli/cmd_herdr_default_shell_gate_test.go:93 | gocognit | 0b7a4ef | committed |
| internal/cli/cmd_herdr_default_shell.go:162 | gocognit | 629ba9a | committed |
| internal/cli/cmd_herdr_default_shell.go:307 | gocognit | 0b7a4ef | committed |
| internal/cli/cmd_herdr_default_shell.go:314 | errcheck | 0b7a4ef | committed |
| internal/cli/cmd_herdr_default_shell.go:358 | errcheck | 0b7a4ef | committed |
| internal/cli/cmd_herdr_default_shell.go:798 | gocognit | 629ba9a | committed |
| internal/cli/cmd_herdr_default_shell.go:901 | errcheck | 629ba9a | committed |
| internal/cli/cmd_herdr_default_shell.go:914 | errcheck | 709f5d5 | committed |
| internal/cli/cmd_herdr_default_shell.go:915 | errcheck | 709f5d5 | committed |
| internal/cli/cmd_herdr_default_shell.go:921 | errcheck | 709f5d5 | committed |
| internal/cli/cmd_herdr_default_shell.go:923 | errcheck | 709f5d5 | committed |
| internal/cli/cmd_herdr_default_shell.go:924 | errcheck | 709f5d5 | committed |
| internal/cli/cmd_herdr_default_shell.go:960 | gocognit | 709f5d5 | committed |
| internal/cli/cmd_herdr_default_shell_writeconfig_test.go:161 | gocognit | 709f5d5 | committed |
| internal/cli/cmd_herdr_default_shell_writeconfig_test.go:22 | gocognit | 709f5d5 | committed |
| internal/cli/cmd_herdr_plugin_egress_test.go:431 | gocognit | 854bb02 | committed |
| internal/cli/cmd_herdr_plugin.go:1896 | gocognit | 6179cd4 | committed |
| internal/cli/cmd_herdr_plugin.go:4027 | errcheck | 5536797 | committed |
| internal/cli/cmd_herdr_plugin.go:938 | errcheck | c56270a | committed |
| internal/cli/cmd_herdr_plugin.go:941 | errcheck | c56270a | committed |
| internal/cli/cmd_herdr_plugin.go:944 | errcheck | c56270a | committed |
| internal/cli/cmd_herdr_plugin_pluginmount_test.go:9 | gocognit | 5536797 | committed |
| internal/cli/cmd_herdr_version_check.go:106 | errcheck | c56270a | committed |
| internal/cli/cmd_herdr_version_check.go:114 | errcheck | c56270a | committed |
| internal/cli/cmd_herdr_version_check.go:123 | errcheck | c56270a | committed |
| internal/cli/cmd_herdr_version_check.go:94 | gocognit | c56270a | committed |
| internal/cli/cmd_kernel_install.go:103 | errcheck | — | untracked |
| internal/cli/cmd_kernel_install.go:107 | errcheck | — | untracked |
| internal/cli/cmd_kernel_install.go:112 | errcheck | — | untracked |
| internal/cli/cmd_kernel_install.go:116 | errcheck | — | untracked |
| internal/cli/cmd_kernel_install.go:125 | errcheck | — | untracked |
| internal/cli/cmd_kernel_install.go:148 | errcheck | — | untracked |
| internal/cli/cmd_kernel_install.go:24 | gocognit | — | untracked |
| internal/cli/cmd_kernel_install.go:60 | errcheck | — | untracked |
| internal/cli/cmd_kernel_install.go:79 | errcheck | — | untracked |
| internal/cli/cmd_kernel_install.go:97 | errcheck | — | untracked |
| internal/cli/cmd_kernel_install_test.go:16 | gocognit | — | untracked |
| internal/cli/cmd_kernel_install_test.go:91 | errcheck | — | untracked |
| internal/cli/cmd_kernel_install_test.go:98 | errcheck | — | untracked |
| internal/cli/cmd_sandbox_diskwarn_test.go:99 | gocognit | 3cb360c | committed |
| internal/cli/cmd_sandbox.go:171 | errcheck | 3cb360c | committed |
| internal/cli/cmd_sandbox.go:184 | errcheck | 3cb360c | committed |
| internal/core/agent/exec.go:154 | errcheck | 6d83306 | committed |
| internal/core/agent/exec_test.go:102 | errcheck | 6d83306 | committed |
| internal/core/agent/exec_test.go:43 | errcheck | 445f48e | committed |
| internal/core/builder/recipelayer_test.go:135 | gocognit | 105849c | committed |
| internal/core/config/config_test.go:923 | gocognit | 342992a | committed |
| internal/core/perimeter/mitm/config_policy_carveout_test.go:137 | errcheck | baf4488 | committed |
| internal/core/perimeter/mitm/config_policy_carveout_test.go:53 | gocognit | baf4488 | committed |
| internal/core/portfwd/statefile.go:104 | gocognit | e91d85d | committed |
| internal/core/service/create_test.go:859 | gocognit | 0000000 | uncommitted |
| internal/core/service/create_test.go:882 | errcheck | 0000000 | uncommitted |
| internal/core/service/create_test.go:885 | errcheck | 0000000 | uncommitted |
| internal/core/service/create_test.go:886 | errcheck | 0000000 | uncommitted |
| internal/core/service/create_test.go:891 | errcheck | 0000000 | uncommitted |
| internal/core/service/service.go:996 | staticcheck | 74b73d4 | committed |
| internal/core/service/usermount_plugins.go:19 | gocognit | 5536797 | committed |
| internal/core/service/usermount_plugins_test.go:10 | gocognit | 5536797 | committed |
| internal/core/volumestore/store_test.go:346 | gocognit | 3a9bd6f | committed |
| internal/mcp/delegate.go:147 | gocognit | d75ca58 | committed |
| internal/mcp/delegate.go:208 | gocognit | d75ca58 | committed |
| internal/mcp/delegate.go:235 | gocognit | ff78205 | committed |
| internal/mcp/delegate_test.go:124 | gocognit | d75ca58 | committed |
| internal/mcp/delegate_test.go:209 | gocognit | d75ca58 | committed |
| internal/mcp/delegate_test.go:244 | gocognit | d75ca58 | committed |
| internal/mcp/delegate_test.go:287 | gocognit | d75ca58 | committed |
| internal/supervisor/portfwd_test.go:197 | errcheck | 311ee13 | committed |
| internal/supervisor/portfwd_test.go:221 | gocognit | 311ee13 | committed |
| internal/supervisor/portfwd_test.go:227 | errcheck | 311ee13 | committed |
| internal/supervisor/portfwd_test.go:262 | errcheck | 311ee13 | committed |
| internal/supervisor/portfwd_test.go:404 | errcheck | f4a1e6e | committed |

## Open items

- TBD-1: Make targets `secret`, `setup`, `audit`, `lint`, `format`, and `ci` are absent.
    The handbook (`make-targets.md:15`) specifies a standard set of targets every project must provide. `lint` requires golangci-lint with a `gocognit` threshold of 10 and a `.golangci.yml` config file; `format` requires a `.editorconfig`; `ci` must chain audit+lint+format+test. None of these exist in the current Makefile. Their absence means contributors cannot run the full quality gate locally, and CI cannot call a single stable target.
    refs: handbook:make-targets.md:15

- TBD-2: CI workflow calls `go build ./...` and `go test ./...` instead of `make build` and `make test`.
    Direct `go` invocations bypass the cgroup memory cap and parallelism guards CLAUDE.md documents as mandatory for safety on this host. They also violate `github-actions.md:103-104`. The fix is a one-line substitution per step, but it depends on TBD-1 landing first (so `make ci` exists as the canonical target CI should call).
    refs: handbook:github-actions.md:103-104, CLAUDE.md, .github/workflows/ci.yml:25,:31

- TBD-3: CI workflow has no `concurrency` group to cancel superseded runs.
    `github-actions.md:95-101` requires a `concurrency:` block so that a new push to the same branch cancels the prior run. Without it, a fast sequence of pushes queues redundant CI runs that waste runner time and delay feedback.
    refs: handbook:github-actions.md:95-101, .github/workflows/ci.yml

- TBD-4: CI runners are `ubuntu-latest`; confirm whether `[self-hosted, linux, x64, v1]` policy applies.
    `github-actions.md:12-20` specifies self-hosted runners. nexus3 is a KVM-level tool whose integration tests require `/dev/kvm`; a stock GitHub-hosted runner cannot run them. Flag for operator decision: if self-hosted runners are available and the policy applies, `ubuntu-latest` must be replaced. If the policy does not apply to this repo (e.g. integration tests are manually gated), record that as a deliberate exception.
    refs: handbook:github-actions.md:12-20, .github/workflows/ci.yml

- TBD-5: No root `Dockerfile` for local development environment.
    `engineering-project-checklist.md:29` requires a Dockerfile that lets a new contributor reproduce the full development environment without installing host dependencies manually. nexus3's dev loop requires KVM access, `cloud-hypervisor`, `make`, and `gcc`; none of these are documented as a container. The Dockerfile would either document them (even if KVM pass-through is noted as a prerequisite) or provide a container image that carries the non-KVM deps.
    refs: handbook:engineering-project-checklist.md:29

- TBD-6: No C4 architecture diagram.
    `engineering-project-checklist.md:28` requires a C4 context or container diagram. nexus3 has no committed architecture diagram; the nearest equivalent is the wayfinder memory note and inline comments in key files. A C4 diagram would make the host-daemon / supervisor / guest-agent layering legible to a new contributor without reading the code.
    refs: handbook:engineering-project-checklist.md:28

- TBD-7: README is in progress (housekeep S5) — track completion.
    `engineering-project-checklist.md:51` requires a README. The README was being written in a parallel housekeep slice (S5, 2026-09-16); this item tracks confirmation that it lands and covers the minimum required content (what the project is, how to build, how to run tests, where to find further docs).
    refs: handbook:engineering-project-checklist.md:51

- TBD-8: CONTRIBUTING guide does not document Tier 2/3 test prerequisites.
    Integration and end-to-end tests in this repo require `/dev/kvm` and a running `herdr` instance. A contributor who runs `make test` without these will hit confusing failures. CONTRIBUTING should state the prerequisite hardware and tooling clearly, and describe which test tiers are runnable on a standard laptop vs. a KVM host.
    refs: handbook:engineering-project-checklist.md, CLAUDE.md

## Decision Log

### D-1: All handbook gaps identified in the 2026-09-16 audit are deferred out of the housekeep pass and into this motive.

**Status:** [accepted]

**Rationale:** The housekeep pass is deletion-first; adding new make targets, CI config changes, a Dockerfile, and a C4 diagram is additive scope that would expand the pass beyond its mandate and risk stalling it. Each gap is independently deliverable; deferring them into a dedicated motive keeps their progress visible without blocking housekeep from closing.

## Baselines

| Item | Status | Date |
|---|---|---|
| Conventional commits enforced | PASS | 2026-09-16 |
| CI on push + pull request | PASS | 2026-09-16 |
| Unit tests (make test) | PASS | 2026-09-16 |
| `make build`, `make test`, `make vet` | PASS | 2026-09-16 |
| `make secret` | PRESENT | 2026-09-16 |
| `make setup` | PRESENT — green | 2026-09-16 |
| `make audit` | PRESENT — green (0 vulnerabilities) | 2026-09-16 |
| `make lint` (.golangci.yml, gocognit) | PRESENT — red on 76 new findings | 2026-09-16 |
| `make format` (.editorconfig) | PRESENT — red on 1 file (pre-baseline) | 2026-09-16 |
| `make ci` | PRESENT — red (lint failure, exit 2) | 2026-09-16 |
| `make deploy` | ABSENT | 2026-09-16 |
| CI uses make targets | PASS | 2026-09-16 |
| CI concurrency group | PASS | 2026-09-16 |
| CI self-hosted runners | EXCEPTION (D-2) | 2026-09-16 |
| Root Dockerfile for dev env | PRESENT — exit 0 | 2026-09-16 |
| C4 architecture diagram | PRESENT (docs/architecture/README.md) | 2026-09-16 |
| README | PASS (80d4b4d) | 2026-09-16 |
| CONTRIBUTING with KVM prereqs | PASS (7f73f90) | 2026-09-16 |
| Go toolchain 1.26 (D-5) | PASS | 2026-09-16 |
| go.mod containerd v1 removed (govulncheck 0) | PASS | 2026-09-16 |

## Tickets

See [MAP.md](MAP.md) for the live ticket index.

## Out of scope

- Handbook rules for stacks not present in this repo (Node, Python, mobile, frontend).
- Re-litigating which handbook rules apply; this motive accepts the audit findings and focuses on delivery.
- Handbook items already passing — they are baselined above and not tracked as open items.
