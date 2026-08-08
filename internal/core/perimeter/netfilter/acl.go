// Copyright (c) The clawk Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Derived from github.com/clawkwork/clawk internal/netfilter/acl.go.
// See NOTICE for modification details.
// Gate (interactive hold/resolve) was removed; AllowList is fail-closed.

package netfilter

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
)

// originCustom labels the legacy single block maintained by the flat-list
// API (NewAllowList/SetPolicy/SetDeniedDomains). Chain-aware callers manage
// their own origins; the shims below only ever touch this one block.
const originCustom = "custom"

// AllowList is a thread-safe network filter consulted by gvproxy's TCP
// forwarder for every outbound SYN. Policy is a Chain of origin-labeled
// blocks (see blocks.go); on top of the chain sit three runtime sets, kept
// separately so a live policy change (SetChain/SetPolicy) can revoke exactly
// the entries that no longer have a justification:
//
//   - resolved: IPs from periodic DNS lookup of the chain's allow patterns,
//     mapped to the name that justified them so a policy deny can veto them
//   - observed: IPs the guest itself resolved (via gvproxy's DNS) for a name
//     the chain allows — this is how wildcards work in practice
//   - granted: IPs allowed at runtime by an explicit decision
//
// The resolved set is rebuilt periodically so DNS-rotated services (CDNs,
// load balancers) don't break.
//
// The AllowList also keeps two observability structures: a bounded map of
// every DNS answer the guest received (lastName), used to attribute a denied
// SYN to the hostname the guest was actually dialing, and a bounded ledger of
// those denials, queryable via Denials.
type AllowList struct {
	mu       sync.RWMutex
	chain    *Chain
	resolved map[netip.Addr]string   // IP -> resolved name that justified it
	observed map[netip.Addr]string   // IP -> name that justified it
	granted  map[netip.Addr]struct{} // IPs allowed at runtime via an explicit decision

	// allowAllUntil, when in the future, makes every destination pass — a
	// time-boxed "allow all" escape hatch. Zero means inactive.
	allowAllUntil time.Time

	lastName map[netip.Addr]string // every DNS answer, for denial attribution
	denials  map[string]*Denial    // keyed by Denial.Host

	resolver *net.Resolver
	ticker   *time.Ticker
	kick     chan struct{}
	stop     chan struct{}
	now      func() time.Time

	// OnDenial, if non-nil, is called (without the lock held) the first
	// time a destination host shows up in the denial ledger. Set it before
	// Start; it must not block.
	OnDenial func(Denial)
}

// Denial is one aggregated record of refused outbound connections to a single
// destination host. Host is the DNS name the guest resolved right before
// dialing when one is known, otherwise the literal IP.
type Denial struct {
	Host      string    `json:"host"`
	IP        string    `json:"ip"`
	Port      string    `json:"port"`
	Count     int       `json:"count"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`

	// Rule attributes the refusal to the chain rule that decided it, e.g.
	// "policy oisd: deny tracker.example.com". Empty for the default
	// fail-closed refusal (nothing matched, nothing to blame).
	Rule string `json:"rule,omitempty"`
}

const (
	// maxObservedNames bounds the DNS-answer attribution map. A guest
	// resolves at most a few hundred distinct names; hitting the cap means
	// something is enumerating, and resetting the map merely degrades
	// denial attribution to bare IPs until names are re-observed.
	maxObservedNames = 4096

	// maxDenialHosts bounds the denial ledger. Past the cap the entry with
	// the oldest LastSeen is evicted — recent denials are the ones the user
	// acts on.
	maxDenialHosts = 256
)

// NewAllowList builds an allow list from static entries (IPs/CIDRs) and a list
// of domains to resolve — the legacy flat-list form, kept as a shim over a
// single custom block. The caller should invoke Start() to begin periodic
// refresh and Stop() to release the goroutine.
func NewAllowList(ips, cidrs, domains []string) (*AllowList, error) {
	c, err := NewChain([]Block{{
		Origin:       originCustom,
		AllowDomains: domains,
		AllowIPs:     append(slices.Clone(ips), cidrs...),
	}})
	if err != nil {
		return nil, err
	}
	return NewAllowListFromChain(c), nil
}

// NewAllowListFromChain builds an allow list evaluating the given policy
// chain. A nil chain means an empty policy: everything falls through to the
// fail-closed refusal.
func NewAllowListFromChain(c *Chain) *AllowList {
	if c == nil {
		c, _ = NewChain(nil)
	}
	return &AllowList{
		chain:    c,
		resolved: make(map[netip.Addr]string),
		observed: make(map[netip.Addr]string),
		granted:  make(map[netip.Addr]struct{}),
		lastName: make(map[netip.Addr]string),
		denials:  make(map[string]*Denial),
		resolver: net.DefaultResolver,
		kick:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
		now:      time.Now,
	}
}

// SplitEntries partitions a list of allowed-IP entries into plain addresses
// and CIDR prefixes so NewAllowList can validate them distinctly.
func SplitEntries(entries []string) (ips, cidrs []string) {
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.Contains(e, "/") {
			cidrs = append(cidrs, e)
		} else {
			ips = append(ips, e)
		}
	}
	return ips, cidrs
}

// Start runs the periodic domain resolver. interval is how often to refresh;
// a good default is 5 minutes. SetChain/SetPolicy trigger an immediate
// refresh in between ticks.
func (al *AllowList) Start(interval time.Duration) {
	al.refreshDomains()
	al.ticker = time.NewTicker(interval)
	go func() {
		for {
			select {
			case <-al.ticker.C:
				al.refreshDomains()
			case <-al.kick:
				al.refreshDomains()
			case <-al.stop:
				return
			}
		}
	}()
}

// Stop ends the refresh goroutine.
func (al *AllowList) Stop() {
	if al.ticker != nil {
		al.ticker.Stop()
	}
	close(al.stop)
}

// SetChain replaces the policy chain. Observed entries whose name the new
// chain no longer allows are dropped immediately; IPs resolved from removed
// patterns stay allowed only until the refresh kicked off here rebuilds the
// resolved set (sub-second in the common case).
func (al *AllowList) SetChain(c *Chain) {
	if c == nil {
		c, _ = NewChain(nil)
	}
	al.mu.Lock()
	al.setChainLocked(c)
	al.mu.Unlock()
	al.kickRefresh()
}

// setChainLocked installs c and prunes observed entries it no longer allows.
// The caller holds al.mu.
func (al *AllowList) setChainLocked(c *Chain) {
	al.chain = c
	for addr, name := range al.observed {
		if v, _ := c.DecideName(name); v != VerdictAllow {
			delete(al.observed, addr)
		}
	}
}

// kickRefresh schedules an immediate domain refresh. Non-blocking: a refresh
// is already pending if the channel is full.
func (al *AllowList) kickRefresh() {
	select {
	case al.kick <- struct{}{}:
	default:
	}
}

// mutateCustomLocked edits the legacy custom block (creating it as the
// highest-precedence block when absent) and recompiles the chain. On error
// the previous chain stays installed. The caller holds al.mu.
func (al *AllowList) mutateCustomLocked(fn func(b *Block)) error {
	blocks := slices.Clone(al.chain.blocks)
	idx := lastCustomIdx(blocks)
	if idx < 0 {
		blocks = append(blocks, Block{Origin: originCustom})
		idx = len(blocks) - 1
	}
	fn(&blocks[idx])
	c, err := NewChain(blocks)
	if err != nil {
		return err
	}
	al.setChainLocked(c)
	return nil
}

// lastCustomIdx locates the block the legacy flat-list API operates on: the
// highest custom block, or -1 when the chain has none.
func lastCustomIdx(blocks []Block) int {
	for i := len(blocks) - 1; i >= 0; i-- {
		if blocks[i].Origin == originCustom {
			return i
		}
	}
	return -1
}

// SetPolicy replaces the legacy custom block's allows in place: static IPs,
// CIDR prefixes, and the domain list. Its denies (SetDeniedDomains) are
// preserved. Observed entries whose name no longer matches any pattern are
// dropped immediately; IPs resolved from removed domains stay allowed only
// until the refresh kicked off here rebuilds the resolved set.
func (al *AllowList) SetPolicy(ips, cidrs, domains []string) error {
	al.mu.Lock()
	err := al.mutateCustomLocked(func(b *Block) {
		b.AllowDomains = domains
		b.AllowIPs = append(slices.Clone(ips), cidrs...)
	})
	al.mu.Unlock()
	if err != nil {
		return err
	}
	al.kickRefresh()
	return nil
}

// Allow reports whether addr (in "host:port" form, or a bare IP) is allowed.
// Returns a descriptive error when rejected to aid debugging; every rejection
// is also recorded in the denial ledger, attributed to the DNS name the guest
// last resolved to that IP when one is known.
//
// Evaluation order (strongest first): chain name verdict, explicit runtime
// grants, chain IP verdict, automatic DNS-derived justifications. A named deny
// can never be overridden; an IP deny can be overridden by a grant.
// Destinations with no opinion from any rule are refused (fail-closed).
func (al *AllowList) Allow(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// Some callers pass a bare IP — accept either form.
		host = addr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("destination %q is not a literal IP", addr)
	}
	ip = ip.Unmap()

	al.mu.RLock()
	chain := al.chain
	bypass := !al.allowAllUntil.IsZero() && al.now().Before(al.allowAllUntil)
	name := al.lastName[ip]
	resolvedName, inResolved := al.resolved[ip]
	if name == "" {
		name = resolvedName
	}
	_, granted := al.granted[ip]
	_, observed := al.observed[ip]
	al.mu.RUnlock()

	if bypass {
		return nil
	}

	// The chain speaks once: one precedence walk over both rule spaces
	// (the attributed name and the literal IP together — see DecideAddr),
	// so a high block's `deny ip` beats a low block's domain allow. The
	// verdict's strength then depends on which space produced it: a NAME
	// verdict is the operator naming this destination — nothing overrides
	// it — while an IP verdict ranks below an explicit interactive grant
	// (IP rules are coarse; the human's per-destination judgment is more
	// informed) but ABOVE the automatic justifications: an operator's
	// `deny ip` guardrail may be overridden by a grant, never by DNS
	// automation.
	v, m, byName := chain.DecideAddr(name, ip)
	if byName {
		switch v {
		case VerdictDeny:
			return al.deny(ip, port, &m)
		case VerdictAllow:
			return nil
		}
	}

	// Explicit grants — a decision that allowed this IP.
	if granted {
		return nil
	}

	// IP-space chain verdict, for destinations no grant justified.
	switch v {
	case VerdictDeny:
		return al.deny(ip, port, &m)
	case VerdictAllow:
		return nil
	}

	// Automatic justifications, weakest: guest-observed DNS answers for
	// allowed names and resolver-refreshed apex IPs. Only reached when no
	// chain rule and no grant had an opinion.
	if observed || inResolved {
		return nil
	}

	// Nothing allowed this destination — fail closed.
	return al.deny(ip, port, nil)
}

// deny folds the refused SYN into the ledger and returns a descriptive error.
// m attributes the refusal to the chain rule that decided it; nil means the
// default fail-closed refusal.
func (al *AllowList) deny(ip netip.Addr, port string, m *Match) error {
	var rule string
	reason := "not in allow list"
	if m != nil {
		rule = m.String()
		reason = "blocked by deny list"
	}
	d := al.recordDenial(ip, port, rule)
	if d.Host == d.IP {
		return fmt.Errorf("destination %s %s", ip, reason)
	}
	return fmt.Errorf("destination %s (%s) %s", d.Host, ip, reason)
}

// SetDeniedDomains replaces the legacy custom block's denied domains.
// Entries are registrable domains; each blocks itself and every subdomain.
// A trailing dot and surrounding space are tolerated.
func (al *AllowList) SetDeniedDomains(domains []string) {
	cleaned := make([]string, 0, len(domains))
	for _, d := range domains {
		if d = strings.TrimSuffix(strings.TrimSpace(d), "."); d != "" {
			cleaned = append(cleaned, d)
		}
	}
	al.mu.Lock()
	// Cannot fail: domain entries never error and the IP entries were
	// already valid when the current chain compiled.
	_ = al.mutateCustomLocked(func(b *Block) { b.DenyDomains = cleaned })
	al.mu.Unlock()
}

// DeniedDomains returns a copy of the custom block's denied domains.
func (al *AllowList) DeniedDomains() []string {
	al.mu.RLock()
	defer al.mu.RUnlock()
	if i := lastCustomIdx(al.chain.blocks); i >= 0 {
		return slices.Clone(al.chain.blocks[i].DenyDomains)
	}
	return nil
}

// AllowAllFor opens a time-boxed bypass: every destination is allowed until
// now+d. A non-positive d clears the bypass.
func (al *AllowList) AllowAllFor(d time.Duration) {
	al.mu.Lock()
	if d <= 0 {
		al.allowAllUntil = time.Time{}
	} else {
		al.allowAllUntil = al.now().Add(d)
	}
	al.mu.Unlock()
}

// AllowAllUntil returns the instant the time-boxed bypass expires, or the zero
// time when no bypass is active or it has elapsed.
func (al *AllowList) AllowAllUntil() time.Time {
	al.mu.RLock()
	defer al.mu.RUnlock()
	if al.allowAllUntil.IsZero() || !al.now().Before(al.allowAllUntil) {
		return time.Time{}
	}
	return al.allowAllUntil
}

// grant adds ip to the runtime-granted set so subsequent connections to it
// pass without prompting. A grant sits below name verdicts: it cannot
// override a chain deny on the destination's domain.
func (al *AllowList) grant(ip netip.Addr) {
	al.mu.Lock()
	al.granted[ip] = struct{}{}
	al.mu.Unlock()
}

// recordDenial folds one refused SYN into the ledger and returns a snapshot
// of the updated entry. rule is the chain attribution (empty for the default
// fail-closed refusal). Fires OnDenial for first-seen hosts.
func (al *AllowList) recordDenial(ip netip.Addr, port, rule string) Denial {
	now := al.now()

	al.mu.Lock()
	host := al.lastName[ip]
	if host == "" {
		host = ip.String()
	}
	d, ok := al.denials[host]
	if !ok {
		if len(al.denials) >= maxDenialHosts {
			al.evictOldestDenialLocked()
		}
		d = &Denial{Host: host, FirstSeen: now}
		al.denials[host] = d
	}
	d.IP = ip.String()
	d.Port = port
	d.Rule = rule
	d.Count++
	d.LastSeen = now
	snapshot := *d
	hook := al.OnDenial
	al.mu.Unlock()

	if !ok && hook != nil {
		hook(snapshot)
	}
	return snapshot
}

func (al *AllowList) evictOldestDenialLocked() {
	var oldest string
	var oldestSeen time.Time
	for host, d := range al.denials {
		if oldest == "" || d.LastSeen.Before(oldestSeen) {
			oldest = host
			oldestSeen = d.LastSeen
		}
	}
	delete(al.denials, oldest)
}

// Denials returns a snapshot of the denial ledger, most recent first.
func (al *AllowList) Denials() []Denial {
	al.mu.RLock()
	out := make([]Denial, 0, len(al.denials))
	for _, d := range al.denials {
		out = append(out, *d)
	}
	al.mu.RUnlock()

	// Most recent first; ties broken by host so output is stable.
	slices.SortFunc(out, func(a, b Denial) int {
		if c := b.LastSeen.Compare(a.LastSeen); c != 0 {
			return c
		}
		return strings.Compare(a.Host, b.Host)
	})
	return out
}

// refreshDomains re-resolves the apex of every allow pattern the chain still
// stands behind (AllowDomainPatterns skips patterns a higher block denies)
// and rebuilds the resolved set from scratch — IPs of patterns removed from
// the policy drop out here. Wildcards ("*.example.com") resolve as the base
// domain only, covering the apex; sub-hosts resolve lazily at their own
// first connection attempt (see ObserveDNSAnswer). Each IP is stored with
// the name that justified it so Allow can veto it by name later.
func (al *AllowList) refreshDomains() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	al.mu.RLock()
	chain := al.chain
	al.mu.RUnlock()

	newResolved := make(map[netip.Addr]string)
	for _, pattern := range chain.AllowDomainPatterns() {
		host := strings.TrimPrefix(pattern, "*.")
		ips, err := al.resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			continue // transient DNS failure is non-fatal
		}
		for _, ip := range ips {
			newResolved[ip.Unmap()] = host
		}
	}

	al.mu.Lock()
	al.resolved = newResolved
	al.mu.Unlock()
}

// Size returns the current number of allowed IPs (chain exact-IP allows +
// resolved + observed + granted) plus CIDR allow prefixes.
func (al *AllowList) Size() (ipCount, prefixCount int) {
	al.mu.RLock()
	defer al.mu.RUnlock()
	exact, prefixes := al.chain.allowIPCounts()
	return exact + len(al.resolved) + len(al.observed) + len(al.granted), prefixes
}

// AllowTCP satisfies the machine.Filter interface. Forwards to Allow so
// gvproxy's TCP filter hook finds a method with the expected shape.
func (al *AllowList) AllowTCP(addr string) error { return al.Allow(addr) }

// AllowUDP satisfies the machine.Filter interface. Forwards to Allow so
// gvproxy's UDP filter hook gates UDP against the same allow-list as TCP
// (DNS-observed IPs auto-allow, so QUIC to allowed hosts is permitted).
func (al *AllowList) AllowUDP(addr string) error { return al.Allow(addr) }

// AllowICMP satisfies the machine.Filter interface. Forwards to Allow so
// gvproxy's ICMP filter hook gates ping against the same allow-list (addr
// is a bare destination IP, which Allow accepts).
func (al *AllowList) AllowICMP(addr string) error { return al.Allow(addr) }

// ObserveDNS satisfies the machine.Filter interface. Forwards to
// ObserveDNSAnswer so gvproxy's DNS observer hook finds a method with
// the expected shape.
func (al *AllowList) ObserveDNS(name string, ip net.IP) { al.ObserveDNSAnswer(name, ip) }

// ObserveDNSAnswer is the handler to install on gvproxy's DNS side. Every
// A-record the DNS server returns to the guest flows through here, keyed
// by the queried name. When the chain's verdict on the name is allow, the
// returned IP is added to the observed set — so the guest's next TCP SYN to
// that IP passes the filter.
//
// This is how "*.snapcraft.io" actually works in practice: the ACL can't
// pre-enumerate every subdomain, but each one's IP becomes legitimate
// the moment the guest resolves it (which happens right before it dials).
//
// Every answer — allowed or not — is also remembered so a later denied SYN
// to that IP can be attributed to the hostname instead of a bare address.
func (al *AllowList) ObserveDNSAnswer(name string, ip net.IP) {
	if ip == nil {
		return
	}
	name = strings.TrimSuffix(name, ".")
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return
	}
	addr = addr.Unmap()

	al.mu.Lock()
	defer al.mu.Unlock()
	if len(al.lastName) >= maxObservedNames {
		// Reset rather than evict: cheap, bounded, and only degrades
		// attribution until names are re-observed.
		al.lastName = make(map[netip.Addr]string)
	}
	al.lastName[addr] = name
	if v, _ := al.chain.DecideName(name); v == VerdictAllow {
		al.observed[addr] = name
	}
}
