// guest_bench is a tiny static binary run inside the benchmark VM.
// It creates N files in the given directory, stats them, then unlinks them,
// and prints timing markers to stdout for the host to parse from the serial log.
//
// Usage: bench <dir> <label> [n]
//
//	bench --gitstatus <repo-dir> <label>
//
// Output lines (to stdout, visible on the guest serial console):
//
//	BENCH_START <label> n=<n>
//	BENCH_CREATE_NS <label> <nanoseconds>
//	BENCH_STAT_NS   <label> <nanoseconds>
//	BENCH_UNLINK_NS <label> <nanoseconds>
//	BENCH_END <label> create=<ms>ms stat=<ms>ms unlink=<ms>ms total=<ms>ms
//
//	BENCH_GITSTATUS_START <label>
//	BENCH_GITSTATUS_NS <label> <nanoseconds>
//	BENCH_GITSTATUS_END <label> elapsed=<ms>ms files=<n>
//
// BENCH-REDO note: --no-optional-locks and GIT_OPTIONAL_LOCKS=0 REMOVED.
// Both were pinning the ext4 leg in its worst case (re-hash every run).
// Index writeback is now allowed; both legs warm their index on the cold run
// and subsequent steady-state runs do a pure stat walk. See motive TBD-PD-17.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
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

// gitStatusBench runs `git status --porcelain` in repoPath and times the full
// execution. Index writeback is ALLOWED so that the first (cold) run refreshes
// the git index; subsequent steady-state runs then do a pure stat walk.
func gitStatusBench(repoPath, label string) {
	fmt.Printf("BENCH_GITSTATUS_START %s\n", label)

	// Global flags:
	//   -c safe.directory=*  bypasses git's dubious-ownership check.
	//                        The guest runs as root (uid=0) but repo files are owned by
	//                        the host user (uid=1000 on virtiofs / same uid on ext4).
	//   -C repoPath          change to the repo directory.
	// NOTE: --no-optional-locks and GIT_OPTIONAL_LOCKS=0 are intentionally absent.
	// Allowing index writeback is required for the equalised benchmark: the cold run
	// refreshes the index (re-hashing after inode scramble from mke2fs -d or from the
	// host-side copy), and steady-state runs thereafter do a pure stat walk on both legs.
	cmd := exec.Command("/usr/bin/git",
		"-c", "safe.directory=*",
		"-C", repoPath,
		"status", "--porcelain")
	// Set explicit PATH so the lookup works even in a minimal init environment.
	cmd.Env = append(os.Environ(),
		"PATH=/usr/bin:/usr/local/bin:/bin:/sbin:/usr/sbin",
		"GIT_TERMINAL_PROMPT=0",
	)

	start := time.Now()
	out, err := cmd.Output()
	ns := time.Since(start).Nanoseconds()

	if err != nil {
		fmt.Fprintf(os.Stderr, "git status %s: %v\n", repoPath, err)
		os.Exit(1)
	}

	files := bytes.Count(out, []byte("\n"))

	fmt.Printf("BENCH_GITSTATUS_NS %s %d\n", label, ns)
	fmt.Printf("BENCH_GITSTATUS_END %s elapsed=%dms files=%d\n",
		label, ns/1e6, files)
}

func main() {
	// --gitstatus mode: bench --gitstatus <repo-dir> <label>
	if len(os.Args) >= 2 && os.Args[1] == "--gitstatus" {
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: bench --gitstatus <repo-dir> <label>")
			os.Exit(1)
		}
		gitStatusBench(os.Args[2], os.Args[3])
		return
	}

	// Default mode: bench <dir> <label> [n]
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
