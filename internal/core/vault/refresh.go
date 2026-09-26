package vault

import (
	"context"
	"fmt"

	"golang.org/x/sync/singleflight"
)

type refresher struct {
	store    Store
	registry *Registry
	sf       singleflight.Group
}

func newRefresher(store Store, registry *Registry) *refresher {
	return &refresher{store: store, registry: registry}
}

func sfKey(k Key) string { return k.Principal + "\x00" + k.Integration }

func (r *refresher) refresh(ctx context.Context, k Key) (Record, error) {
	v, err, _ := r.sf.Do(sfKey(k), func() (any, error) {
		return r.doRefresh(ctx, k)
	})
	if err != nil {
		return Record{}, err
	}
	return v.(Record), nil
}

func (r *refresher) doRefresh(ctx context.Context, k Key) (Record, error) {
	rec, err := r.store.Get(ctx, k)
	if err != nil {
		return Record{}, err
	}
	conn, err := r.registry.Get(k.Integration)
	if err != nil {
		return Record{}, fmt.Errorf("vault: no connector for %q: %w", k.Integration, err)
	}
	refreshed, err := conn.Refresh(ctx, rec)
	if err != nil {
		return Record{}, fmt.Errorf("vault: refresh %q: %w", k.Integration, err)
	}
	refreshed.AllowedProjects = rec.AllowedProjects
	if err := r.store.Put(ctx, k, refreshed); err != nil {
		return Record{}, fmt.Errorf("vault: persist refreshed record: %w", err)
	}
	return refreshed, nil
}

func (r *refresher) forget(k Key) {
	r.sf.Forget(sfKey(k))
}
