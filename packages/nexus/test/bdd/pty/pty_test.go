package pty_test

import (
	"testing"

	_ "github.com/inizio/nexus/packages/nexus/test/bdd/harness"
)

func TestPTYOpenClose(t *testing.T)  { t.Skip("not implemented: pty.open / pty.close") }
func TestPTYWriteRead(t *testing.T)  { t.Skip("not implemented: pty.write") }
func TestPTYResize(t *testing.T)     { t.Skip("not implemented: pty.resize") }
func TestPTYList(t *testing.T)       { t.Skip("not implemented: pty.list") }
func TestPTYTmux(t *testing.T)       { t.Skip("not implemented: pty.tmux") }
