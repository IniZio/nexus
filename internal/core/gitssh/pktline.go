package gitssh

import (
	"bytes"
	"io"
	"path"
	"strings"
)

// RefMatchesGlob reports whether ref matches pattern.
// Trailing "/**": prefix match (refs/heads/nexus3/** matches refs/heads/nexus3/x
// and refs/heads/nexus3/x/y). All other patterns: path.Match semantics.
func RefMatchesGlob(pattern, ref string) bool {
	const doubleStarSuffix = "/**"
	if strings.HasSuffix(pattern, doubleStarSuffix) {
		prefix := strings.TrimSuffix(pattern, doubleStarSuffix)
		return ref == prefix || strings.HasPrefix(ref, prefix+"/")
	}
	matched, err := path.Match(pattern, ref)
	return err == nil && matched
}

// ParseHex4 parses a 4-character ASCII hex slice into an int.
// Returns (0, false) for invalid input.
func ParseHex4(b []byte) (int, bool) {
	if len(b) != 4 {
		return 0, false
	}
	val := 0
	for _, c := range b {
		val <<= 4
		switch {
		case c >= '0' && c <= '9':
			val |= int(c - '0')
		case c >= 'a' && c <= 'f':
			val |= int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			val |= int(c-'A') + 10
		default:
			return 0, false
		}
	}
	return val, true
}

// ParseRefUpdates reads pkt-line ref-update commands from r (up to 64 KiB),
// buffers all consumed bytes, and validates each ref against allowedBranches.
//
// Returns:
//
//	buf       — all bytes consumed from r (must be re-injected into ssh stdin)
//	deniedRef — non-empty if any ref didn't match allowedBranches
//	malformed — true if the pkt-line stream is invalid/too large
//
// An empty allowedBranches slice is fail-closed: every ref is denied.
// Stops reading at flush (0000), delimiter (0001/0002), or a pkt-line parse error.
// Does NOT drain r past the flush packet.
func ParseRefUpdates(r io.Reader, allowedBranches []string) (buf *bytes.Buffer, deniedRef string, malformed bool) {
	const maxPkt = 64 * 1024
	buf = &bytes.Buffer{}

	for buf.Len() <= maxPkt {
		var lenBuf [4]byte
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			break // body ended cleanly
		}
		buf.Write(lenBuf[:])
		pktLen, ok := ParseHex4(lenBuf[:])
		if !ok {
			malformed = true
			return
		}
		// Flush packet (0000) and delimiter packets (0001, 0002): stop.
		if pktLen == 0 || pktLen == 1 || pktLen == 2 {
			break
		}
		dataLen := pktLen - 4
		if dataLen <= 0 {
			continue // keep-alive: length field exactly 4, zero data bytes
		}
		if buf.Len()+dataLen > maxPkt {
			malformed = true
			return
		}
		data := make([]byte, dataLen)
		if _, err := io.ReadFull(r, data); err != nil {
			buf.Write(data)
			break
		}
		buf.Write(data)
		// Parse ref update command: "<old-sha1> <new-sha1> <refname>\n"
		line := strings.TrimRight(string(data), "\n")
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 3 {
			continue // capability advertisement or keep-alive
		}
		ref := parts[2]
		// Strip NUL-separated capabilities (present on first pkt-line only).
		if idx := strings.IndexByte(ref, 0); idx >= 0 {
			ref = ref[:idx]
		}
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		// Allowlist check.
		matched := false
		for _, pattern := range allowedBranches {
			if RefMatchesGlob(pattern, ref) {
				matched = true
				break
			}
		}
		if !matched {
			deniedRef = ref
			return
		}
	}
	return
}
