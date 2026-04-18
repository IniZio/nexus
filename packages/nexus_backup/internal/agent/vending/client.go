package vending

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Client struct {
	baseURL string
}

func NewClient(baseURL string) *Client {
	return &Client{baseURL: baseURL}
}

func (c *Client) GetToken(workspaceID, provider string) (string, time.Time, error) {
	reqBody, err := json.Marshal(map[string]string{
		"workspace_id": workspaceID,
		"provider":     provider,
	})
	if err != nil {
		return "", time.Time{}, err
	}

	resp, err := http.Post(
		c.baseURL+"/token",
		"application/json",
		bytes.NewReader(reqBody),
	)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()

	var result struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Error     string `json:"error,omitempty"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", time.Time{}, err
	}

	if result.Error != "" {
		return "", time.Time{}, fmt.Errorf("%s", result.Error)
	}

	return result.Token, time.Unix(result.ExpiresAt, 0), nil
}
