package splitkit

import (
	"context"
	"strings"
	"sync"
	"time"
)

// OnDemand loads the current Split Kit block when a request needs it.
// Results are cached briefly and concurrent lookups for one event share a fetch.
type OnDemand struct {
	host     string
	wait     time.Duration
	ttl      time.Duration
	mu       sync.Mutex
	cache    map[string]cacheEntry
	inflight map[string]*flight
}

type cacheEntry struct {
	block Block
	ok    bool
	at    time.Time
}

type flight struct {
	done  chan struct{}
	block Block
	ok    bool
}

// NewOnDemand returns a lookup that waits up to wait for a remoteValue and
// caches a successful block for ttl.
func NewOnDemand(host string, wait, ttl time.Duration) *OnDemand {
	if wait <= 0 {
		wait = 3 * time.Second
	}
	if ttl <= 0 {
		ttl = 15 * time.Second
	}
	return &OnDemand{
		host:     host,
		wait:     wait,
		ttl:      ttl,
		cache:    make(map[string]cacheEntry),
		inflight: make(map[string]*flight),
	}
}

// Current returns the cached or freshly loaded block for guid.
func (o *OnDemand) Current(ctx context.Context, guid string) (Block, bool) {
	if guid == "" {
		return Block{}, false
	}
	return o.lookup(ctx, guid, func() (Block, bool) {
		fetchCtx, cancel := context.WithTimeout(context.Background(), o.wait+15*time.Second)
		defer cancel()
		return FetchCurrent(fetchCtx, o.host, guid, o.wait)
	})
}

// Block returns the cached or freshly loaded getblocks entry for blockGUID.
func (o *OnDemand) Block(ctx context.Context, eventGUID, blockGUID string) (Block, bool) {
	eventGUID = strings.TrimSpace(eventGUID)
	blockGUID = strings.TrimSpace(blockGUID)
	if eventGUID == "" || blockGUID == "" {
		return Block{}, false
	}
	return o.lookup(ctx, eventGUID+"\x00"+blockGUID, func() (Block, bool) {
		fetchCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		blocks, err := fetchBlocks(fetchCtx, nil, o.host, eventGUID)
		if err != nil {
			return Block{}, false
		}
		return matchBlock(blocks, blockGUID)
	})
}

func (o *OnDemand) lookup(ctx context.Context, key string, load func() (Block, bool)) (Block, bool) {
	o.mu.Lock()
	if ent, ok := o.cache[key]; ok && time.Since(ent.at) < o.ttl {
		o.mu.Unlock()
		return ent.block, ent.ok
	}
	if f, ok := o.inflight[key]; ok {
		o.mu.Unlock()
		select {
		case <-f.done:
			return f.block, f.ok
		case <-ctx.Done():
			return Block{}, false
		}
	}
	f := &flight{done: make(chan struct{})}
	o.inflight[key] = f
	o.mu.Unlock()

	block, ok := load()

	o.mu.Lock()
	delete(o.inflight, key)
	if ok {
		o.cache[key] = cacheEntry{block: block, ok: true, at: time.Now()}
	}
	f.block = block
	f.ok = ok
	close(f.done)
	o.mu.Unlock()
	return block, ok
}
