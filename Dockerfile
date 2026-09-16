FROM golang:1.26-bookworm

RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential \
        git \
        python3 \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

RUN go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest \
 && go install golang.org/x/vuln/cmd/govulncheck@latest

WORKDIR /src

LABEL dev.nexus3.no-kvm-targets="make build vet test lint format audit"
LABEL dev.nexus3.kvm-targets="make test-integration -- requires: --device /dev/kvm; bind-mount cloud-hypervisor at /usr/local/bin/cloud-hypervisor"

CMD ["make", "vet"]
