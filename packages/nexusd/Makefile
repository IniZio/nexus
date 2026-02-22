.PHONY: all build build-daemon build-cli clean test

all: build

build: build-daemon build-cli

build-daemon:
	go build -o nexusd ./cmd/daemon

build-cli:
	go build -o nexus ./cmd/cli

clean:
	rm -f nexus nexusd

test:
	go test ./...

run: build-daemon
	./nexusd --token=dev-token --port=8080

install: build
	cp nexus /usr/local/bin/
	cp nexusd /usr/local/bin/
