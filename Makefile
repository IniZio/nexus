.PHONY: proto build vet test check-agent-fresh install-agent build-agent docs docs-build

# proto regenerates the Go stubs from proto/nexus3/agent/v1/agent.proto.
# Running this target twice must leave the tree byte-identical (deterministic).
#
# Prerequisites (install once):
#   go install github.com/bufbuild/buf/cmd/buf@latest
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
proto:
	buf generate proto

# MEMORY GUARDRAILS (build + test)
#
# `go test ./...` in this repo is not a normal Go test run. The integration
# suites boot real cloud-hypervisor VMs whose guest RAM is memfd-backed
# (vmMemoryConfig.Shared — see internal/core/driver/cloudhypervisor/client.go:195),
# so it is resident and unswappable, and the builder leases up to
# MaxCacheDiskSlots (8, internal/core/builder/cachedisk.go:58) concurrent builder
# VMs. Combined with -race and one test binary per package at GOMAXPROCS (12)
# parallelism, a full run has repeatedly exhausted host RAM and tripped the
# GLOBAL OOM killer — which does not politely stop the test, it takes out
# dbus/ssh-agent and with them the entire login session, including any coding
# agent attached to it.
#
# Four guards, cheapest first:
#   GOMAXPROCS         the only one that reaches NESTED toolchains. See below.
#   *_P / *_PARALLEL   cap how many packages and tests run at once.
#   choom -n 1000      makes the kernel pick THIS process tree first if memory
#                      does run out, instead of the session infrastructure.
#   systemd-run --scope bounds the whole tree with MemoryHigh/MemoryMax, so a
#                      runaway suite is throttled and then killed inside its own
#                      cgroup and never reaches a global OOM at all.
#
# GOMAXPROCS is load-bearing, not a tuning knob. Several tests shell out to the
# Go toolchain themselves (TestHerdrPluginABI_binaryBoundary runs
# `go build ./cmd/nexus3` into a temp dir), and a nested `go build` is a FRESH
# toolchain whose default -p is GOMAXPROCS — it inherits nothing from the outer
# `go test -p`. On 2026-08-30 `make test GOTEST_P=1 GOTEST_PARALLEL=1` — the
# most conservative setting available — still reached 147 concurrent linkers at
# ~195 MB each and exhausted all 30 GB plus 8 GB of swap. -p bounds test
# PACKAGES; only GOMAXPROCS in the environment reaches what the tests spawn.
#
# ManagedOOMPreference=avoid is NOT optional. user@.service ships
# ManagedOOMMemoryPressure=kill with a 60%/20s limit, and a transient scope
# inherits it. MemoryHigh throttling is precisely what drives cgroup PSI over
# that limit, so without this property the three guards fight each other:
# MemoryHigh generates the pressure, systemd-oomd reads the pressure and kills
# the whole scope, and choom -n 1000 guarantees this tree is the victim chosen.
# Observed 2026-08-30 as `systemd-oomd killed 500 process(es) in this unit`
# roughly 90s into `go test -race ./...`, at a 20G MemoryMax the suite never
# came close to reaching. MemoryMax stays the real bound; oomd must stay out.
#
# Ceilings are sized for a 30G host that is also running several coding-agent
# sessions. Raising GOTEST_MEM_MAX much past 12G leaves the rest of the machine
# nothing and defeats the point of the cap.
#
# Override any of these on the command line when you have headroom, e.g.
#   make test GOMAXPROCS=8 GOTEST_P=6 GOTEST_MEM_MAX=20G
# Narrow a run with GOTEST_ARGS:
#   make test GOTEST_ARGS='-run TestHerdrPluginABI'
GOMAXPROCS      ?= 4
GOBUILD_P       ?= 4
GOTEST_P        ?= 2
GOTEST_PARALLEL ?= 2
GOTEST_MEM_HIGH ?= 8G
GOTEST_MEM_MAX  ?= 10G
GOTEST_ARGS     ?=

# CAPPED runs a command inside a memory-bounded transient scope with a
# maximally-OOM-preferred score, and with GOMAXPROCS exported so nested
# toolchains inherit the cap.
#
# It FAILS CLOSED. An earlier version probed for a usable user manager and fell
# back to an uncapped run when the probe failed. That is exactly backwards: the
# probe only fails when the machine is already thrashing and systemd is slow to
# answer — so the cap silently disappeared at the one moment it was needed, and
# the warning went to stderr where an agent never saw it. On 2026-08-30 that
# fallback let a run reach 28G in session-4.scope (memory.max=max) with swap
# 100% full. If the scope cannot be created now, the run does not start.
define CAPPED
	@set -e; \
	if [ -n "$$NEXUS3_ALLOW_UNCAPPED" ]; then \
		echo "make: NEXUS3_ALLOW_UNCAPPED set — running WITHOUT a memory cap." >&2; \
		exec choom -n 1000 -- env GOMAXPROCS=$(GOMAXPROCS) $(1); \
	fi; \
	exec systemd-run --user --scope -q \
		-p MemoryHigh=$(GOTEST_MEM_HIGH) -p MemoryMax=$(GOTEST_MEM_MAX) \
		-p ManagedOOMPreference=avoid \
		-- choom -n 1000 -- env GOMAXPROCS=$(GOMAXPROCS) $(1)
endef

build:
	$(call CAPPED,go build -p $(GOBUILD_P) ./...)

# install-agent compiles the on-PATH nexus3-agent (CGO_ENABLED=0, static) and
# installs it at NEXUS3_AGENT_INSTALL_DIR/nexus3-agent (default: ~/.local/bin).
#
# WHY THIS MATTERS — the builder trap:
#   `nexus3 create --file` resolves the agent via exec.LookPath("nexus3-agent")
#   and bakes it into the builder VM image. The builder-image cache key is
#   sha256(agentBytes)[:8] (see internal/core/builder/builderimage/image.go:94).
#   Rebuilding only `go build ./cmd/nexus3` leaves the on-PATH agent STALE —
#   new agent code (e.g. boot.json capture) silently never runs. Always run
#   `make install-agent` when agent source changes, not just after CLI changes.
#
# The base-image agent (images/kernel/nexus3-agent, baked into nexus3-agent-base)
# is a SEPARATE binary rebuilt by images/kernel/rebuild-base.sh — see AGENT-REBUILD.md.
NEXUS3_AGENT_INSTALL_DIR ?= $(HOME)/.local/bin

install-agent:
	@mkdir -p $(NEXUS3_AGENT_INSTALL_DIR)
	CGO_ENABLED=0 go build -o $(NEXUS3_AGENT_INSTALL_DIR)/nexus3-agent ./cmd/nexus3-agent
	@echo "OK: nexus3-agent installed → $(NEXUS3_AGENT_INSTALL_DIR)/nexus3-agent"

# build-agent: legacy alias — installs to NEXUS3_AGENT_INSTALL_DIR (same as install-agent).
# Previously wrote to /tmp; use install-agent for new scripts.
build-agent: install-agent

vet:
	go vet -p $(GOBUILD_P) ./...

test:
	$(call CAPPED,go test -race -p $(GOTEST_P) -parallel $(GOTEST_PARALLEL) $(GOTEST_ARGS) ./...)

# docs serves the documentation site locally with live reload.
# docs-build renders it to docs/site/.vitepress/dist (gitignored).
#
# The site is the ONLY part of this repo with a JS toolchain, and it is scoped
# to docs/site so the Go build never sees it. Requires pnpm; first run installs
# VitePress into docs/site/node_modules.
docs:
	@echo "Docs dev server → http://localhost:5180"
	cd docs/site && pnpm install --frozen-lockfile && pnpm run dev

docs-build:
	cd docs/site && pnpm install --frozen-lockfile && pnpm run build

# check-agent-fresh detects stale agent binaries in TWO places:
#
#   Part 1 — base-image agent (images/kernel/nexus3-agent):
#     Embedded into the nexus3-agent-base rootfs image; used by every plain
#     `sandbox create` that does NOT pass --file. A stale base agent fails
#     silently at runtime (e.g. 2026-08-15 egress outage, D-PD-27: pre-dummy0-fix
#     agent left virtio eth0 DOWN with a healthy vsock). See images/AGENT-REBUILD.md.
#     Fix: images/kernel/rebuild-base.sh [--image]
#
#   Part 2 — on-PATH builder agent (resolved via exec.LookPath("nexus3-agent")):
#     Used ONLY by `nexus3 create --file` to bake a custom builder VM image.
#     Cache key = sha256(agentBytes)[:8] (builderimage/image.go:94), so a
#     stale on-PATH agent silently re-bakes old code — new agent features
#     (e.g. boot.json capture) never run even when the CLI is rebuilt.
#     Fix: make install-agent
#
# Rule: if ANY .go file under cmd/nexus3-agent/ or internal/core/agent/ is
# newer than the checked binary, the binary is stale.
check-agent-fresh:
	@bin=images/kernel/nexus3-agent; \
	if [ ! -f $$bin ]; then \
		echo "FAIL: $$bin missing — run images/kernel/rebuild-base.sh"; exit 1; \
	fi; \
	stale=$$(find cmd/nexus3-agent internal/core/agent -name '*.go' -newer $$bin 2>/dev/null | head -1); \
	if [ -n "$$stale" ]; then \
		echo "FAIL: agent source newer than $$bin (first offender: $$stale)"; \
		echo "      the nexus3-agent-base image embeds a stale agent — runtime failures are silent"; \
		echo "      fix: images/kernel/rebuild-base.sh && images/kernel/rebuild-base.sh --image"; \
		exit 1; \
	fi; \
	echo "OK: images/kernel/nexus3-agent is fresher than all agent sources"; \
	path_bin=$$(command -v nexus3-agent 2>/dev/null); \
	if [ -z "$$path_bin" ]; then \
		echo "WARN: nexus3-agent not found in PATH — 'nexus3 create --file' will fail at runtime"; \
		echo "      fix: make install-agent"; \
	else \
		stale2=$$(find cmd/nexus3-agent internal/core/agent -name '*.go' -newer "$$path_bin" 2>/dev/null | head -1); \
		if [ -n "$$stale2" ]; then \
			echo "FAIL: on-PATH builder agent $$path_bin is stale (first offender: $$stale2)"; \
			echo "      builder-image cache key = sha256(agentBytes)[:8]; the stale agent is"; \
			echo "      silently re-baked — new agent features never run even after \`go build ./cmd/nexus3\`"; \
			echo "      fix: make install-agent"; \
			exit 1; \
		fi; \
		echo "OK: $$path_bin is fresher than all agent sources"; \
	fi
