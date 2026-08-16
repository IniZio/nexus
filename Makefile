.PHONY: proto build vet test check-agent-fresh

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

vet:
	go vet ./...

test:
	go test -race ./...

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
