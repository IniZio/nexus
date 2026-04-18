package workspace

// State represents the lifecycle state of a workspace.
type State string

const (
	StateCreated  State = "created"
	StateRunning  State = "running"
	StatePaused   State = "paused"
	StateStopped  State = "stopped"
	StateRestored State = "restored"
	StateRemoved  State = "removed"
)

// CanTransitionTo returns true if a transition from the current state to next is valid.
func (s State) CanTransitionTo(next State) bool {
	if next == StateRemoved {
		return true
	}
	switch s {
	case StateCreated:
		return next == StateRunning
	case StateRunning:
		return next == StatePaused || next == StateStopped
	case StatePaused:
		return next == StateRunning || next == StateStopped
	case StateStopped:
		return next == StateRunning || next == StateRestored
	case StateRestored:
		return next == StateRunning
	}
	return false
}

// IsTerminal returns true if no further transitions are possible.
func (s State) IsTerminal() bool {
	return s == StateRemoved
}
