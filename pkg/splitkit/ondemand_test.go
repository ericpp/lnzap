package splitkit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestFetchCurrentRemoteValueCloses(t *testing.T) {
	const guid = "event-live"
	closed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sk/getblocks" {
			writeBlocks(w, Block{Title: "Seed", BlockGUID: "seed"})
			return
		}
		conn := acceptSocket(w, r)
		if conn == nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		if !handshake(ctx, conn) {
			return
		}
		payload, _ := json.Marshal(Block{Title: "Live", BlockGUID: "live", FeedGUID: "feed"})
		if err := writeText(ctx, conn, `42/event,["remoteValue",`+string(payload)+`]`); err != nil {
			return
		}
		_, _, _ = conn.Read(ctx)
		close(closed)
	}))
	defer srv.Close()

	block, ok := FetchCurrent(context.Background(), srv.URL, guid, time.Second)
	if !ok || block.Title != "Live" || block.BlockGUID != "live" || block.FeedGUID != "feed" {
		t.Fatalf("block %#v ok=%v", block, ok)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("socket stayed open after the first remoteValue")
	}
}

func TestFetchCurrentEmptyFallsBack(t *testing.T) {
	const guid = "event-empty"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sk/getblocks" {
			writeBlocks(w, Block{Title: "Fallback", BlockGUID: "fallback", EventAPI: "https://example/api"})
			return
		}
		conn := acceptSocket(w, r)
		if conn == nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		if !handshake(ctx, conn) {
			return
		}
		_ = writeText(ctx, conn, `42/event,["remoteValue",{}]`)
		<-ctx.Done()
	}))
	defer srv.Close()

	block, ok := FetchCurrent(context.Background(), srv.URL, guid, time.Second)
	if !ok || block.Title != "Fallback" || block.BlockGUID != "fallback" || block.EventAPI != "https://example/api" {
		t.Fatalf("block %#v ok=%v", block, ok)
	}
}

func TestFetchCurrentTimeoutFallsBack(t *testing.T) {
	const guid = "event-timeout"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sk/getblocks" {
			writeBlocks(w, Block{Title: "Seeded", BlockGUID: "seeded"})
			return
		}
		conn := acceptSocket(w, r)
		if conn == nil {
			return
		}
		defer conn.CloseNow()
		if !handshake(r.Context(), conn) {
			return
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	block, ok := FetchCurrent(context.Background(), srv.URL, guid, 40*time.Millisecond)
	if !ok || block.Title != "Seeded" || block.BlockGUID != "seeded" {
		t.Fatalf("block %#v ok=%v", block, ok)
	}
}

func TestOnDemandCache(t *testing.T) {
	const guid = "event-cache"
	var conns atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sk/getblocks" {
			writeBlocks(w, Block{Title: "Seed", BlockGUID: "seed"})
			return
		}
		conns.Add(1)
		conn := acceptSocket(w, r)
		if conn == nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		if !handshake(ctx, conn) {
			return
		}
		payload, _ := json.Marshal(Block{Title: "Cached", BlockGUID: "cached"})
		_ = writeText(ctx, conn, `42/event,["remoteValue",`+string(payload)+`]`)
		<-ctx.Done()
	}))
	defer srv.Close()

	lookup := NewOnDemand(srv.URL, time.Second, time.Minute)
	for i := 0; i < 2; i++ {
		block, ok := lookup.Current(context.Background(), guid)
		if !ok || block.Title != "Cached" {
			t.Fatalf("call %d block %#v ok=%v", i, block, ok)
		}
	}
	if got := conns.Load(); got != 1 {
		t.Fatalf("connections %d", got)
	}
}

func TestOnDemandBlock(t *testing.T) {
	const guid = "event-pin"
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sk/getblocks" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		writeBlocks(w,
			Block{Title: "First", BlockGUID: "first"},
			Block{Title: "Pinned", BlockGUID: "pinned", FeedGUID: "feed"},
		)
	}))
	defer srv.Close()

	lookup := NewOnDemand(srv.URL, time.Second, time.Minute)
	for i := 0; i < 2; i++ {
		block, ok := lookup.Block(context.Background(), guid, "pinned")
		if !ok || block.Title != "Pinned" || block.FeedGUID != "feed" {
			t.Fatalf("call %d block %#v ok=%v", i, block, ok)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits %d", got)
	}
	if _, ok := lookup.Block(context.Background(), guid, "missing"); ok {
		t.Fatal("expected miss")
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("hits after miss %d", got)
	}
}

func TestOnDemandSingleflight(t *testing.T) {
	const guid = "event-flight"
	var conns atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/socket.io/" {
			http.NotFound(w, r)
			return
		}
		n := conns.Add(1)
		conn := acceptSocket(w, r)
		if conn == nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		if !handshake(ctx, conn) {
			return
		}
		if n == 1 {
			<-release
		}
		payload, _ := json.Marshal(Block{Title: "Shared", BlockGUID: "shared"})
		_ = writeText(ctx, conn, `42/event,["remoteValue",`+string(payload)+`]`)
		<-ctx.Done()
	}))
	defer srv.Close()

	lookup := NewOnDemand(srv.URL, time.Second, time.Minute)
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			block, ok := lookup.Current(context.Background(), guid)
			if !ok || block.BlockGUID != "shared" {
				errCh <- errBadBlock
			}
		}()
	}
	close(start)
	waitFor(t, func() bool { return conns.Load() == 1 })
	time.Sleep(80 * time.Millisecond)
	if got := conns.Load(); got != 1 {
		t.Fatalf("connections before release %d", got)
	}
	releaseOnce.Do(func() { close(release) })
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

var errBadBlock = errString("unexpected block")

type errString string

func (e errString) Error() string { return string(e) }

func acceptSocket(w http.ResponseWriter, r *http.Request) *websocket.Conn {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return nil
	}
	return conn
}

func handshake(ctx context.Context, conn *websocket.Conn) bool {
	if err := writeText(ctx, conn, `0{"sid":"abc","upgrades":[],"pingInterval":25000,"pingTimeout":20000}`); err != nil {
		return false
	}
	_, data, err := conn.Read(ctx)
	return err == nil && string(data) == "40/event,"
}
