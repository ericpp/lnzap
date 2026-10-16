package splitkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Watcher keeps one Socket.IO connection per Split Kit event and stores the
// latest value block from remoteValue, seeded from getblocks.
type Watcher struct {
	host       string
	guids      []string
	http       *http.Client
	mu         sync.RWMutex
	current    map[string]storedBlock
	backoffMin time.Duration
	backoffMax time.Duration
}

type storedBlock struct {
	block Block
	ok    bool
}

// NewWatcher builds a watcher for the distinct event ids.
// host is the Split Kit origin, for example https://curiohoster.com.
// If hc is nil, a client with a 10 second timeout is used.
func NewWatcher(host string, eventGUIDs []string, hc *http.Client) *Watcher {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	seen := make(map[string]struct{})
	var guids []string
	for _, g := range eventGUIDs {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		if _, ok := seen[g]; ok {
			continue
		}
		seen[g] = struct{}{}
		guids = append(guids, g)
	}
	return &Watcher{
		host:       strings.TrimRight(strings.TrimSpace(host), "/"),
		guids:      guids,
		http:       hc,
		current:    make(map[string]storedBlock),
		backoffMin: time.Second,
		backoffMax: 60 * time.Second,
	}
}

// Start dials each event in its own goroutine until ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) {
	for _, guid := range w.guids {
		go w.run(ctx, guid)
	}
}

// Current returns the latest block for an event guid.
func (w *Watcher) Current(_ context.Context, eventGUID string) (Block, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	stored, ok := w.current[eventGUID]
	if !ok || !stored.ok {
		return Block{}, false
	}
	return stored.block, true
}

func (w *Watcher) set(guid string, block Block) {
	w.mu.Lock()
	w.current[guid] = storedBlock{block: block, ok: true}
	w.mu.Unlock()
}

func (w *Watcher) run(ctx context.Context, guid string) {
	backoff := w.backoffMin
	for {
		if ctx.Err() != nil {
			return
		}
		w.seed(ctx, guid)
		handshook, err := w.connectOnce(ctx, guid)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Warn("split kit socket", "event", guid, "err", err)
		}
		wait := backoff
		if handshook {
			backoff = w.backoffMin
		} else {
			backoff *= 2
			if backoff > w.backoffMax {
				backoff = w.backoffMax
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Block returns the getblocks entry whose blockGuid matches.
func (w *Watcher) Block(ctx context.Context, eventGUID, blockGUID string) (Block, bool) {
	if strings.TrimSpace(eventGUID) == "" || strings.TrimSpace(blockGUID) == "" {
		return Block{}, false
	}
	blocks, err := fetchBlocks(ctx, w.http, w.host, eventGUID)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("split kit getblocks", "event", eventGUID, "err", err)
		}
		return Block{}, false
	}
	return matchBlock(blocks, blockGUID)
}

func (w *Watcher) seed(ctx context.Context, guid string) {
	blocks, err := fetchBlocks(ctx, w.http, w.host, guid)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("split kit getblocks", "event", guid, "err", err)
		}
		return
	}
	if len(blocks) == 0 {
		return
	}
	w.set(guid, blocks[0])
}

func fetchBlocks(ctx context.Context, hc *http.Client, host, guid string) ([]Block, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	host = strings.TrimRight(strings.TrimSpace(host), "/")
	guid = strings.TrimSpace(guid)
	if host == "" || guid == "" {
		return nil, fmt.Errorf("missing split kit host or event guid")
	}
	rawURL, err := blocksURL(host, guid)
	if err != nil {
		return nil, err
	}
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "lnzap/1.0")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMessageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxMessageBytes {
		return nil, fmt.Errorf("response too large")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var parsed struct {
		Blocks []Block `json:"blocks"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	return parsed.Blocks, nil
}

func matchBlock(blocks []Block, blockGUID string) (Block, bool) {
	for _, block := range blocks {
		if block.BlockGUID == blockGUID {
			return block, true
		}
	}
	return Block{}, false
}

func blocksURL(host, guid string) (string, error) {
	u, err := url.Parse(host)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/sk/getblocks"
	q := u.Query()
	q.Set("guid", guid)
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String(), nil
}
