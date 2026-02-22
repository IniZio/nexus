package docker

import (
	"context"
)

type Client struct {
	host string
}

func NewClient(host string) (*Client, error) {
	return &Client{
		host: host,
	}, nil
}

func (c *Client) Ping(ctx context.Context) error {
	return nil
}

func (c *Client) Close() error {
	return nil
}
