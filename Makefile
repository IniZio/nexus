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

build:
	go build ./...

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
	go vet ./...

test:
	go test -race ./...

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
