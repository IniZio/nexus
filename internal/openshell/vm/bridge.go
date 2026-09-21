package vm

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

func runBridge(ctx context.Context, bridgeSock, vsockSock string) error {
	_ = os.Remove(bridgeSock)

	listener, err := net.Listen("unix", bridgeSock)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", bridgeSock, err)
	}
	defer listener.Close()

	go func() {
		for {
			select {
			case <-ctx.Done():
				listener.Close()
				return
			default:
			}

			listener.(*net.UnixListener).SetDeadline(time.Now().Add(1 * time.Second))
			clientConn, err := listener.Accept()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if strings.Contains(err.Error(), "deadline exceeded") {
					continue
				}
				log.Printf("bridge accept error on %s: %v", bridgeSock, err)
				continue
			}

			go func(client net.Conn) {
				defer client.Close()

				vsockConn, err := connectVsock(ctx, vsockSock)
				if err != nil {
					log.Printf("bridge failed to connect vsock %s: %v", vsockSock, err)
					return
				}
				defer vsockConn.Close()

				proxyBridge(client, vsockConn)
			}(clientConn)
		}
	}()

	<-ctx.Done()
	return ctx.Err()
}

func connectVsock(ctx context.Context, vsockSock string) (net.Conn, error) {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", vsockSock)
	if err != nil {
		return nil, fmt.Errorf("dial vsock %s: %w", vsockSock, err)
	}

	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = conn.Write([]byte("CONNECT 5500\n"))
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("write CONNECT: %w", err)
	}
	conn.SetWriteDeadline(time.Time{})

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)
	reply, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read CONNECT reply: %w", err)
	}
	conn.SetReadDeadline(time.Time{})

	if !strings.HasPrefix(reply, "OK") {
		conn.Close()
		return nil, fmt.Errorf("CONNECT failed: %s", strings.TrimSpace(reply))
	}

	wrappedConn := &drainedConn{
		br:   br,
		conn: conn,
	}
	return wrappedConn, nil
}

type drainedConn struct {
	br   *bufio.Reader
	conn net.Conn
}

func (dc *drainedConn) Read(b []byte) (int, error) {
	// Use MultiReader to drain buffered data first
	return io.MultiReader(dc.br, dc.conn).Read(b)
}

func (dc *drainedConn) Write(b []byte) (int, error) {
	return dc.conn.Write(b)
}

func (dc *drainedConn) Close() error {
	return dc.conn.Close()
}

func (dc *drainedConn) LocalAddr() net.Addr {
	return dc.conn.LocalAddr()
}

func (dc *drainedConn) RemoteAddr() net.Addr {
	return dc.conn.RemoteAddr()
}

func (dc *drainedConn) SetDeadline(t time.Time) error {
	return dc.conn.SetDeadline(t)
}

func (dc *drainedConn) SetReadDeadline(t time.Time) error {
	return dc.conn.SetReadDeadline(t)
}

func (dc *drainedConn) SetWriteDeadline(t time.Time) error {
	return dc.conn.SetWriteDeadline(t)
}

// proxyBridge copies between a and b bidirectionally, waiting for both directions.
func proxyBridge(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
	}()

	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, a)
	}()

	wg.Wait()
}
