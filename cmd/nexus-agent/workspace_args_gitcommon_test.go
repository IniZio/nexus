package main

import "testing"

func TestParseWorkspaceMountArg_gitCommon(t *testing.T) {
	m, ok := parseWorkspaceMountArg("--workspace-mount=nxfs1:/r/.git:virtiofs:false:false:false::gitcommon")
	if !ok || !m.GitCommon || m.IsFile || m.FileName != "" {
		t.Fatalf("got %+v ok=%v", m, ok)
	}
	m, ok = parseWorkspaceMountArg("--workspace-mount=nxfs1:/r/.git:virtiofs:false:false:false")
	if !ok || m.GitCommon {
		t.Fatalf("plain arg: %+v", m)
	}
}
