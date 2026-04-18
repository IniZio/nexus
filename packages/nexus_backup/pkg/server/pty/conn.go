package pty

type Conn interface {
	Enqueue([]byte)
	GetPTY(id string) *Session
	SetPTY(id string, s *Session)
	RemovePTY(id string)
	ClosePTY(id string) bool
	CloseAllPTY()
}
