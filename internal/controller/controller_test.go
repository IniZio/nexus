package controller

import (
	"strings"
	"testing"
)

func TestNewThreadRefAndChannel(t *testing.T) {
	ref := NewThreadRef("T123", "C456", "1234567890.123456")
	want := ThreadRef("slack:T123:C456:1234567890.123456")
	if ref != want {
		t.Fatalf("NewThreadRef = %q, want %q", ref, want)
	}
	if ch := ref.Channel(); ch != "C456" {
		t.Fatalf("Channel() = %q, want %q", ch, "C456")
	}
	if ch := (ThreadRef("bad")).Channel(); ch != "" {
		t.Fatalf("malformed Channel() = %q, want empty", ch)
	}
}

func TestValidTransition(t *testing.T) {
	all := []Status{
		StatusStarting,
		StatusWorking,
		StatusIdle,
		StatusWaitingOnUser,
		StatusClosed,
		StatusFailed,
		StatusPaused,
	}

	// Build expected set from the contract table.
	type pair struct{ from, to Status }
	allowed := map[pair]bool{}
	table := map[Status][]Status{
		StatusStarting:      {StatusWorking, StatusFailed, StatusClosed},
		StatusWorking:       {StatusIdle, StatusWaitingOnUser, StatusFailed, StatusClosed},
		StatusWaitingOnUser: {StatusWorking, StatusIdle, StatusPaused, StatusFailed, StatusClosed},
		StatusIdle:          {StatusWorking, StatusPaused, StatusFailed, StatusClosed},
		StatusPaused:        {StatusWorking, StatusFailed, StatusClosed},
		StatusFailed:        {StatusStarting, StatusClosed},
		StatusClosed:        {},
	}
	for from, tos := range table {
		for _, to := range tos {
			allowed[pair{from, to}] = true
		}
	}

	var failures []string
	for _, from := range all {
		for _, to := range all {
			got := ValidTransition(from, to)
			want := allowed[pair{from, to}]
			if got != want {
				failures = append(failures, "ValidTransition("+string(from)+", "+string(to)+") = "+boolStr(got)+", want "+boolStr(want))
			}
		}
	}
	if len(failures) > 0 {
		t.Fatalf("transition table mismatches:\n  %s", strings.Join(failures, "\n  "))
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
