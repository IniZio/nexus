package repro

import (
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// AgentLinkage returns "static", "dynamic", or "unknown" for the binary at path.
// Uses debug/elf to check for the PT_INTERP program header:
//   - PT_INTERP present → "dynamic" (has an ELF interpreter)
//   - PT_INTERP absent  → "static"
//   - Cannot open/parse → "unknown"
//
// Never shells out.
//
// MUTATION: change the PT_INTERP check to always return "dynamic" → test fails
// with "got dynamic, want static"
func AgentLinkage(path string) string {
	f, err := elf.Open(path)
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return "dynamic"
		}
	}
	return "static"
}

// AgentSHA256 returns the hex-encoded SHA256 of the file at path.
// Returns ("", error) on failure.
func AgentSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// PopulateProvenance fills the provenance fields of r from agentBinPath and nexus3BinPath.
// If agentBinPath is non-empty: reads sha256, linkage; if linkage is "dynamic",
// appends a HIF probe "precondition.agent_dynamic" to r.Probes.
// If nexus3BinPath is non-empty: reads sha256.
// Does NOT return an error — failures are recorded as empty strings in r.
func PopulateProvenance(r *RunResult, agentBinPath, nexus3BinPath string) {
	r.AgentBinPath = agentBinPath
	if agentBinPath != "" {
		if sum, err := AgentSHA256(agentBinPath); err == nil {
			r.AgentBinSHA256 = sum
		}
		r.AgentLinkage = AgentLinkage(agentBinPath)
		if r.AgentLinkage == "dynamic" {
			r.Probes = append(r.Probes, probeHIF("precondition.agent_dynamic",
				fmt.Sprintf("agent binary %s is dynamically linked — bricks every builder boot", filepath.Base(agentBinPath))))
		}
	}
	r.Nexus3BinPath = nexus3BinPath
	if nexus3BinPath != "" {
		if sum, err := AgentSHA256(nexus3BinPath); err == nil {
			r.Nexus3SHA256 = sum
		}
	}
}
