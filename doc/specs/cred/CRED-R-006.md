---
id: CRED-R-006
concept: C-CRED
summary: "A dev server started in the guest on port P (1024–11023) is auto-discovered and reachable at 127.0.0.1:P on a remote herdr ≥0.9 client at the same port number; the forward disappears when the guest listener closes."
criticality: must
verification: live
status: active
trace: AC-6
---

When a guest process binds a TCP port in the range `[GuestPortBase, GuestPortTop]` (1024–11023), nexus3 **shall** auto-discover the listener (via `/proc/net/tcp` polling inside the sandbox), open a supervised forward, and make the port reachable at `127.0.0.1:<port>` on the nexus3 host and at `127.0.0.1:<port>` on a remote herdr ≥ 0.9 client — **at the same port number both sides** (renumbering is not permitted; it breaks OAuth redirect URIs and Vite HMR WebSocket URLs).

This **requires herdr ≥ 0.9** on the remote client. nexus3 **shall** declare `min_herdr_version = "0.9.0"` and plugin ABI `"3"` in the herdr-plugin manifest (`plugins/herdr/herdr-plugin.toml`). herdr versions below 0.9 **shall** fail the ABI probe at install time with a clear version message, not silently skip auto-forward.

When the guest listener closes, nexus3 **shall** cancel the forward and the port **shall** disappear from the herdr overlay within the reconcile interval.

- **Why** — Manual `nexus3 forward` is fragile in practice: it requires the operator to know the port in advance, blocks a terminal pane, and does not survive the guest process restarting. Auto-discovery eliminates all three limitations and is the pattern herdr-plugin-msb already proved.
- **Fit criterion** — Two genuine hosts required (same-host loops are vacuous). On the laptop: `curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:<port>` returns 200 while the guest listener is running, then the local listening socket is gone after the guest process exits. Live only.
- **Verification** live · **Criticality** must · **Source** nexus3-mount-creds-ssh-relay#AC-6
- **Tests** `TestReconcileAppliesForward` (`internal/core/portfwd/manager_test.go:18`); `TestReconcileCancelOnStopped` (`manager_test.go:64`); `TestReconcileCancelOnVanished` (`manager_test.go:86`); `TestFilterListeners_ForwardablePort` (`internal/core/portfwd/discovery_test.go:212`); `TestParseProcNetTCP_ByteOrder` (`discovery_test.go:140`); `TestApplySamePortInvariant` (`internal/core/portfwd/forward_test.go:127`); `TestWriteForwardsStateAtomic_ConcurrentSafe` (`internal/cli/portfwd_state_test.go:16`)
