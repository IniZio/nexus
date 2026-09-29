# virtiofsd lifetime: tied to the VM, not the supervisor

## Why

virtiofsd used to be a direct child of the per-sandbox supervisor with
`Pdeathsig=SIGKILL`. cloud-hypervisor is not a child of the supervisor (it lives
under the netns child), so when the supervisor exited (crash, SIGKILL,
`supervisor-upgrade` handoff) the kernel killed every virtiofsd while the VM
kept running. CH has no vhost-user-fs reconnect and virtiofsd unlinks its
socket after accept, so guest FUSE requests then block forever.

virtiofsd is now spawned into the netns child's process group
(`SysProcAttr{Setpgid: true, Pgid: rt.ChildPGID}`, both the plain and the
file-mount user-namespace re-exec variants) with no Pdeathsig. It lives and dies
with the VM: `rt.Stop`'s `Kill(-ChildPGID)` and the adopted path's group-gone
check cover it, even though an adopted driver's `virtiofsdProcs` is empty.
virtiofsd also exits by itself when CH closes the vhost-user connection.

## Handoff R7 retired

The original R7 plan had the incoming supervisor "defuse the outgoing
supervisor's Pdeathsig". That cannot work: PR_SET_PDEATHSIG is set by the child
on itself and no other process can clear it. With virtiofsd outside the
supervisor's lifetime no virtiofsd state crosses a handoff, so
`Payload.Virtiofs` and `VirtiofsHandle` were removed.

## Legacy sandboxes

A virtiofsd forked by an old binary keeps its armed Pdeathsig; a new binary
cannot rescue it. Only `nexus sandbox stop && nexus sandbox start` makes such a
sandbox safe. `nexus supervisor-upgrade` refuses (code
`supervisor_upgrade_live_mounts_would_break`) when a live mount's virtiofsd is
parented by the supervisor or outside the netns pgid; `--force-drop-mounts`
overrides. `nexus recover` warns when live mounts exist but no virtiofsd is in
the netns group.
