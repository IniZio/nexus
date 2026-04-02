#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
source "$script_dir/lib.sh"

backend="${1:-}"
[ -n "$backend" ] || die "usage: prepare-backend.sh <dind|lxc|firecracker>"

docker_host_from_context() {
  local context_name
  context_name="$(docker context show 2>/dev/null || true)"
  if [ -z "$context_name" ]; then
    printf '%s\n' "unix:///var/run/docker.sock"
    return
  fi

  local host
  host="$(docker context inspect "$context_name" --format '{{ (index .Endpoints "docker").Host }}' 2>/dev/null || true)"
  if [ -z "$host" ]; then
    host="unix:///var/run/docker.sock"
  fi
  printf '%s\n' "$host"
}

export_env() {
  local key="$1"
  local value="$2"
  if [ -n "${GITHUB_ENV:-}" ]; then
    printf '%s=%s\n' "$key" "$value" >> "$GITHUB_ENV"
  fi
}

normalize_name() {
  tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9-' '-'
}

lxc_exec_sudo() {
  local instance_name="$1"
  shift
  sudo lxc exec "$instance_name" -- "$@"
}

host_workspace_prepare_secrets() {
  local workspace_root="$1"
  [ -n "$workspace_root" ] || return 0
  [ -d "$workspace_root" ] || return 0

  local needs_secret_generation=false

  if [ ! -f "$workspace_root/.env" ] || [ ! -f "$workspace_root/backend/.env" ]; then
    needs_secret_generation=true
  fi

  if [ -f "$workspace_root/.env" ] && grep -q 'GENERATE_SECRET\[' "$workspace_root/.env" 2>/dev/null; then
    needs_secret_generation=true
  fi

  if [ -f "$workspace_root/backend/.env" ] && grep -q 'GENERATE_SECRET\[' "$workspace_root/backend/.env" 2>/dev/null; then
    needs_secret_generation=true
  fi

  if [ "$needs_secret_generation" = true ]; then
    echo "+ (host) pre-generating .env files via make secret"
    (
      cd "$workspace_root"
      make secret
    )
  fi
}

host_dns_servers() {
  awk '/^nameserver[[:space:]]+/ { print $2 }' /run/systemd/resolve/resolv.conf /etc/resolv.conf 2>/dev/null |
    grep -Ev '^(127\.|::1$|0\.0\.0\.0$)' |
    awk '!seen[$0]++'
}

lxc_write_resolv_conf() {
  local instance_name="$1"
  local tmp
  tmp="$(mktemp)"

  {
    if host_dns_servers | grep -q '.'; then
      while IFS= read -r dns; do
        [ -n "$dns" ] && printf 'nameserver %s\n' "$dns"
      done < <(host_dns_servers)
    else
      printf 'nameserver 1.1.1.1\n'
      printf 'nameserver 8.8.8.8\n'
    fi
    printf 'options timeout:2 attempts:5\n'
  } > "$tmp"

  lxc_exec_sudo "$instance_name" bash -lc 'if [ -L /etc/resolv.conf ]; then rm -f /etc/resolv.conf || true; fi'
  sudo lxc file push "$tmp" "$instance_name/etc/resolv.conf"
  rm -f "$tmp"
}

lxc_bootstrap_apt() {
  local instance_name="$1"
  local attempt

  for attempt in 1 2 3; do
    lxc_write_resolv_conf "$instance_name"
    if lxc_exec_sudo "$instance_name" bash -lc 'apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io docker-compose-v2 curl make python3 git nodejs npm || DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io docker-compose-plugin curl make python3 git nodejs npm'; then
      return 0
    fi
    echo "lxc apt bootstrap attempt $attempt failed; retrying" >&2
    sleep $((attempt * 5))
  done

  return 1
}

lxc_docker_ready() {
  local instance_name="$1"
  lxc_exec_sudo "$instance_name" bash -lc 'if docker info >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then exit 0; fi; if [ -S /tmp/nexus-host-docker.sock ] && DOCKER_HOST=unix:///tmp/nexus-host-docker.sock docker info >/dev/null 2>&1 && DOCKER_HOST=unix:///tmp/nexus-host-docker.sock docker compose version >/dev/null 2>&1; then exit 0; fi; exit 1'
}

lxc_install_docker_wrapper() {
  local instance_name="$1"
  local tmp
  tmp="$(mktemp)"
  cat > "$tmp" <<'EOF'
#!/usr/bin/env sh
if [ -z "${DOCKER_HOST:-}" ] && [ -S /tmp/nexus-host-docker.sock ]; then
  if /usr/bin/docker --host unix:///tmp/nexus-host-docker.sock version >/dev/null 2>&1; then
    export DOCKER_HOST=unix:///tmp/nexus-host-docker.sock
  fi
fi
exec /usr/bin/docker "$@"
EOF
  sudo lxc file push "$tmp" "$instance_name/usr/local/bin/docker"
  sudo lxc exec "$instance_name" -- chmod +x /usr/local/bin/docker
  rm -f "$tmp"
}

push_binary_into_lxc() {
  local instance_name="$1"
  local host_path="$2"
  local guest_path="${3:-$2}"

  [ -n "$host_path" ] || return 1
  [ -x "$host_path" ] || return 1

  lxc_exec_sudo "$instance_name" mkdir -p "$(dirname "$guest_path")"
  sudo lxc file push "$host_path" "$instance_name$guest_path"
  lxc_exec_sudo "$instance_name" chmod +x "$guest_path"
  return 0
}

lxc_seed_docker_tooling() {
  local instance_name="$1"

  for candidate in \
    "$(command -v docker || true)" \
    "$(command -v dockerd || true)" \
    "$(command -v containerd || true)" \
    "$(command -v containerd-shim-runc-v2 || true)" \
    "$(command -v ctr || true)" \
    "$(command -v runc || true)" \
    /usr/bin/docker-init; do
    [ -n "$candidate" ] || continue
    if [ -x "$candidate" ]; then
      echo "+ sudo lxc file push $candidate $instance_name$candidate"
      push_binary_into_lxc "$instance_name" "$candidate" "$candidate" || true
    fi
  done

  local docker_compose_plugin=""
  for candidate in \
    /usr/libexec/docker/cli-plugins/docker-compose \
    /usr/lib/docker/cli-plugins/docker-compose \
    /usr/local/lib/docker/cli-plugins/docker-compose; do
    if [ -x "$candidate" ]; then
      docker_compose_plugin="$candidate"
      break
    fi
  done

  if [ -n "$docker_compose_plugin" ]; then
    echo "+ sudo lxc exec $instance_name -- mkdir -p /usr/libexec/docker/cli-plugins"
    sudo lxc exec "$instance_name" -- mkdir -p /usr/libexec/docker/cli-plugins
    echo "+ sudo lxc file push $docker_compose_plugin $instance_name/usr/libexec/docker/cli-plugins/docker-compose"
    sudo lxc file push "$docker_compose_plugin" "$instance_name/usr/libexec/docker/cli-plugins/docker-compose"
    echo "+ sudo lxc exec $instance_name -- chmod +x /usr/libexec/docker/cli-plugins/docker-compose"
    sudo lxc exec "$instance_name" -- chmod +x /usr/libexec/docker/cli-plugins/docker-compose
  fi
}

lxc_seed_node_tooling() {
  local instance_name="$1"

  local node_bin
  node_bin="$(command -v node || true)"
  [ -n "$node_bin" ] || return 0

  local node_prefix
  node_prefix="$(dirname "$(dirname "$node_bin")")"
  if [ ! -d "$node_prefix/bin" ]; then
    return 0
  fi

  echo "+ sudo lxc exec $instance_name -- mkdir -p /opt/nexus-node/bin /opt/nexus-node/lib/node_modules"
  sudo lxc exec "$instance_name" -- mkdir -p /opt/nexus-node/bin /opt/nexus-node/lib/node_modules

  if [ -x "$node_prefix/bin/node" ]; then
    echo "+ sudo lxc file push $node_prefix/bin/node $instance_name/opt/nexus-node/bin/node"
    sudo lxc file push "$node_prefix/bin/node" "$instance_name/opt/nexus-node/bin/node"
    sudo lxc exec "$instance_name" -- chmod +x /opt/nexus-node/bin/node
  fi

  if [ -d "$node_prefix/lib/node_modules/npm" ]; then
    echo "+ sudo lxc file push -r $node_prefix/lib/node_modules/npm $instance_name/opt/nexus-node/lib/node_modules/"
    sudo lxc file push -r "$node_prefix/lib/node_modules/npm" "$instance_name/opt/nexus-node/lib/node_modules/"
  fi

  echo "+ sudo lxc exec $instance_name -- install npm/npx wrappers"
  sudo lxc exec "$instance_name" -- bash -lc '
set -e
if [ ! -x /opt/nexus-node/bin/node ]; then
  exit 0
fi

cat > /opt/nexus-node/bin/npm <<"WRAP"
#!/usr/bin/env sh
exec /opt/nexus-node/bin/node /opt/nexus-node/lib/node_modules/npm/bin/npm-cli.js "$@"
WRAP

cat > /opt/nexus-node/bin/npx <<"WRAP"
#!/usr/bin/env sh
exec /opt/nexus-node/bin/node /opt/nexus-node/lib/node_modules/npm/bin/npx-cli.js "$@"
WRAP

chmod +x /opt/nexus-node/bin/npm /opt/nexus-node/bin/npx
'

  echo "+ sudo lxc exec $instance_name -- link node tooling"
  sudo lxc exec "$instance_name" -- bash -lc '
set -e
if [ ! -x /opt/nexus-node/bin/node ]; then
  exit 0
fi

for bin in node npm npx; do
  if [ -x "/opt/nexus-node/bin/$bin" ]; then
    ln -sf "/opt/nexus-node/bin/$bin" "/usr/local/bin/$bin"
  fi
done
'
}

lxc_seed_opencode_tooling() {
  local instance_name="$1"
  local opencode_bin node_modules_dir
  opencode_bin="$(command -v opencode || true)"
  node_modules_dir=""

  if [ -n "$opencode_bin" ]; then
    node_modules_dir="$(cd "$(dirname "$opencode_bin")/../lib/node_modules" 2>/dev/null && pwd || true)"
  fi

  if [ -z "$node_modules_dir" ] && command -v npm >/dev/null 2>&1; then
    node_modules_dir="$(npm root -g 2>/dev/null || true)"
  fi

  [ -n "$node_modules_dir" ] || return 0
  [ -d "$node_modules_dir/opencode-ai" ] || return 0

  echo "+ sudo lxc exec $instance_name -- mkdir -p /usr/local/bin /usr/local/lib/node_modules"
  sudo lxc exec "$instance_name" -- mkdir -p /usr/local/bin /usr/local/lib/node_modules

  echo "+ sudo lxc file push -r $node_modules_dir/opencode-ai $instance_name/usr/local/lib/node_modules/"
  sudo lxc file push -r "$node_modules_dir/opencode-ai" "$instance_name/usr/local/lib/node_modules/"

  echo "+ sudo lxc exec $instance_name -- install opencode wrapper"
  sudo lxc exec "$instance_name" -- bash -lc '
set -e
if [ ! -x /usr/local/lib/node_modules/opencode-ai/bin/opencode ]; then
  exit 0
fi

cat > /usr/local/bin/opencode <<"WRAP"
#!/usr/bin/env sh
if [ -x /usr/local/bin/node ]; then
  exec /usr/local/bin/node /usr/local/lib/node_modules/opencode-ai/bin/opencode "$@"
fi
exec /opt/nexus-node/bin/node /usr/local/lib/node_modules/opencode-ai/bin/opencode "$@"
WRAP

chmod +x /usr/local/bin/opencode
'
}

lxc_dump_dns_debug() {
  local instance_name="$1"
  echo "+ sudo lxc exec $instance_name -- dns diagnostics"
  sudo lxc exec "$instance_name" -- bash -lc '
set +e
echo "--- /etc/resolv.conf ---"
cat /etc/resolv.conf || true
echo "--- route ---"
ip route || true
echo "--- getent hosts archive.ubuntu.com ---"
getent hosts archive.ubuntu.com || true
echo "--- nslookup archive.ubuntu.com ---"
if command -v nslookup >/dev/null 2>&1; then
  nslookup archive.ubuntu.com || true
fi
'
}

ensure_lxc_host() {
  if ! command -v lxc >/dev/null 2>&1; then
    echo "+ sudo apt-get update"
    sudo apt-get update
    echo "+ sudo apt-get install -y lxd lxd-client"
    if ! sudo apt-get install -y lxd lxd-client; then
      echo "apt packages unavailable; falling back to snap lxd"
      if ! command -v snap >/dev/null 2>&1; then
        die "neither apt nor snap could install lxd (snap missing)"
      fi
      if ! snap list lxd >/dev/null 2>&1; then
        echo "+ sudo snap install lxd"
        sudo snap install lxd
      fi
    fi
  fi
  require_cmd lxc
  echo "+ command -v lxc"
  command -v lxc

  if ! groups | grep -qw lxd; then
    echo "+ sudo usermod -aG lxd $USER"
    sudo usermod -aG lxd "$USER" || true
  fi
  echo "+ sudo lxd init --auto"
  sudo lxd init --auto
  echo "+ sudo lxc info"
  sudo lxc info
}

if ! command -v docker >/dev/null 2>&1; then
  die "docker is required to prepare runtime backend"
fi

if [ "$backend" = "lxc" ]; then
  section "prepare lxc backend"
  ensure_lxc_host

  workspace_root="${GITHUB_WORKSPACE:-}"
  [ -n "$workspace_root" ] || die "GITHUB_WORKSPACE is required for lxc backend"
  host_workspace_prepare_secrets "$workspace_root"

  run_id="${GITHUB_RUN_ID:-local}"
  attempt="${GITHUB_RUN_ATTEMPT:-1}"
  repo="${GITHUB_REPOSITORY:-repo}"
  instance_name="$(printf 'nexus-doctor-%s-%s-%s' "$repo" "$run_id" "$attempt" | normalize_name)"

  if sudo lxc info "$instance_name" >/dev/null 2>&1; then
    echo "+ sudo lxc delete --force $instance_name"
    sudo lxc delete --force "$instance_name"
  fi

  echo "+ sudo lxc launch ubuntu:24.04 $instance_name"
  sudo lxc launch ubuntu:24.04 "$instance_name"

  echo "+ sudo lxc config set $instance_name security.nesting true"
  sudo lxc config set "$instance_name" security.nesting true

  echo "+ sudo lxc config set $instance_name security.syscalls.intercept.mknod true"
  sudo lxc config set "$instance_name" security.syscalls.intercept.mknod true

  echo "+ sudo lxc config set $instance_name security.syscalls.intercept.setxattr true"
  sudo lxc config set "$instance_name" security.syscalls.intercept.setxattr true

  echo "+ sudo lxc config device add $instance_name workspace disk source=$workspace_root path=$workspace_root"
  sudo lxc config device add "$instance_name" workspace disk source="$workspace_root" path="$workspace_root"

  host_docker_host="$(docker_host_from_context)"
  host_docker_socket=""
  if [[ "$host_docker_host" == unix://* ]]; then
    host_docker_socket="${host_docker_host#unix://}"
  fi

  if [ -n "$host_docker_socket" ] && [ -S "$host_docker_socket" ]; then
    local_proxy_sock="/tmp/nexus-host-docker.sock"
    echo "+ sudo lxc config device add $instance_name docker-sock proxy listen=unix:$local_proxy_sock connect=unix:$host_docker_socket"
    sudo lxc config device remove "$instance_name" docker-sock >/dev/null 2>&1 || true
    if sudo lxc config device add "$instance_name" docker-sock proxy listen=unix:$local_proxy_sock connect=unix:$host_docker_socket bind=container uid=0 gid=0 mode=0660; then
      sudo lxc exec "$instance_name" -- bash -lc "mkdir -p /var/run && ln -sf $local_proxy_sock /var/run/docker.sock"
      lxc_install_docker_wrapper "$instance_name"
    else
      echo "warning: failed to proxy docker socket into lxc instance; falling back to in-container docker bootstrap" >&2
    fi
  fi

  echo "+ seed docker tooling into lxc instance from host"
  lxc_seed_docker_tooling "$instance_name"
  echo "+ seed node tooling into lxc instance from host"
  lxc_seed_node_tooling "$instance_name"
  echo "+ seed opencode tooling into lxc instance from host"
  lxc_seed_opencode_tooling "$instance_name"

  echo "+ sudo lxc exec $instance_name -- bash -lc 'echo lxc-ready'"
  sudo lxc exec "$instance_name" -- bash -lc 'echo lxc-ready'

  echo "+ sudo lxc exec $instance_name -- ensure docker daemon is running"
  lxc_exec_sudo "$instance_name" bash -lc 'if command -v systemctl >/dev/null 2>&1; then systemctl enable docker >/dev/null 2>&1 || true; systemctl start docker >/dev/null 2>&1 || true; fi; if ! docker info >/dev/null 2>&1 && command -v dockerd >/dev/null 2>&1; then nohup dockerd --host=unix:///var/run/docker.sock --storage-driver=vfs --iptables=false --bridge=none --userland-proxy=false >/tmp/nexus-doctor-dockerd.log 2>&1 & sleep 5; fi'

  if ! lxc_docker_ready "$instance_name"; then
    echo "+ sudo lxc exec $instance_name -- apt bootstrap for docker runtime (retrying with dns fix)"
    if ! lxc_bootstrap_apt "$instance_name"; then
      echo "warning: apt bootstrap failed inside lxc; continuing to verify existing runtime" >&2
      lxc_dump_dns_debug "$instance_name" >&2 || true
    fi

    echo "+ sudo lxc exec $instance_name -- retry docker daemon start after apt bootstrap"
    lxc_exec_sudo "$instance_name" bash -lc 'if command -v systemctl >/dev/null 2>&1; then systemctl enable docker >/dev/null 2>&1 || true; systemctl start docker >/dev/null 2>&1 || true; fi; if ! docker info >/dev/null 2>&1 && command -v dockerd >/dev/null 2>&1; then nohup dockerd --host=unix:///var/run/docker.sock --storage-driver=vfs --iptables=false --bridge=none --userland-proxy=false >/tmp/nexus-doctor-dockerd.log 2>&1 & sleep 5; fi'
  fi

  echo "+ sudo lxc exec $instance_name -- docker info"
  if ! lxc_exec_sudo "$instance_name" bash -lc 'docker info'; then
    if ! lxc_exec_sudo "$instance_name" bash -lc 'DOCKER_HOST=unix:///tmp/nexus-host-docker.sock docker info'; then
    echo "docker info failed inside lxc instance; dumping dockerd logs" >&2
    lxc_exec_sudo "$instance_name" bash -lc 'cat /tmp/nexus-doctor-dockerd.log || true' >&2 || true
    lxc_dump_dns_debug "$instance_name" >&2 || true
    die "docker daemon is not ready inside lxc instance"
    fi
  fi

  echo "+ sudo lxc exec $instance_name -- docker compose version"
  if ! lxc_exec_sudo "$instance_name" bash -lc 'docker compose version'; then
    if ! lxc_exec_sudo "$instance_name" bash -lc 'DOCKER_HOST=unix:///tmp/nexus-host-docker.sock docker compose version'; then
      die "docker compose plugin is not ready inside lxc instance"
    fi
  fi

  if lxc_exec_sudo "$instance_name" bash -lc 'command -v node >/dev/null 2>&1 && command -v npm >/dev/null 2>&1'; then
    echo "+ sudo lxc exec $instance_name -- node --version"
    lxc_exec_sudo "$instance_name" bash -lc 'node --version'

    echo "+ sudo lxc exec $instance_name -- npm --version"
    lxc_exec_sudo "$instance_name" bash -lc 'npm --version'

    echo "+ sudo lxc exec $instance_name -- opencode --version (best effort)"
    if ! lxc_exec_sudo "$instance_name" bash -lc 'opencode --version'; then
      echo "warning: opencode is not ready inside lxc instance" >&2
    fi
  else
    echo "warning: node/npm are not available inside lxc instance; continuing with runtime-only guarantees" >&2
  fi

  export_env "NEXUS_DOCTOR_LXC_INSTANCE" "$instance_name"
  export_env "NEXUS_DOCTOR_LXC_EXEC_MODE" "sudo-lxc"
  export_env "NEXUS_DOCTOR_DIND_DOCKER_HOST" ""
elif [ "$backend" = "dind" ]; then
  section "prepare dind backend"
  echo "+ docker info"
  docker info

  docker_host="$(docker_host_from_context)"
  echo "+ resolved docker host from context: $docker_host"

  export_env "NEXUS_DOCTOR_DIND_DOCKER_HOST" "$docker_host"
  export_env "NEXUS_DOCTOR_LXC_INSTANCE" ""
  export_env "NEXUS_DOCTOR_LXC_EXEC_MODE" ""
elif [ "$backend" = "firecracker" ]; then
  section "prepare firecracker backend"

  # Native firecracker contract: require kernel and rootfs paths
  if [[ -z "${NEXUS_FIRECRACKER_KERNEL:-}" ]]; then
    die "NEXUS_FIRECRACKER_KERNEL is required when NEXUS_RUNTIME_BACKEND=firecracker"
  fi
  if [[ -z "${NEXUS_FIRECRACKER_ROOTFS:-}" ]]; then
    die "NEXUS_FIRECRACKER_ROOTFS is required when NEXUS_RUNTIME_BACKEND=firecracker"
  fi
  if [[ -n "${NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE:-}" ]]; then
    die "NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE was removed in native firecracker cutover; configure NEXUS_FIRECRACKER_KERNEL and NEXUS_FIRECRACKER_ROOTFS instead"
  fi
  if [[ -n "${NEXUS_DOCTOR_FIRECRACKER_INSTANCE:-}" ]]; then
    die "NEXUS_DOCTOR_FIRECRACKER_INSTANCE was removed in native firecracker cutover; configure NEXUS_FIRECRACKER_KERNEL and NEXUS_FIRECRACKER_ROOTFS instead"
  fi
  if [[ -n "${NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE:-}" ]]; then
    die "NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE was removed in native firecracker cutover; configure NEXUS_FIRECRACKER_KERNEL and NEXUS_FIRECRACKER_ROOTFS instead"
  fi

  echo "prepare-backend: native firecracker backend selected"
  echo "prepare-backend: kernel=${NEXUS_FIRECRACKER_KERNEL}"
  echo "prepare-backend: rootfs=${NEXUS_FIRECRACKER_ROOTFS}"
  echo "prepare-backend: Nexus daemon is responsible for microVM lifecycle provisioning via Firecracker API + vsock agent"

  # Export native firecracker env contract
  export_env "NEXUS_FIRECRACKER_KERNEL" "${NEXUS_FIRECRACKER_KERNEL}"
  export_env "NEXUS_FIRECRACKER_ROOTFS" "${NEXUS_FIRECRACKER_ROOTFS}"

  # Clear legacy env vars to prevent confusion
  export_env "NEXUS_DOCTOR_FIRECRACKER_INSTANCE" ""
  export_env "NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE" ""
  export_env "NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE" ""
  export_env "NEXUS_DOCTOR_DIND_DOCKER_HOST" ""
  export_env "NEXUS_DOCTOR_LXC_INSTANCE" ""
  export_env "NEXUS_DOCTOR_LXC_EXEC_MODE" ""
else
  die "unsupported backend: $backend"
fi
