package hubclient

import (
	"context"
	"errors"
	"testing"
)

type fakeTransport struct {
	emitted []Event
	err     error
	last    *Event
}

func (f *fakeTransport) Emit(_ context.Context, ev Event) error {
	f.emitted = append(f.emitted, ev)
	return f.err
}
func (f *fakeTransport) Read(context.Context, Filter, string) (<-chan Item, error) {
	ch := make(chan Item, 1)
	ch <- Item{Gap: true}
	close(ch)
	return ch, nil
}
func (f *fakeTransport) Last(context.Context, string) (*Event, error) { return f.last, f.err }
func (f *fakeTransport) LastAll(context.Context) ([]Event, error) {
	return []Event{{Subject: "a"}}, f.err
}

func TestEmitDefaultsActor(t *testing.T) {
	t.Setenv(EnvSession, "")
	ft := &fakeTransport{}
	c := &Client{T: ft}
	if err := c.Emit(context.Background(), Event{Type: TypeSandboxCreated}); err != nil {
		t.Fatal(err)
	}
	if ft.emitted[0].Actor != ActorAnonymous {
		t.Errorf("actor = %q", ft.emitted[0].Actor)
	}
	t.Setenv(EnvSession, "s1")
	_ = c.Emit(context.Background(), Event{})
	if ft.emitted[1].Actor != "s1" {
		t.Errorf("actor = %q", ft.emitted[1].Actor)
	}
}

func TestEmitBestEffortSwallowsError(t *testing.T) {
	ft := &fakeTransport{err: errors.New("boom")}
	(&Client{T: ft}).EmitBestEffort(context.Background(), Event{Type: "x"})
	if len(ft.emitted) != 1 {
		t.Fatalf("emitted %d", len(ft.emitted))
	}
}

func TestInertClient(t *testing.T) {
	c := &Client{}
	ctx := context.Background()
	if err := c.Emit(ctx, Event{}); err != nil {
		t.Error(err)
	}
	if ev, err := c.Last(ctx, "a"); ev != nil || err != nil {
		t.Errorf("Last = %v, %v", ev, err)
	}
	if _, err := c.Read(ctx, Filter{}, ""); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Read err = %v", err)
	}
}

func TestLastUnsupportedIsNil(t *testing.T) {
	c := &Client{T: &fakeTransport{err: ErrUnsupported}}
	if ev, err := c.Last(context.Background(), "a"); ev != nil || err != nil {
		t.Errorf("Last = %v, %v", ev, err)
	}
}

func TestNewUsesRegisteredTransport(t *testing.T) {
	ft := &fakeTransport{}
	RegisterTransport(func() Transport { return ft })
	t.Cleanup(func() { RegisterTransport(nil) })
	if New().T != ft {
		t.Error("New did not use registered transport")
	}
}
