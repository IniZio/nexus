---
id: CRED-R-003
concept: C-CRED
summary: "From inside a claude-code sandbox, git push to an in-policy SSH remote succeeds via the vsock relay, with no private key material in the guest and no HTTPS push traffic through the MITM."
criticality: must
verification: live
status: active
trace: AC-3
---

From inside a claude-code sandbox, `git push origin HEAD:nexus/<branch>` to `git@github.com:<owner>/<repo>.git` **shall** succeed via the vsock git SSH relay. No private key file **shall** exist under `/root/.ssh` in the guest, `SSH_AUTH_SOCK` **shall** be unset in the guest, and `GIT_SSH_COMMAND` **shall** point at the `nexus-agent` shim. No `github.com` `git-receive-pack` CONNECT request **shall** appear in the MITM egress log for that push (the push does not route through the MITM proxy).

The host relay **shall** exec the real host `ssh` with the host's `SSH_AUTH_SOCK`, so the private key never crosses the guest boundary.

- **Why** — SSH remotes are the canonical remote format for many repos (GitHub's default clone URL). Rewriting SSH to HTTPS in gitconfig was a workaround that required per-push MITM interception; the vsock relay eliminates the MITM dependency for git operations while keeping private key material host-side.
- **Fit criterion** — In guest: `ls -la /root/.ssh` shows no `id_*` private key; `echo $SSH_AUTH_SOCK` is empty; `cat /proc/$(pgrep -n git)/environ | tr '\0' '\n' | grep GIT_SSH_COMMAND` shows the shim path. `nexus egress log` shows no `github.com` `git-receive-pack` CONNECT for the push. Live only.
- **Verification** live · **Criticality** must · **Source** nexus-mount-creds-ssh-relay#AC-3
- **Tests** `TestRelayE2E_UploadPack` (`internal/core/gitssh/relay_test.go:153`); `TestGitSSHShim_ExitCodePropagated` (`cmd/nexus-agent/git_ssh_shim_test.go:44`); `TestGitSSHShim_RequestArgvSent` (`git_ssh_shim_test.go:71`); `TestWriteReadRequest_RoundTrip` (`internal/core/gitssh/wire_test.go:13`)
