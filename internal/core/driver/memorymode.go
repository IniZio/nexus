package driver

import "github.com/IniZio/nexus/internal/core/domain"

// MemoryMode describes how a VM's memory is managed at runtime.
type MemoryMode int

const (
	MemoryModeFlat MemoryMode = iota
	MemoryModeVirtioMem
	MemoryModeBalloon
)

// DriftStatus is returned by MemoryModeReporter.ObserveSample.
type DriftStatus int

const (
	DriftOK DriftStatus = iota
	DriftSuspect
	DriftCorrected
)

// MemoryModeReporter is an optional interface implemented by drivers that
// expose per-VM memory mode and balloon drift correction. CHDriver implements this interface.
type MemoryModeReporter interface {
	MemoryMode(id domain.SandboxID) MemoryMode
	BalloonBytes(id domain.SandboxID) int64
	ObserveSample(id domain.SandboxID, memTotal, memAvail uint64) (DriftStatus, uint32)
}
