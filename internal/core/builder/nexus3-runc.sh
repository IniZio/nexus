#!/bin/sh
# nexus3-runc: OCI runtime shim baked into every `--file` guest image at
# /usr/local/sbin/runc, ahead of the distro runc on PATH. Every container that
# dockerd/containerd/BuildKit creates in the guest (docker run, compose, and
# `docker build` RUN steps) gets the sandbox perimeter CA bundle bind-mounted
# read-only at /etc/nexus3/ca plus the common TLS env vars pointing at it, so
# TLS-intercepted hosts verify without any Dockerfile change. Inert until the
# supervisor has seeded the MITM CA (NEXUS3_CA_CRT). It never touches
# package-owned paths inside the image, so `apt-get install ca-certificates`
# and friends keep working.
set -u
RUNC="${NEXUS3_RUNC:-}"
if [ -z "$RUNC" ]; then
	for p in /usr/sbin/runc /usr/bin/runc /usr/local/bin/runc; do
		[ -x "$p" ] && RUNC="$p" && break
	done
fi
[ -n "$RUNC" ] || RUNC=runc

CA_CRT="${NEXUS3_CA_CRT:-/usr/local/share/ca-certificates/nexus3-mitm.crt}"
SYS_BUNDLE="${NEXUS3_SYS_BUNDLE:-/etc/ssl/certs/ca-certificates.crt}"
TRUST_DIR="${NEXUS3_TRUST_DIR:-/etc/nexus3/docker-trust}"
CA=/etc/nexus3/ca/ca-certificates.crt

bundle=""
cmd=""
prev=""
for a in "$@"; do
	case "$prev" in
	--bundle | -b) bundle="$a" ;;
	esac
	case "$a" in
	--bundle=*) bundle="${a#--bundle=}" ;;
	create | run) [ -z "$cmd" ] && cmd="$a" ;;
	esac
	prev="$a"
done

prepare_trust_dir() {
	mkdir -p "$TRUST_DIR" || return 1
	if [ ! -f "$TRUST_DIR/ca-certificates.crt" ] || [ "$SYS_BUNDLE" -nt "$TRUST_DIR/ca-certificates.crt" ]; then
		cp -f "$SYS_BUNDLE" "$TRUST_DIR/ca-certificates.crt.tmp" && mv -f "$TRUST_DIR/ca-certificates.crt.tmp" "$TRUST_DIR/ca-certificates.crt" || return 1
	fi
	[ -f "$TRUST_DIR/wgetrc" ] || printf 'ca_certificate=%s\n' "$CA" >"$TRUST_DIR/wgetrc"
	[ -f "$TRUST_DIR/apt.conf" ] || printf 'Acquire::https::CAInfo "%s";\n' "$CA" >"$TRUST_DIR/apt.conf"
}

if [ -n "$cmd" ] && [ -f "$CA_CRT" ] && [ -f "$SYS_BUNDLE" ]; then
	[ -n "$bundle" ] || bundle="$PWD"
	cfg="$bundle/config.json"
	if [ -f "$cfg" ] && ! grep -q '"destination":"/etc/nexus3/ca"' "$cfg" && prepare_trust_dir; then
		ENVS="\"SSL_CERT_FILE=$CA\",\"CURL_CA_BUNDLE=$CA\",\"REQUESTS_CA_BUNDLE=$CA\",\"PIP_CERT=$CA\",\"NODE_EXTRA_CA_CERTS=$CA\",\"GIT_SSL_CAINFO=$CA\",\"CARGO_HTTP_CAINFO=$CA\",\"NIX_SSL_CERT_FILE=$CA\",\"DENO_CERT=$CA\",\"WGETRC=/etc/nexus3/ca/wgetrc\",\"APT_CONFIG=/etc/nexus3/ca/apt.conf\""
		MOUNT="{\"destination\":\"/etc/nexus3/ca\",\"type\":\"bind\",\"source\":\"$TRUST_DIR\",\"options\":[\"rbind\",\"ro\",\"nosuid\",\"nodev\"]}"
		# Collapse to one line first (raw newlines never occur inside JSON strings)
		# so the ",]" fixup and the first-match rules hold for pretty-printed files.
		tr -d '\n' <"$cfg" >"$cfg.nexus3" 2>/dev/null &&
			sed -i \
				-e "s|\"mounts\":[[:space:]]*\\[|\"mounts\":[$MOUNT,|" \
				-e "s|\"env\":[[:space:]]*\\[|\"env\":[$ENVS,|" \
				-e 's|,[[:space:]]*\]|]|g' "$cfg.nexus3" 2>/dev/null &&
			mv -f "$cfg.nexus3" "$cfg" 2>/dev/null || rm -f "$cfg.nexus3"
	fi
fi

exec "$RUNC" "$@"
