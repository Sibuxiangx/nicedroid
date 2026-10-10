package router

import (
	"context"
	"sync"
	"time"

	"github.com/Sibuxiangx/nicedroid/internal/config"
	"github.com/Sibuxiangx/nicedroid/internal/store"
	"github.com/Sibuxiangx/nicedroid/internal/upstream"
)

// KeyState tracks one upstream key's quota snapshot.
type KeyState struct {
	KeyID             int64               `json:"key_id"`
	KeyName           string              `json:"key_name"`
	KeyMasked         string              `json:"key_masked"`
	StandardMax       int                 `json:"standard_max"`
	CoreMax           int                 `json:"core_max"`
	Standard          upstream.PoolLimits `json:"standard"`
	Core              upstream.PoolLimits `json:"core"`
	OveragePreference string              `json:"overage_preference"`
	ExtraUsageCents   int                 `json:"extra_usage_cents"`
	LastError         string              `json:"last_error,omitempty"`
	CheckedAt         time.Time           `json:"checked_at"`
}

// Snapshot is the router's view of quota health.
type Snapshot struct {
	Keys      []KeyState `json:"keys"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Router owns upstream key selection and quota monitoring.
type Router struct {
	client *upstream.Client
	store  *store.Store
	cfg    config.Config
	mu     sync.RWMutex
	keys   []store.UpstreamKey
	states map[int64]KeyState
	rr     int
}

// New builds a router.
func New(client *upstream.Client, st *store.Store, cfg config.Config) *Router {
	return &Router{
		client: client,
		store:  st,
		cfg:    cfg,
		states: map[int64]KeyState{},
	}
}

// Reload refreshes the key list from the store.
func (r *Router) Reload() error {
	keys, err := r.store.EnabledUpstreamKeys()
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.keys = keys
	r.mu.Unlock()
	return nil
}

// NextKey picks an upstream key using weighted round-robin over the healthiest
// available keys. It skips keys whose standard quota is exhausted when the
// request needs the standard pool.
func (r *Router) NextKey(needPool config.ModelTier) (store.UpstreamKey, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.keys) == 0 {
		return store.UpstreamKey{}, false
	}
	// Expand weighted list.
	var pool []store.UpstreamKey
	for _, k := range r.keys {
		w := k.Weight
		if w <= 0 {
			w = 1
		}
		if st, ok := r.states[k.ID]; ok {
			if needPool == config.TierStandard && st.StandardMax >= 100 {
				continue // exhausted
			}
		}
		for i := 0; i < w; i++ {
			pool = append(pool, k)
		}
	}
	if len(pool) == 0 {
		// Everything is exhausted; fall back to any enabled key so the caller
		// gets a real upstream error rather than a local failure.
		pool = r.keys
	}
	k := pool[r.rr%len(pool)]
	r.rr = (r.rr + 1) % 1_000_000
	return k, true
}

// RecordStatus updates the health status of a key after a call.
func (r *Router) RecordStatus(keyID int64, status string) {
	_ = r.store.SetUpstreamKeyStatus(keyID, status)
}

// Snapshot returns the latest quota view.
func (r *Router) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := Snapshot{UpdatedAt: time.Now()}
	for _, k := range r.keys {
		st, ok := r.states[k.ID]
		if !ok {
			st = KeyState{KeyID: k.ID, KeyName: k.Name, KeyMasked: mask(k.Key)}
		}
		out.Keys = append(out.Keys, st)
	}
	return out
}

// RefreshQuotas polls /api/billing/limits for every enabled key.
func (r *Router) RefreshQuotas(ctx context.Context) {
	keys, err := r.store.EnabledUpstreamKeys()
	if err != nil {
		return
	}
	r.mu.Lock()
	r.keys = keys
	r.mu.Unlock()
	for _, k := range keys {
		st := KeyState{KeyID: k.ID, KeyName: k.Name, KeyMasked: mask(k.Key), CheckedAt: time.Now()}
		lim, err := r.client.Limits(ctx, k.Key)
		if err != nil {
			st.LastError = err.Error()
		} else {
			st.Standard = lim.Limits.Standard
			st.Core = lim.Limits.Core
			st.StandardMax = lim.Limits.Standard.MaxUsedPercent()
			st.CoreMax = lim.Limits.Core.MaxUsedPercent()
			st.OveragePreference = lim.OveragePreference
			st.ExtraUsageCents = lim.ExtraUsageBalanceCents
		}
		r.mu.Lock()
		r.states[k.ID] = st
		r.mu.Unlock()
	}
}

// RunRefreshLoop polls quotas until ctx is done.
func (r *Router) RunRefreshLoop(ctx context.Context) {
	interval := r.cfg.QuotaPollInterval
	if interval <= 0 {
		interval = time.Minute
	}
	r.RefreshQuotas(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.RefreshQuotas(ctx)
		}
	}
}

func mask(k string) string {
	if len(k) <= 10 {
		return "****"
	}
	return k[:10] + "****" + k[len(k)-4:]
}
