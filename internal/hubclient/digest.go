package hubclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// DigestOpts carries the state the mailbox cannot see.
type DigestOpts struct {
	// TakeSeat is set when the arriving session is on a default seat whose
	// predecessor is dead: it owns nothing until it takes TakeSeat.
	TakeSeat string
	Leases   []string
	Budget   []string
}

// Digest builds the resume digest for seat from its inbox.
func (m *Mailbox) Digest(ctx context.Context, seat string, owned []string, o DigestOpts) (string, error) {
	topics := make([]string, 0, len(owned)+1)
	for _, id := range owned {
		topics = append(topics, SandboxTopic(id))
	}
	topics = append(topics, TopicHost)
	in, err := m.Inbox(ctx, seat, topics...)
	if err != nil {
		return "", err
	}
	return FormatDigest(seat, in.Items, o), nil
}

// FormatDigest renders the P7 resume digest: unread messages, events since the
// last ack, held leases, open budget reservations, and a gap marker.
func FormatDigest(seat string, items []Item, o DigestOpts) string {
	var msgs, evs []string
	gap := false
	for _, it := range items {
		if it.Gap {
			gap = true
			continue
		}
		e := it.Event
		if e.Type == TypeMessage {
			var p MessagePayload
			_ = json.Unmarshal(e.Payload, &p)
			msgs = append(msgs, fmt.Sprintf("%s: %s", p.From, p.Text))
			continue
		}
		evs = append(evs, fmt.Sprintf("%s %s", e.Type, e.Topic))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "nexus hub digest for %s\n", seat)
	if o.TakeSeat != "" {
		fmt.Fprintf(&b, "you own nothing; nexus hub seat take %s\n", o.TakeSeat)
	}
	if gap {
		b.WriteString("gap: journal history before the cursor is no longer available\n")
	}
	section := func(title string, rows []string) {
		if len(rows) == 0 {
			fmt.Fprintf(&b, "%s: none\n", title)
			return
		}
		fmt.Fprintf(&b, "%s (%d):\n", title, len(rows))
		for _, r := range rows {
			fmt.Fprintf(&b, "  - %s\n", r)
		}
	}
	section("unread messages", msgs)
	section("events since last ack", evs)
	section("held leases", o.Leases)
	section("open budget reservations", o.Budget)
	return b.String()
}
