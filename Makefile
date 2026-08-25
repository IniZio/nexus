.PHONY: proto build vet test check-agent-fresh docs docs-build

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

# build-agent compiles the in-guest nexus3-agent with CGO_ENABLED=0 (static,
# no glibc dependency). The builder VM rootfs is musl/Alpine; a dynamically
# linked agent crashes as PID 1. Use this target for local development builds;
# images/kernel/rebuild-base.sh also sets CGO_ENABLED=0 for image builds.
build-agent:
	CGO_ENABLED=0 go build -o /tmp/nexus3-agent ./cmd/nexus3-agent

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

# check-agent-fresh fails when the in-guest agent source changed but the
# staged agent binary (images/kernel/nexus3-agent, embedded into the
# nexus3-agent-base image) was not rebuilt. A stale agent image fails
# silently at runtime: e.g. the 2026-08-15 egress outage (D-PD-27) where a
# pre-dummy0-fix agent assigned the guest IP to dummy0 and left virtio eth0
# DOWN — dead network with a healthy vsock. See images/AGENT-REBUILD.md.
#
# Rule: if ANY .go file under cmd/nexus3-agent/ or internal/core/agent/ is
# newer than images/kernel/nexus3-agent, the binary is stale. Rebuild with:
#   images/kernel/rebuild-base.sh
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
	echo "OK: images/kernel/nexus3-agent is fresher than all agent sources"
