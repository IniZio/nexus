package safeenv

import (
	"os"
	"strings"
)

var allowedKeys = map[string]struct{}{
	"PATH":     {},
	"HOME":     {},
	"SHELL":    {},
	"TMPDIR":   {},
	"TMP":      {},
	"TEMP":     {},
	"USER":     {},
	"LOGNAME":  {},
	"LANG":     {},
	"LC_ALL":   {},
	"LC_CTYPE": {},
	"TERM":     {},
}

func Base() []string {
	raw := os.Environ()
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, entry := range raw {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		if _, ok := allowedKeys[key]; !ok {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		out = append(out, entry)
		seen[key] = struct{}{}
	}
	return out
}

