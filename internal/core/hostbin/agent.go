package hostbin

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

const (
	NexusAgent             = "nexus-agent"
	AgentEnvVar            = "NEXUS_AGENT_PATH"
	SourceKernelDir Source = "kernel-dir"
)

// Agent is the result of a successful ResolveAgent call.
type Agent struct {
	Path   string
	Bytes  []byte
	Source Source
}

// ResolveAgent locates the nexus-agent binary: env → embedded → PATH → kernelDir → error.
func (r *Resolver) ResolveAgent(kernelDir func() string) (Agent, error) {
	if v := r.getenv(AgentEnvVar); v != "" {
		fi, err := os.Stat(v)
		if err != nil || !fi.Mode().IsRegular() {
			return Agent{}, fmt.Errorf("%w: %s=%q (stat: %v)", ErrNotFound, AgentEnvVar, v, err)
		}
		data, err := os.ReadFile(v)
		if err != nil {
			return Agent{}, fmt.Errorf("%w: read %s=%q: %v", ErrNotFound, AgentEnvVar, v, err)
		}
		return Agent{Path: v, Bytes: data, Source: SourceEnv}, nil
	}

	emb := r.embedded()
	if _, err := fs.Stat(emb, NexusAgent+".zst"); err == nil {
		wantSHA := r.EmbeddedAgentSHA256()
		var data []byte
		if wantSHA != "" {
			data, err = decompressEmbedded(emb, NexusAgent, wantSHA)
			if err != nil {
				return Agent{}, err
			}
		} else {
			data, err = agentDecompressRaw(emb)
			if err != nil {
				return Agent{}, err
			}
			wantSHA = hexSHA256(data)
		}
		finalPath := filepath.Join(r.dataDir(), "artifacts", wantSHA, NexusAgent)
		if fi, serr := os.Stat(finalPath); serr != nil || !fi.Mode().IsRegular() {
			if werr := atomicWrite(finalPath, data); werr != nil {
				return Agent{}, fmt.Errorf("hostbin: write nexus-agent: %w", werr)
			}
		}
		return Agent{Path: finalPath, Bytes: data, Source: SourceEmbedded}, nil
	}

	if p, err := r.lookPath(NexusAgent); err == nil {
		r.logger().Warn("hostbin: falling back to PATH", "name", NexusAgent, "path", p)
		data, err := os.ReadFile(p)
		if err != nil {
			return Agent{}, fmt.Errorf("found nexus-agent but cannot read %s: %w", p, err)
		}
		return Agent{Path: p, Bytes: data, Source: SourcePath}, nil
	}

	if kernelDir != nil {
		if dir := kernelDir(); dir != "" {
			p := filepath.Join(dir, NexusAgent)
			data, err := os.ReadFile(p)
			if err == nil {
				return Agent{Path: p, Bytes: data, Source: SourceKernelDir}, nil
			}
			if !os.IsNotExist(err) {
				return Agent{}, err
			}
		}
	}

	return Agent{}, fmt.Errorf("%w: nexus-agent not found (tried $NEXUS_AGENT_PATH, embedded, PATH, beside kernel); run `make artifacts` or install nexus-agent", ErrNotFound)
}

// ResolveAgent is a convenience wrapper using a zero-value Resolver.
func ResolveAgent(kernelDir func() string) (Agent, error) {
	return (&Resolver{}).ResolveAgent(kernelDir)
}

// EmbeddedAgentSHA256 returns the trimmed contents of the embedded nexus-agent.sha256, or "".
func (r *Resolver) EmbeddedAgentSHA256() string {
	data, err := fs.ReadFile(r.embedded(), NexusAgent+".sha256")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// EmbeddedAgentSHA256 is a convenience wrapper using a zero-value Resolver.
func EmbeddedAgentSHA256() string {
	return (&Resolver{}).EmbeddedAgentSHA256()
}

func agentDecompressRaw(emb fs.FS) ([]byte, error) {
	f, err := emb.Open(NexusAgent + ".zst")
	if err != nil {
		return nil, fmt.Errorf("hostbin: open embedded nexus-agent.zst: %w", err)
	}
	defer f.Close()
	dec, err := zstd.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("hostbin: zstd reader: %w", err)
	}
	defer dec.Close()
	return io.ReadAll(io.LimitReader(dec, maxArtifactBytes))
}
