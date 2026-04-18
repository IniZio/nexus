package workspace

import "fmt"

// Policy defines behavioral constraints for a workspace.
type Policy struct {
	AutoStop       bool   `json:"autoStop,omitempty"`
	AutoStopDelay  int    `json:"autoStopDelay,omitempty"`
	IsolationLevel string `json:"isolationLevel,omitempty"`
	MaxLifetimeSec int    `json:"maxLifetimeSec,omitempty"`
}

// Validate checks that the policy values are coherent.
func (p Policy) Validate() error {
	if p.AutoStopDelay < 0 {
		return fmt.Errorf("autoStopDelay must be non-negative")
	}
	if p.MaxLifetimeSec < 0 {
		return fmt.Errorf("maxLifetimeSec must be non-negative")
	}
	return nil
}
