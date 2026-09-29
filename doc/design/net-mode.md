# Net mode is recorded per sandbox

## Why

A sandbox's guest NIC is backed by vhost-user (default for new sandboxes since
S9b-11) or, opt-in, a tap. Tap removal is tracked in
`.groundwork/plans/s9d-tap-removal.md`.
Reading `NEXUS_NET_MODE` at start would boot a long-lived tap sandbox with no
network the moment the variable appears in the environment of a restart.

## Contract

- `NEXUS_NET_MODE` is read once, at create (`service.NetModeFromEnv`, called
  from `CreateAndBoot`). Invalid values fail the create. Unset means
  vhost-user. `NEXUS_NET_MODE=tap` is honoured; on a host where the tap probe
  reports EPERM (AppArmor userns restriction) an explicit tap create fails and
  names `NEXUS_NET_MODE=vhost-user`. Nothing probes vhost-user availability.
- The value is stored on the record as `net_mode` (`omitempty`). Empty means
  tap: a record written by an earlier binary loads as tap and re-serialises
  byte-identically. New sandboxes stamp `vhost-user` explicitly; explicit tap
  creates stamp `tap`. No schema bump, no backfill.
- Every later spawn (start, adopt, reacquire, upgrade) takes the mode from the
  spawn spec or the `--net-mode` supervisor flag. A missing flag means tap.
- `cloudhypervisor.Config.NetMode` carries the mode into the driver. The value
  `none` is honoured by the driver for tests only; the CLI and the record
  reject it.
- Builder VMs follow the same create-time resolution as sandboxes and receive
  the mode as `--net-mode`.
