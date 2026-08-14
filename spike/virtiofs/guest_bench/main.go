// guest_bench is a tiny static binary run inside the benchmark VM.
// It creates N files in the given directory, stats them, then unlinks them,
// and prints timing markers to stdout for the host to parse from the serial log.
//
// Usage: bench <dir> <label> [n]
//
// Output lines (to stdout, visible on the guest serial console):
//
//	BENCH_START <label> n=<n>
//	BENCH_CREATE_NS <label> <nanoseconds>
//	BENCH_STAT_NS   <label> <nanoseconds>
//	BENCH_UNLINK_NS <label> <nanoseconds>
//	BENCH_END <label> create=<ms>ms stat=<ms>ms unlink=<ms>ms total=<ms>ms
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

func bench(dir, label string, n int) {
	fmt.Printf("BENCH_START %s n=%d\n", label, n)

	// ---- CREATE ----
	start := time.Now()
	for i := range n {
		name := fmt.Sprintf("%s/f%d", dir, i)
		f, err := os.Create(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "create %s: %v\n", name, err)
			os.Exit(1)
		}
		f.Close()
	}
	createNs := time.Since(start).Nanoseconds()
	fmt.Printf("BENCH_CREATE_NS %s %d\n", label, createNs)

	// ---- STAT ----
	start = time.Now()
	for i := range n {
		name := fmt.Sprintf("%s/f%d", dir, i)
		if _, err := os.Stat(name); err != nil {
			fmt.Fprintf(os.Stderr, "stat %s: %v\n", name, err)
			os.Exit(1)
		}
	}
	statNs := time.Since(start).Nanoseconds()
	fmt.Printf("BENCH_STAT_NS %s %d\n", label, statNs)

	// ---- UNLINK ----
	start = time.Now()
	for i := range n {
		name := fmt.Sprintf("%s/f%d", dir, i)
		if err := os.Remove(name); err != nil {
			fmt.Fprintf(os.Stderr, "remove %s: %v\n", name, err)
			os.Exit(1)
		}
	}
	unlinkNs := time.Since(start).Nanoseconds()
	fmt.Printf("BENCH_UNLINK_NS %s %d\n", label, unlinkNs)

	total := createNs + statNs + unlinkNs
	fmt.Printf("BENCH_END %s create=%dms stat=%dms unlink=%dms total=%dms\n",
		label, createNs/1e6, statNs/1e6, unlinkNs/1e6, total/1e6)
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: bench <dir> <label> [n]")
		os.Exit(1)
	}
	dir := os.Args[1]
	label := os.Args[2]
	n := 1000
	if len(os.Args) > 3 {
		var err error
		n, err = strconv.Atoi(os.Args[3])
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad n: %v\n", err)
			os.Exit(1)
		}
	}
	bench(dir, label, n)
}
