package service_test

import (
	"errors"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/fake"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/service"
)

func TestStart_storeOnlyRecord_returnsErrNotBootable(t *testing.T) {
	st := newFileStore(t)
	drv := fake.New()
	svc := service.New(st, drv, lifecycle.New()).WithDiskDir(t.TempDir())

	sb, err := svc.Create(ctx(), "proj", "noop", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, startErr := svc.Start(ctx(), sb.Handle())
	if startErr == nil {
		t.Fatal("Start returned nil; want ErrNotBootable")
	}
	if !errors.Is(startErr, service.ErrNotBootable) {
		t.Fatalf("want errors.Is(err, ErrNotBootable), got: %v", startErr)
	}

	fresh, err := svc.GetSandboxByID(ctx(), sb.ID)
	if err != nil {
		t.Fatalf("GetSandboxByID: %v", err)
	}
	if fresh.State != domain.Created {
		t.Errorf("state = %v after failed Start; want Created", fresh.State)
	}
}
