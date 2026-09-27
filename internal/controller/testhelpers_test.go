package controller_test

import (
	"context"

	"github.com/IniZio/nexus/internal/controller"
)

type fakeLinker struct{ err error }

func (f *fakeLinker) Require(_ context.Context, _ string) error { return f.err }
func (f *fakeLinker) StartLink(_ context.Context, _, _ string) (string, error) {
	return "", controller.ErrNotImplemented
}

type fakeProjects struct{ project string }

func (f *fakeProjects) Resolve(_ context.Context, _ string) (string, error) {
	if f.project == "" {
		return "", controller.ErrNoProject
	}
	return f.project, nil
}

type fakeLc struct {
	paused  []string
	resumed []string
	stopped []string
	started []string
}

func (f *fakeLc) Pause(_ context.Context, id string) error {
	f.paused = append(f.paused, id)
	return nil
}
func (f *fakeLc) Resume(_ context.Context, id string) error {
	f.resumed = append(f.resumed, id)
	return nil
}
func (f *fakeLc) Stop(_ context.Context, id string) error {
	f.stopped = append(f.stopped, id)
	return nil
}
func (f *fakeLc) Start(_ context.Context, id string) error {
	f.started = append(f.started, id)
	return nil
}
