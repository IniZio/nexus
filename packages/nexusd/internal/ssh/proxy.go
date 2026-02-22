package ssh

import (
	"fmt"
	"io"
	"net"

	"github.com/gorilla/websocket"
)

func ProxyAgentToWebSocket(agentConn net.Conn, wsConn *websocket.Conn) {
	buf := make([]byte, 4096)
	for {
		n, err := agentConn.Read(buf)
		if err != nil {
			if err != io.EOF {
				fmt.Printf("Agent read error: %v\n", err)
			}
			break
		}

		err = wsConn.WriteMessage(websocket.BinaryMessage, buf[:n])
		if err != nil {
			fmt.Printf("WebSocket write error: %v\n", err)
			break
		}
	}

	wsConn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(1000, ""))
}

func ProxyWebSocketToAgent(wsConn *websocket.Conn, agentConn net.Conn) {
	for {
		msgType, reader, err := wsConn.NextReader()
		if err != nil {
			break
		}

		if msgType == websocket.CloseMessage {
			break
		}

		if msgType != websocket.BinaryMessage {
			continue
		}

		buf := make([]byte, 4096)
		n, err := reader.Read(buf)
		if err != nil {
			if err != io.EOF {
				fmt.Printf("WebSocket read error: %v\n", err)
			}
			break
		}

		_, err = agentConn.Write(buf[:n])
		if err != nil {
			fmt.Printf("Agent write error: %v\n", err)
			break
		}
	}

	agentConn.Close()
}
