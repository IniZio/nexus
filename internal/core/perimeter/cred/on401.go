package cred

import (
	"context"
	"net/http"
	"strings"
)

// ForceRefreshFn is called on a 401 response to force-refresh a credential.
type ForceRefreshFn func(ctx context.Context) (string, error)

// Handle401Once calls forceRefresh on a 401 response and replays the request
// with the new Authorization header. At most one retry is attempted; a second
// 401 on the replay is returned as-is (fail closed).
func Handle401Once(
	ctx context.Context,
	req *http.Request,
	resp *http.Response,
	forceRefresh ForceRefreshFn,
	doRequest func(*http.Request) (*http.Response, error),
) (*http.Response, error) {
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	newTok, err := forceRefresh(ctx)
	if err != nil || newTok == "" {
		return resp, nil
	}
	newReq := req.Clone(ctx)
	bearer := newTok
	if !strings.HasPrefix(strings.ToLower(bearer), "bearer ") {
		bearer = "Bearer " + bearer
	}
	newReq.Header.Set("Authorization", bearer)
	return doRequest(newReq)
}
