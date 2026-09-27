# guest-agent — design notes

**Branch**: develop  
**Status**: living document; updated as invariants are established

---

## Exit-frame semantics (`streamRingToWriter` in `cmd/nexus-agent/dataplane.go`)

The outbound goroutine sends an `Exit` frame only when `streamRingToWriter`
returns `normal=true` OR when `sess.exited` is already set. If the streamer
terminates abnormally (corrupt record header, write error, or ring overrun with
bytes lost) while the session is still running, the goroutine closes the
connection without an `Exit` frame. The host observes a read error on the
connection, which maps to a non-zero return code.

Sending `Exit{Code: 0}` in this situation would fabricate a successful exit for
a still-running process — the design guards against that by leaving the exit
code path entirely to the actual process exit.

---

## Ring eviction and record alignment (`streamRingToWriter`)

The ring holds either raw bytes (`tagged=false`) or length-prefixed records
(`tagged=true`, format `[tag:1][len:4][data:N]`). When `WaitNextCursored`
returns `overrun=true`, the eviction frontier has moved past the reader cursor.
Any partial record accumulated in `carry` is now misaligned against the new
oldest boundary. Discarding `carry` at that point is required to re-sync to the
next intact record. The ring guarantees `OldestOffset` is always a record
boundary, so the next chunk returned is safe to parse from its beginning.

---

## Ring overrun loss reporting

When `bytesSkipped > 0` at drain time, `streamRingToWriter` writes a human-
readable notice to `StreamStderr` and returns `false` (abnormal). The `false`
causes the outbound goroutine — for a still-live session — to close the
connection without an `Exit` frame, forcing the host to see a non-zero result.
This prevents silent data loss from being mistaken for a clean exit.

---

## Host-home symlink (`EnsureHostHomeSymlink` in `internal/core/agent/workspace_mount_linux.go`)

Agents running inside the guest inherit absolute host paths embedded in config
files (e.g. `/home/alice/.claude/plugins/…`). The symlink `hostHome → /root`
makes those paths resolve correctly inside the guest, where the user home
directory is `/root`. The symlink is established before workspace mounts are
applied.

The `--hosthome=<path>` kernel cmdline argument is the source of `hostHome`. An
empty or `/root` value skips the operation entirely.

**Skip conditions**: empty string, `/root`, non-absolute path, path containing
`..`, path that already exists as a symlink (leave as-is), or path that exists
as a non-empty real directory (log warning, leave as-is).

**Empty real directory**: if `hostHome` exists as an empty directory it is
removed and replaced with the symlink.

---

## `HandshakeAck.BytesLost` wire field (`internal/core/agent/wire/wire.go`)

`HandshakeAck.BytesLost` carries the number of bytes evicted from the ring
before this reader attached (0 = no loss). It occupies bytes 6–13 of the
handshake-ack payload (big-endian uint64, after the existing 5-byte
status+exit_code block). When an older writer sends the short payload,
`decodeHandshakeAck` zero-fills `BytesLost` (backward-compatible branch).

---

## Protocol error on unexpected data tag (`internal/core/agent/exec.go`)

On the host receive path, a `Data` frame whose tag is `StreamStdin` (0) or any
unknown value is treated as a fatal protocol error. `StreamStdin` is a
host→guest direction; its presence in a guest→host frame indicates a misaligned
ring read on the guest side. Treating it as fatal ensures the return code is
never silently fabricated as 0 when the output stream is corrupt.
