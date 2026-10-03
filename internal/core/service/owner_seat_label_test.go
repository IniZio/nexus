package service_test

import (
	"testing"

	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/hubclient"
)

func TestCreateStampsOwnerSeatLabel(t *testing.T) {
	t.Setenv(hubclient.EnvSeat, "alpha")
	svc := newSvc(t)
	sb, err := svc.Create(ctx(), "proj", "seated", service.CreateOptions{Labels: map[string]string{"k": "v"}})
	if err != nil {
		t.Fatal(err)
	}
	if sb.Labels[hubclient.LabelOwnerSeat] != "alpha" || sb.Labels["k"] != "v" {
		t.Errorf("labels = %v", sb.Labels)
	}
	t.Setenv(hubclient.EnvSeat, "")
	sb, err = svc.Create(ctx(), "proj", "unseated", service.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sb.Labels[hubclient.LabelOwnerSeat]; ok {
		t.Errorf("unexpected label: %v", sb.Labels)
	}
}
