package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/hub"
	"github.com/IniZio/nexus/internal/hubclient"
)

func TestMain(m *testing.M) {
	if os.Getenv("NEXUS_HUB_TEST_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

func hubCmd(t *testing.T, db string, args ...string) *exec.Cmd {
	t.Helper()
	full := append([]string{"--protocol", "1"}, args...)
	c := exec.Command(os.Args[0], full...)
	c.Env = append(os.Environ(), "NEXUS_HUB_TEST_MAIN=1", EnvDB+"="+db)
	return c
}

func TestProtocolMismatch(t *testing.T) {
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"--protocol", "99", "version"}, strings.NewReader(""), &out, &errb)
	if code != hubclient.ExitProtocolMismatch || !strings.Contains(errb.String(), "protocol mismatch") {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
}

func TestAppendLastLastAll(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	do := func(stdin string, args ...string) string {
		var out, errb bytes.Buffer
		a := append([]string{"--protocol", "1", "--db", db}, args...)
		if code := run(context.Background(), a, strings.NewReader(stdin), &out, &errb); code != 0 {
			t.Fatalf("%v: code %d: %s", args, code, errb.String())
		}
		return out.String()
	}
	if got := strings.TrimSpace(do(`{"topic":"sandbox:a","type":"sandbox.created","subject":"a","payload":{"id":"a"}}`, "append")); got != `{"seq":1}` {
		t.Fatalf("append: %s", got)
	}
	do(`{"topic":"sandbox:a","type":"sandbox.started","subject":"a"}`, "append")
	do(`{"topic":"sandbox:b","type":"sandbox.created","subject":"b"}`, "append")
	var e hubclient.Event
	if err := json.Unmarshal([]byte(do("", "last", "--subject", "a")), &e); err != nil || e.Type != "sandbox.started" {
		t.Fatalf("last: %+v %v", e, err)
	}
	if strings.TrimSpace(do("", "last", "--subject", "zzz")) != "null" {
		t.Fatal("last missing subject should print null")
	}
	if n := len(strings.Split(strings.TrimSpace(do("", "last-all")), "\n")); n != 2 {
		t.Fatalf("last-all lines = %d", n)
	}
	if !strings.Contains(do("", "version"), `"protocol":1`) {
		t.Fatal("version missing protocol")
	}
}

func TestConcurrentWatchBothReceive(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	type w struct {
		cmd *exec.Cmd
		sc  *bufio.Scanner
	}
	var ws []w
	for i := 0; i < 2; i++ {
		c := hubCmd(t, db, "watch", "--topic", "sandbox:x", "--cursor", "0")
		pipe, err := c.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
		ws = append(ws, w{c, bufio.NewScanner(pipe)})
	}
	time.Sleep(300 * time.Millisecond)
	ap := hubCmd(t, db, "append")
	ap.Stdin = strings.NewReader(`{"topic":"sandbox:x","type":"sandbox.created","subject":"x"}`)
	if out, err := ap.CombinedOutput(); err != nil || !strings.Contains(string(out), `"seq":1`) {
		t.Fatalf("append: %v %s", err, out)
	}
	for i, x := range ws {
		done := make(chan string, 1)
		go func() {
			if x.sc.Scan() {
				done <- x.sc.Text()
			} else {
				done <- ""
			}
		}()
		select {
		case line := <-done:
			var wl hubclient.WatchLine
			if err := json.Unmarshal([]byte(line), &wl); err != nil || wl.Kind != "event" || wl.Seq != 1 {
				t.Fatalf("watcher %d: line %q err %v", i, line, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("watcher %d timed out", i)
		}
	}
}

func TestWatchAck(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	ap := hubCmd(t, db, "append")
	ap.Stdin = strings.NewReader(`{"topic":"t","type":"x"}`)
	if out, err := ap.CombinedOutput(); err != nil {
		t.Fatalf("append: %v %s", err, out)
	}
	c := hubCmd(t, db, "watch", "--topic", "t", "--ack")
	c.Env = append(c.Env, hubclient.EnvSession+"=seat1")
	in, _ := c.StdinPipe()
	out, _ := c.StdoutPipe()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Process.Kill(); _ = c.Wait() }()
	if !bufio.NewScanner(out).Scan() {
		t.Fatal("no event")
	}
	if _, err := in.Write([]byte("1\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	_ = c.Process.Kill()
	_ = c.Wait()
	h := openForTest(t, db)
	defer h.Close()
	n, err := h.Cursor(context.Background(), "seat1", "t")
	if err != nil || n != 1 {
		t.Fatalf("cursor = %d err %v", n, err)
	}
}

func openForTest(t *testing.T, db string) *hub.Hub {
	t.Helper()
	h, err := hub.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestWatchSeatResumesFromCursor(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	for i := 0; i < 3; i++ {
		ap := hubCmd(t, db, "append")
		ap.Stdin = strings.NewReader(`{"topic":"t","type":"x"}`)
		if out, err := ap.CombinedOutput(); err != nil {
			t.Fatalf("append: %v %s", err, out)
		}
	}
	h := openForTest(t, db)
	if err := h.Ack(context.Background(), "s1", "t", 2); err != nil {
		t.Fatal(err)
	}
	h.Close()
	c := hubCmd(t, db, "watch", "--topic", "t", "--seat", "s1")
	out, _ := c.StdoutPipe()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Process.Kill(); _ = c.Wait() }()
	sc := bufio.NewScanner(out)
	if !sc.Scan() {
		t.Fatal("no event")
	}
	var l hubclient.WatchLine
	if err := json.Unmarshal(sc.Bytes(), &l); err != nil || l.Seq != 3 {
		t.Fatalf("first seq = %d err %v (%s)", l.Seq, err, sc.Text())
	}
}
