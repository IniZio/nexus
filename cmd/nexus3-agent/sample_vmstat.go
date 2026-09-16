package main

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// sampleVmstatPath is the /proc/vmstat location; tests point it at a fixture.
var sampleVmstatPath = "/proc/vmstat"

// readVmstatPswpin returns the cumulative pswpin (pages swapped in) counter
// from path. Any failure — missing file, missing key, unparsable value —
// yields 0 and never an error: the host governor treats a zero delta as
// "not paging", which is the safe default for kernels without swap.
func readVmstatPswpin(path string) uint64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || fields[0] != "pswpin" {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return v
	}
	return 0
}
