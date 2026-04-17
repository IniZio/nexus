package authrelay

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

type Grant struct {
	WorkspaceID string
	Env         map[string]string
	ExpiresAt   time.Time
}

type Broker struct {
	mu     sync.Mutex
	grants map[string]Grant
	now    func() time.Time
}

func NewBroker() *Broker {
	return &Broker{
		grants: make(map[string]Grant),
		now:    time.Now,
	}
}

func (b *Broker) Mint(workspaceID string, env map[string]string, ttl time.Duration) string {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	token := randomToken()
	b.mu.Lock()
	b.grants[token] = Grant{WorkspaceID: workspaceID, Env: cloneMap(env), ExpiresAt: b.now().Add(ttl)}
	b.mu.Unlock()
	return token
}

func (b *Broker) Consume(token string, workspaceID string) (map[string]string, bool) {
	b.mu.Lock()
	grant, ok := b.grants[token]
	if ok {
		delete(b.grants, token)
	}
	b.mu.Unlock()
	if !ok {
		return nil, false
	}
	if b.now().After(grant.ExpiresAt) {
		return nil, false
	}
	if grant.WorkspaceID != workspaceID {
		return nil, false
	}
	return cloneMap(grant.Env), true
}

func (b *Broker) Revoke(token string) {
	b.mu.Lock()
	delete(b.grants, token)
	b.mu.Unlock()
}

func cloneMap(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b)
}
