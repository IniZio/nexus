package hubclient

import "testing"

func TestIsUrgent(t *testing.T) {
	cases := []struct {
		name string
		typ  string
		pl   string
		want bool
	}{
		{"died owned", TypeSandboxDied, `{"owner_seat":"me"}`, true},
		{"stopped owned", TypeSandboxStopped, `{"owner_seat":"me"}`, true},
		{"died other owner", TypeSandboxDied, `{"owner_seat":"you"}`, false},
		{"died no owner", TypeSandboxDied, `{}`, false},
		{"started owned", TypeSandboxStarted, `{"owner_seat":"me"}`, false},
		{"delegate done", TypeDelegateDone, `{}`, true},
		{"friction blocked", TypeDelegateFriction, `{"blocked":true}`, true},
		{"friction not blocked", TypeDelegateFriction, `{"blocked":false}`, false},
		{"message to me", TypeMessage, `{"to":"me"}`, true},
		{"message to other", TypeMessage, `{"to":"you"}`, false},
		{"budget refused mine", TypeBudgetRefused, `{"seat":"me"}`, true},
		{"budget refused other", TypeBudgetRefused, `{"seat":"you"}`, false},
		{"binary installed", TypeBinaryInstalled, `{}`, false},
		{"bad payload", TypeMessage, `nope`, false},
	}
	for _, c := range cases {
		got := IsUrgent(Event{Type: c.typ, Payload: []byte(c.pl)}, "me")
		if got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
