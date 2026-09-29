# Net mode (tombstone: vhost-user is the only NIC)

## Why

Tap networking was removed in S9d (`.groundwork/plans/s9d-tap-removal.md`).
Every sandbox and builder VM boots a vhost-user-net NIC served from the netns
child inside an empty user+net namespace. There is no mode to select, no
`net_mode` record field and no `--net-mode` supervisor flag.

## Contract

- `NEXUS_NET_MODE` is a tombstone. `service.CheckNetModeEnv` runs at create
  (`CreateAndBoot` and the `create --file` builder path). Any non-empty value,
  including `vhost-user`, fails the create with
  `NEXUS_NET_MODE is no longer supported (tap networking was removed in S9d; vhost-user is the only mode): unset NEXUS_NET_MODE`.
- `nexus doctor` check `net_mode_env` fails while the variable is set.
- Guest memory is always shared (CH refuses vhost-user devices on private memory).
- `ip` is no longer a host dependency. The AppArmor userns restriction no
  longer blocks networking; userns is still required.
- Deleting the tombstone is a later cleanup.

## Legacy sandboxes (S9d cutover, history)

| Legacy state | Behaviour |
|---|---|
| Stopped tap sandbox | Next `start` cold-boots vhost-user. Same MAC; old `net_mode` and `guest_tap_name` keys are ignored and dropped on the next record write. |
| Running pre-S9d tap VM | Keeps running on its old supervisor. `stop` works. `supervisor-upgrade`, adopt and reacquire refuse with code `supervisor_upgrade_legacy_nic`; remedy `nexus stop X && nexus start X`. |
| Running tap VM, supervisor dead | Recovery reports it indeterminate; no spawn, no kill. |
| Tap snapshot on disk | Refused (`ErrTapSnapshot`); fork and restore fail. No conversion. |
| Snapshot/fork of a running tap VM | Refused before the snapshot is taken. |

The `supervisor-backfill-netns-identity` verb was removed with the tap path.

## History

Before S9d the mode was recorded per sandbox as `net_mode` (empty meant tap),
read once at create from `NEXUS_NET_MODE`, and vhost-user became the default in
S9b-11. That design is retired; see `zero-host-deps-vhostnet.md` for the
vhost-user implementation and `s9b7-parity.md` for the tap comparison.
