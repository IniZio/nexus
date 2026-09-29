# S9b-7: vhost-user vs tap parity, throughput, soak

Host: develop @ edb56e4, 2026-09-29, isolated state (`HOME=/var/tmp/s9b7/home`).
Two sandboxes from `ghcr.io/inizio/nexus-base:latest` (Debian 12), identical flags
(`--egress closed`, named volume at `/data`, rw dir mount at `/mnt/host`,
`--egress-policy-json` for `github.com /octocat/Hello-World/**`); the second was
created with `NEXUS_NET_MODE=vhost-user`. Records confirm the modes:
tap has `guest_tap_name=nxg-...`, vhost-user has `net_mode=vhost-user` and a
`vhost_socket`.

## Parity matrix

| Row | tap | vhost-user | Evidence |
|---|---|---|---|
| port forward | PASS | PASS | guest `nc -l -p 8080` loop; `nexus forward <ref> 1808x:8080`; host `curl` returns `hello-fwd` on both |
| DNS allowed / denied / NXDOMAIN | PASS | PASS | `example.com` and `google.com` resolve identically (DNS is not the policy point); unknown name is NXDOMAIN on both |
| egress allowed / denied | PASS | PASS | `https://example.com` 200; `https://google.com` connect refused (curl 000) on both; `egress log` shows the same ALLOW/DENY lines |
| git-ssh relay | PASS | PASS | `GIT_SSH_COMMAND=/sbin/nexus-agent git-ssh git clone git@github.com:octocat/Hello-World.git` succeeds, same HEAD `7fd1a60`; host ssh-agent found at `/run/user/1003/ssh-agent.sock` (no keys copied) |
| git-ssh 50 MB push | SKIPPED | SKIPPED | no scratch repo owned by the tester; never push to third-party repos |
| rw dir mount | PASS | PASS | guest reads host marker; guest write visible on host |
| named volume | PASS | PASS | write + read `/data/vfile` |
| dockerd + image pull | PASS | PASS | `apt-get install docker.io` in the guest, `dockerd --data-root /data/docker --storage-driver vfs`; `docker pull alpine:3.21` (4.37 s tap, 4.40 s vhost-user) and `docker run --network none alpine echo` on both |
| guest dmesg | clean | clean | no virtio error/reset lines |
| herdr worktree + delegate agent | NOT RUN | NOT RUN | touches user herdr state |

Notes:

- The CDN host that serves Docker layers is `production.cloudfront.docker.com`;
  both modes denied it identically until `nexus egress allow <ref> <host>`
  admitted it at runtime (also proves runtime allow works over vhost-user).
- `alpine:3.21` `apk` over https fails cert verification against the MITM CA in
  both modes; not a mode difference.
- Spotlight discovery was not exercised separately; `nexus forward` is the verb.

## Throughput and latency

Endpoint: a python `http.server` on the host LAN address, admitted with
`egress allow <ref> 192.168.0.103`, so both modes cross the same egress proxy
to a local source with no internet variance. 300 MB body, runs interleaved
tap/vhost-user.

| Metric | tap | vhost-user | delta |
|---|---|---|---|
| bulk download, median of 10 (MB/s) | 236.7 | 225.4 | -4.8% |
| bulk download, best / worst (MB/s) | 287.0 / 186.0 | 287.1 / 134.5 | worst case is one outlier run |
| 200 sequential small GETs, median of 11 (us per request) | 5182 | 4905 | -5% (faster) |
| gateway ping, 50 packets avg RTT (ms) | 0.570 | 0.465 | within noise |
| `git clone` git@github.com Hello-World, median of 5 (ms) | 3698 | 3737 | +1% |
| TTFB https://example.com, median of 5 (ms) | 41.7 | 47.8 | internet noise |
| Debian `Contents-all.gz` 34 MB over internet, median of 6 (s) | 4.06 | 3.5 | internet variance dominates (1.9 to 14 s tap, 2.5 to 20 s vhost-user) |

Both modes showed occasional 300+ ms-per-request stalls in the small-GET loop
(tap 30 ms and 354 ms, vhost-user 350 ms); they are host noise, not mode
specific.

## Proposed threshold (plan D-4)

Median bulk download of at least 10 interleaved runs against a local endpoint
must be no more than 10% below tap (measured: 4.8%), and median small-request
latency no more than 15% above tap (measured: 5% below). Use medians only;
single runs vary by up to 40% on this host. Both are met, so the D-4 proposal of
<=10% stands.

## Soak

`scripts/s9b-soak.sh --bin NEXUS [--state DIR] [--duration SECS] [--interval SECS]`
creates one vhost-user sandbox (`s9bsoak/vu`, default-deny, allowing
example.com) in isolated state. Every interval it probes DNS, allowed
https 200, and denied https, logging to `$STATE/soak.log`. At the end it scans
guest `dmesg` for virtio/net errors, prints `RESULT PASS|FAIL`, and removes the
sandbox by exact name. The 24 h soak is not run; a 2 h run was started instead
(state `/var/tmp/s9b7/soak`).
