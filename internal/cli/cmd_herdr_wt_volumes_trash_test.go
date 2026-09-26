package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/volumestore"
)

const testHandle = "myorg/feat-branch"

func mustCreateDirVolume(t *testing.T, vs *volumestore.VolumeStore, name string) {
	t.Helper()
	if _, err := vs.Create(context.Background(), name, volumestore.KindDir, 0, ""); err != nil {
		t.Fatalf("create volume %s: %v", name, err)
	}
}

func TestHerdrWtRemoveVolumes_TrashesIrreplaceableDeletesCache(t *testing.T) {
	storeRoot := t.TempDir()
	vs := volumestore.New(filepath.Join(storeRoot, "volumes"))

	agentcfg := herdrAgentCfgDiskVolumeName(testHandle)
	nexusst := herdrNexusStateDiskVolumeName(testHandle)
	docker := herdrDockerDiskVolumeName(testHandle)
	gocache := herdrGoCacheDiskVolumeName(testHandle)
	gopath := herdrGoPathDiskVolumeName(testHandle)

	for _, name := range []string{agentcfg, nexusst, docker, gocache, gopath} {
		mustCreateDirVolume(t, vs, name)
	}

	results := herdrWtRemoveVolumes(context.Background(), storeRoot, testHandle)

	trashedNames := map[string]string{}
	deletedNames := map[string]bool{}
	for _, r := range results {
		if r.Trashed {
			trashedNames[r.Name] = r.Entry
		} else {
			deletedNames[r.Name] = true
		}
	}

	for _, name := range []string{agentcfg, nexusst} {
		entry, ok := trashedNames[name]
		if !ok {
			t.Errorf("expected %s to be trashed; got results=%v", name, results)
			continue
		}
		if entry == "" {
			t.Errorf("trashed %s has empty Entry", name)
		}
	}

	trash, err := vs.ListTrash(context.Background())
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	trashOriginals := map[string]bool{}
	for _, te := range trash {
		trashOriginals[te.Original] = true
	}
	for _, name := range []string{agentcfg, nexusst} {
		if !trashOriginals[name] {
			t.Errorf("%s not found in ListTrash", name)
		}
	}

	live, err := vs.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	liveNames := map[string]bool{}
	for _, r := range live {
		liveNames[r.Name] = true
	}
	for _, name := range []string{agentcfg, nexusst} {
		if liveNames[name] {
			t.Errorf("%s still in live list after Trash", name)
		}
	}

	for _, name := range []string{docker, gocache, gopath} {
		if !deletedNames[name] {
			t.Errorf("expected %s to be deleted outright; got results=%v", name, results)
		}
		if liveNames[name] {
			t.Errorf("%s still in live list after Rm", name)
		}
	}
}

func TestHerdrWtRemoveVolumes_MissingVolumesSkipped(t *testing.T) {
	storeRoot := t.TempDir()
	results := herdrWtRemoveVolumes(context.Background(), storeRoot, testHandle)
	if len(results) != 0 {
		t.Errorf("expected no results for empty store; got %v", results)
	}
}

func TestHerdrWtRemoveVolumes_ExpireOldTrash(t *testing.T) {
	storeRoot := t.TempDir()
	vs := volumestore.New(filepath.Join(storeRoot, "volumes"))

	agentcfg := herdrAgentCfgDiskVolumeName(testHandle)
	mustCreateDirVolume(t, vs, agentcfg)

	entry, err := vs.Trash(context.Background(), agentcfg)
	if err != nil {
		t.Fatalf("Trash: %v", err)
	}

	trashRoot := filepath.Join(storeRoot, "volumes", volumestore.TrashDir)
	oldEntry := strings.Replace(entry, entry[strings.LastIndex(entry, "@")+1:], "20200101T000000Z", 1)
	oldPath := filepath.Join(trashRoot, oldEntry)
	srcPath := filepath.Join(trashRoot, entry)
	if err := os.Rename(srcPath, oldPath); err != nil {
		t.Fatalf("rename to old timestamp: %v", err)
	}

	mustCreateDirVolume(t, vs, fmt.Sprintf("%s-dummy", agentcfg))
	_ = herdrWtRemoveVolumes(context.Background(), storeRoot, testHandle)

	trash, err := vs.ListTrash(context.Background())
	if err != nil {
		t.Fatalf("ListTrash after expire: %v", err)
	}
	for _, te := range trash {
		if te.Name == oldEntry {
			t.Errorf("old trash entry %s was not expired", oldEntry)
		}
	}
}

func TestHerdrWtRemoveVolumes_TrashEntryInResult(t *testing.T) {
	storeRoot := t.TempDir()
	vs := volumestore.New(filepath.Join(storeRoot, "volumes"))

	agentcfg := herdrAgentCfgDiskVolumeName(testHandle)
	mustCreateDirVolume(t, vs, agentcfg)

	results := herdrWtRemoveVolumes(context.Background(), storeRoot, testHandle)
	if len(results) != 1 {
		t.Fatalf("expected 1 result; got %d", len(results))
	}
	r := results[0]
	if !r.Trashed {
		t.Error("agentcfg result should be Trashed=true")
	}
	if r.Entry == "" {
		t.Error("agentcfg result Entry must not be empty")
	}
	if !strings.Contains(r.Entry, agentcfg+"@") {
		t.Errorf("Entry %q does not look like a trash entry for %q", r.Entry, agentcfg)
	}

	now := time.Now()
	gracePast := now.Add(volumestore.TrashGrace + time.Hour)
	expired, err := vs.ExpireTrash(context.Background(), volumestore.TrashGrace, gracePast)
	if err != nil {
		t.Fatalf("ExpireTrash: %v", err)
	}
	if len(expired) != 1 || expired[0] != r.Entry {
		t.Errorf("expected expired=[%s]; got %v", r.Entry, expired)
	}
}
