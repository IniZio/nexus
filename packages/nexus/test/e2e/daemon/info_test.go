//go:build e2e

package daemon_test

import (
	"testing"

	"github.com/inizio/nexus/packages/nexus/test/e2e/harness"
)

func TestNodeInfo(t *testing.T) {
	h := harness.New(t)

	var result struct {
		Node struct {
			Name string   `json:"name"`
			Tags []string `json:"tags,omitempty"`
		} `json:"node"`
		Capabilities []struct {
			Name      string `json:"name"`
			Available bool   `json:"available"`
		} `json:"capabilities"`
	}
	h.MustCall("node.info", nil, &result)

	if result.Node.Name == "" {
		t.Error("node.info: name is empty")
	}
	if len(result.Capabilities) == 0 {
		t.Error("node.info: capabilities is empty")
	}

	found := false
	for _, cap := range result.Capabilities {
		if cap.Name == "runtime.process" {
			found = true
			if !cap.Available {
				t.Error("node.info: runtime.process capability should be available")
			}
		}
	}
	if !found {
		t.Error("node.info: missing runtime.process capability")
	}
}
