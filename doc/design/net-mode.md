# Net mode is recorded per sandbox

## Why

A sandbox's guest NIC is backed by a tap (default) or, opt-in, vhost-user.
Reading `NEXUS_NET_MODE` at start would boot a long-lived tap sandbox with no
network the moment the variable appears in the environment of a restart.

## Contract

- `NEXUS_NET_MODE` is read once, at create (`service.NetModeFromEnv`, called
  from `CreateAndBoot`). Invalid values fail the create.
- The value is stored on the record as `net_mode` (`omitempty`). Empty means
  tap. Tap sandboxes leave the field empty so the record keeps its legacy
  shape; a record written by an earlier binary loads as tap and re-serialises
  byte-identically. No schema bump, no backfill.
- Every later spawn (start, adopt, reacquire, upgrade) takes the mode from the
  spawn spec or the `--net-mode` supervisor flag. A missing flag means tap.
- `cloudhypervisor.Config.NetMode` carries the mode into the driver. The value
  `none` is honoured by the driver for tests only; the CLI and the record
  reject it.
- Builder VMs (`__builder`) always use tap and never read the variable.
