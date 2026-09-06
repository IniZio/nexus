// memhog allocates and holds a configurable amount of host RAM to apply
// memory pressure during nexus3 repro builds.  It receives --target-free-mib
// (the MemAvailable level to maintain), allocates the difference via mmap,
// touches every page to force physical backing, then blocks until SIGTERM/SIGINT.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

// floorMiB is the minimum --target-free-mib the binary will accept.
// Refusing to go below this prevents the host from becoming unresponsive.
const floorMiB = 1536

func main() {
	targetFreeMiB := flag.Int("target-free-mib", 0,
		"target MemAvailable in MiB to maintain (required, ≥1536)")
	flag.Parse()

	if *targetFreeMiB == 0 {
		fmt.Fprintln(os.Stderr, "memhog: --target-free-mib is required")
		os.Exit(1)
	}
	if *targetFreeMiB < floorMiB {
		fmt.Fprintf(os.Stderr, "memhog: --target-free-mib=%d is below floor %d MiB\n",
			*targetFreeMiB, floorMiB)
		os.Exit(1)
	}

	currentMiB, err := readMemAvailable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "memhog: cannot read /proc/meminfo: %v\n", err)
		os.Exit(1)
	}

	allocMiB := currentMiB - int64(*targetFreeMiB)
	if allocMiB <= 0 {
		fmt.Printf("memhog: currentAvail=%dMiB ≤ target=%dMiB; nothing to allocate; blocking on signal\n",
			currentMiB, *targetFreeMiB)
		blockOnSignal()
		return
	}

	allocBytes := allocMiB * 1024 * 1024
	mem, err := syscall.Mmap(-1, 0, int(allocBytes),
		syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		fmt.Fprintf(os.Stderr, "memhog: mmap(%d MiB) failed: %v\n", allocMiB, err)
		os.Exit(1)
	}

	// Touch every 4096th byte to force physical page backing.
	for i := 0; i < len(mem); i += 4096 {
		mem[i] = 1
	}

	afterMiB, _ := readMemAvailable()
	fmt.Printf("memhog: allocated %dMiB; MemAvailable now %dMiB\n", allocMiB, afterMiB)

	blockOnSignal()

	fmt.Println("memhog: releasing memory")
	_ = syscall.Munmap(mem)
}

// blockOnSignal parks the process until SIGTERM or SIGINT is received.
func blockOnSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	<-ch
}

// readMemAvailable returns the MemAvailable field from /proc/meminfo in MiB.
func readMemAvailable() (int64, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemAvailable:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, parseErr := strconv.ParseInt(fields[1], 10, 64)
				return kb / 1024, parseErr
			}
		}
	}
	return 0, fmt.Errorf("MemAvailable not found in /proc/meminfo")
}
