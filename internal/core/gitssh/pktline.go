package gitssh

import (
	"bytes"
	"io"
	"path"
	"strings"
)

// RefMatchesGlob reports whether ref matches pattern (path.Match; trailing /** is prefix-match).
func RefMatchesGlob(pattern, ref string) bool {
	const doubleStarSuffix = "/**"
	if strings.HasSuffix(pattern, doubleStarSuffix) {
		prefix := strings.TrimSuffix(pattern, doubleStarSuffix)
		return ref == prefix || strings.HasPrefix(ref, prefix+"/")
	}
	matched, err := path.Match(pattern, ref)
	return err == nil && matched
}

// ParseHex4 parses a 4-character ASCII hex slice into an int; (0, false) on invalid input.
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

// ParseRefUpdates reads pkt-line ref-update commands from r (up to 64 KiB), buffers consumed
// bytes, and validates each ref against allowedBranches. Empty allowedBranches is fail-closed.
// Returns buf (must be re-injected into ssh stdin), deniedRef, and malformed flag.
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
		if pktLen == 0 || pktLen == 1 || pktLen == 2 { // flush (0000) or delimiter (0001/0002): stop
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
		line := strings.TrimRight(string(data), "\n") // format: "<old> <new> <refname>\n"
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 3 {
			continue // capability advertisement or keep-alive
		}
		ref := parts[2]
		if idx := strings.IndexByte(ref, 0); idx >= 0 { // strip NUL-separated capabilities (first pkt-line only)
			ref = ref[:idx]
		}
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
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
