package oomattr

import (
	"encoding/json"

	"github.com/IniZio/nexus/internal/hubclient"
)

func classify(before, after Snapshot, sig Signal) hubclient.Cause {
	if sig.Signal != 9 && sig.Signal != SignalUnknown {
		return hubclient.CauseUnknown
	}
	switch {
	case after.ScopeOOM > before.ScopeOOM || after.AncestorOOM > before.AncestorOOM:
		return hubclient.CauseSliceOOM
	case after.VmstatOOMKill > before.VmstatOOMKill || after.ScopeOOMKill > before.ScopeOOMKill:
		return hubclient.CauseHostOOM
	}
	return hubclient.CauseUnknown
}

func backfill(startedPayload json.RawMessage, now Snapshot) hubclient.Cause {
	var p struct {
		BootID  string `json:"boot_id"`
		OOMKill int64  `json:"oom_kill"`
	}
	if json.Unmarshal(startedPayload, &p) != nil || p.BootID == "" || p.BootID != now.BootID {
		return hubclient.CauseUnknown
	}
	if now.VmstatOOMKill > p.OOMKill {
		return hubclient.CauseHostOOM
	}
	return hubclient.CauseUnknown
}
