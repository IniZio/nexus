# S9b-R: tap upgrade-survival regression

`scripts/s9b-regression.sh --old <25f6fc7 binary> --new <HEAD binary> [--state DIR]`
creates a tap sandbox with the OLD binary (named volume, rw dir mount, default-deny
egress allowing only example.com), swaps `bin/nexus` by atomic rename, exports
`NEXUS_NET_MODE=vhost-user`, then exercises each lifecycle op and re-probes DNS,
allowed/denied egress, volume and mount after every step. State is isolated
(`env -i`, own HOME/XDG_RUNTIME_DIR/TMPDIR); only `s9br/fixture` and volume `s9brvol`
are removed, by exact name.

Build: `git archive <rev> | tar -x -C <dir>`; in it
`TMPDIR=/var/tmp make artifacts HOSTBIN_GOARCH=amd64 && go build -o <bin> ./cmd/nexus`.

## Result (OLD 25f6fc7, NEW f22a7d3, 2026-09-29)

| Step | Result | Evidence |
|---|---|---|
| fixture | PASS | tap `nxg-...`, `open_egress` absent (false), AllowedHosts=[example.com], no `net_mode` |
| pre-upgrade probes | PASS | DNS 2 addr, https example.com 200, google.com blocked, volume + mount rw |
| (a) running supervisor under NEW CLI | PASS | all probes; record diff empty; tap in `/proc/<netns_child_pid>/net/dev`; no `--net-mode` in supervisor argv |
| (b) `supervisor-upgrade` | PASS for tap/record, **FAIL mount** | pid changed; tap kept; record diff = `supervisor_pid` only; `/mnt/host` reads hang (see below) |
| (c) stop -> start | PASS | record diff lifecycle only (state, instance_id, netns_*, ch_api_socket, supervisor_*, stop_reason); same tap name; all probes |
| (d) snapshot/restore | see S9b-6 below | second tap fixture `s9br/snapfix` (no volume or mount) |
| (e) fork | see S9b-6 below | two children of `s9br/snapfix` |
| (f) SIGKILL supervisor + `nexus recover` | PASS for tap/record, **FAIL mount** | verified cmdline contains fixture id; replacement pid; `supervisor.reacquire.acquired` 0->1; "rebuilt the perimeter"; record diff = `supervisor_pid`; `/mnt/host` reads hang |
| AC4 overall | PASS | created vs final: only instance_id, netns_child_{pid,pgid,start_time}, supervisor_pid changed; `net_mode` never written |
| AC5 real sandbox | PASS | installed binary, read-only: `sandbox list` shows handbook-review/newman-cto-104-contracts running; `exec ... -- true` rc=0 |

## Finding: live `--mount` (virtiofs) hangs after supervisor replacement

After (b) and (f), `cat /mnt/host/marker.txt` in the guest hangs until cancelled
(`agent: pump: read frame: context canceled`), while DNS, egress and the named
volume (virtio-blk) keep working. Not caused by S9b:

- reproduced with `NEXUS_NET_MODE` unset (old -> new adopt);
- reproduced old(25f6fc7) -> mid(29a9f29, before S9b-1) adopt;
- old -> old is refused ("already serving"), so no clean control exists.

Likely the virtiofsd instance is not handed off or re-established on
supervisor replacement. Real user sandboxes carry many live mounts, so a
`supervisor-upgrade` on them is at risk. Follow-up outside S9b-R scope.
The harness reports this as FAIL; it is not suppressed.

## S9b-6: snapshot, restore and fork

The `fork`, `snapshot` and `restore` verbs were retired from the CLI (e15245a),
so the harness builds `./cmd/nexus` with `-tags s9blive` into
`$STATE/bin/nexus-live` and drives `nexus-live s9b-live snapshot|snaprm|restore|fork`
(`internal/cli/cmd_s9blive.go`; not part of the normal binary). Restore and
fork children get the detached reacquire supervisor the old verbs spawned.

Steps, run under `NEXUS_NET_MODE=vhost-user` against tap fixtures made by the
OLD binary:

- `(d) snapshot mount guard`: the mount + volume fixture is refused.
- `(d) snapshot` / `(d) restore` of `s9br/snapfix`, `(e) fork` x2: each child
  must be tap (`net_mode` absent, tap in the child's netns, no `--net-mode`
  argv, no `vhost_socket`), keep guest state, and pass DNS, allowed and denied
  egress. Children are removed by exact handle; snapshots via `snaprm`.

Expected outcome: only `(b) supervisor-upgrade` fails (refused by the
live-mounts guard). Tap names may repeat between siblings forked in the same
millisecond; they live in separate netns, so `(e)` checks distinct netns
children instead.
