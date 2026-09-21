package vm

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	openshell "github.com/IniZio/nexus/internal/openshell"
)

var _ openshell.RelayListener = (*VMRelayListener)(nil)

type VMRelayListener struct {
	VsockSocket string
}

func (l *VMRelayListener) Listen(ctx context.Context, port uint32, handler func(net.Conn)) error {
	guestPortPath := fmt.Sprintf("%s_%d", l.VsockSocket, port)

	_ = os.Remove(guestPortPath)
	listener, err := net.Listen("unix", guestPortPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", guestPortPath, err)
	}
	defer listener.Close()

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	ul := listener.(*net.UnixListener)
	for {
		ul.SetDeadline(time.Now().Add(1 * time.Second))
		conn, err := ul.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return fmt.Errorf("accept on %s: %w", guestPortPath, err)
		}
		go handler(conn)
	}
}
