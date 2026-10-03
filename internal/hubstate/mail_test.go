package hubstate

import "testing"

func TestMailPersistAckCursor(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutMail(Mail{ID: "b", To: "repo#x", From: "a", Text: "2", TS: 2}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutMail(Mail{ID: "a", To: "repo#x", From: "a", Text: "1", TS: 1}); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ms, err := st2.ListMail("repo#x")
	if err != nil || len(ms) != 2 || ms[0].ID != "a" {
		t.Fatalf("list = %+v %v", ms, err)
	}
	if err := st2.AckMail("repo#x", "cur1", []string{"a"}); err != nil {
		t.Fatal(err)
	}
	st3, _ := Open(dir)
	ms, _ = st3.ListMail("repo#x")
	if len(ms) != 1 || ms[0].ID != "b" {
		t.Fatalf("after ack = %+v", ms)
	}
	if c, _ := st3.Cursor("repo#x"); c != "cur1" {
		t.Fatalf("cursor = %q", c)
	}
	if err := st3.AckMailUrgent("repo#x", "u1", nil); err != nil {
		t.Fatal(err)
	}
	if c, _ := st3.UrgentCursor("repo#x"); c != "u1" {
		t.Fatalf("urgent cursor = %q", c)
	}
	if c, _ := st3.Cursor("repo#x"); c != "cur1" {
		t.Fatalf("plain cursor moved: %q", c)
	}
	if err := st3.PutMail(Mail{ID: "../x", To: "s"}); err == nil {
		t.Fatal("want invalid name error")
	}
}
