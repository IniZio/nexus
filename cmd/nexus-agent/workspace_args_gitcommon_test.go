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

func TestValidateWorkspaceMountArg(t *testing.T) {
	ok := []string{
		"--workspace-mount=d:/t:ext4:false",
		"--workspace-mount=d:/t:virtiofs:false:false:false::gitcommon",
		"--workspace-mount=d:/t:ext4:false:true:false:f.txt:gitcommon",
	}
	for _, a := range ok {
		if err := validateWorkspaceMountArg(a); err != nil {
			t.Errorf("%q: unexpected error %v", a, err)
		}
	}
	bad := []string{
		"--workspace-mount=d:/t:ext4:false:true:false:f:gitcommon:extra",
		"--workspace-mount=d:/t:ext4:false:true:false:f:newopt",
	}
	for _, a := range bad {
		if err := validateWorkspaceMountArg(a); err == nil {
			t.Errorf("%q: expected error", a)
		}
	}
}
