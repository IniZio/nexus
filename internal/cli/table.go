package cli

import (
	"fmt"
	"strings"
)

// renderTable lays rows out in fixed-width columns sized to the widest cell,
// header included, with two spaces between columns and no trailing padding on
// the last column.
//
// Alignment is not decoration in a terminal: a ragged list cannot be scanned,
// and scanning is the entire purpose of a listing command. Rows shorter than
// the header are padded with empty cells rather than silently truncating the
// table, so a caller that adds a column and forgets one row still renders.
func renderTable(headers []string, rows [][]string) string {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	norm := make([][]string, 0, len(rows))
	for _, r := range rows {
		row := make([]string, len(headers))
		copy(row, r)
		for i, c := range row {
			if len(c) > widths[i] {
				widths[i] = len(c)
			}
		}
		norm = append(norm, row)
	}

	var b strings.Builder
	writeRow := func(r []string) {
		for i, c := range r {
			if i == len(r)-1 {
				b.WriteString(c)
				b.WriteString("\n")
				continue
			}
			fmt.Fprintf(&b, "%-*s  ", widths[i], c)
		}
	}
	writeRow(headers)
	for _, r := range norm {
		writeRow(r)
	}
	return b.String()
}
