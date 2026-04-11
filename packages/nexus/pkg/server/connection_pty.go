package server

import (
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/server/pty"
)

func (c *Connection) Enqueue(b []byte) {
	c.send <- b
}

func (c *Connection) GetPTY(id string) *pty.Session {
	c.ptyMu.Lock()
	defer c.ptyMu.Unlock()
	return c.pty[id]
}

func (c *Connection) SetPTY(id string, s *pty.Session) {
	c.ptyMu.Lock()
	c.pty[id] = s
	c.ptyMu.Unlock()
}

func (c *Connection) RemovePTY(id string) {
	c.ptyMu.Lock()
	delete(c.pty, id)
	c.ptyMu.Unlock()
}

func (c *Connection) ClosePTY(id string) bool {
	c.ptyMu.Lock()
	session, ok := c.pty[id]
	if ok {
		delete(c.pty, id)
	}
	c.ptyMu.Unlock()
	if !ok {
		return false
	}
	if session.RemoteConn != nil {
		session.Closing.Store(true)
		session.Mu.Lock()
		_ = session.Enc.Encode(map[string]any{"id": session.ID, "type": "shell.close"})
		session.Mu.Unlock()
		_ = session.RemoteConn.Close()
		if session.Done != nil {
			select {
			case <-session.Done:
			case <-time.After(2 * time.Second):
			}
		}
		return true
	}

	_ = session.File.Close()
	if session.Cmd.Process != nil {
		_ = session.Cmd.Process.Kill()
		_, _ = session.Cmd.Process.Wait()
	}
	return true
}

func (c *Connection) CloseAllPTY() {
	c.ptyMu.Lock()
	ids := make([]string, 0, len(c.pty))
	for id := range c.pty {
		ids = append(ids, id)
	}
	c.ptyMu.Unlock()
	for _, id := range ids {
		_ = c.ClosePTY(id)
	}
}
