package vsock

import (
	"encoding/json"
	"fmt"
	"net"
)

type Client struct {
	serverPort uint32
}

func NewClient(serverPort uint32) *Client {
	return &Client{serverPort: serverPort}
}

func (c *Client) RequestToken(workspaceID, provider string) (Response, error) {
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", c.serverPort))
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	if err := enc.Encode(Request{WorkspaceID: workspaceID, Provider: provider}); err != nil {
		return Response{}, err
	}

	var resp Response
	if err := dec.Decode(&resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}
