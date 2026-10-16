package splitkit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestParseEvent(t *testing.T) {
	name, data, err := parseEvent(`42/event,["remoteValue",{"blockGuid":"b","title":"Show"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if name != "remoteValue" {
		t.Fatalf("name %s", name)
	}
	var block Block
	if err := json.Unmarshal(data, &block); err != nil {
		t.Fatal(err)
	}
	if block.BlockGUID != "b" || block.Title != "Show" {
		t.Fatalf("%#v", block)
	}
}

func TestWatcherRemoteValueAndReconnect(t *testing.T) {
	const guid = "event-1"
	var conns atomic.Int32
	seenLive := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sk/getblocks" {
			writeBlocks(w, Block{Title: "Seed", BlockGUID: "seed", FeedGUID: "feed"})
			return
		}
		if r.URL.Path != "/socket.io/" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("EIO") != "4" || r.URL.Query().Get("transport") != "websocket" || r.URL.Query().Get("event_id") != guid {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		n := conns.Add(1)
		if err := writeText(ctx, conn, `0{"sid":"abc","upgrades":[],"pingInterval":25000,"pingTimeout":20000}`); err != nil {
			return
		}
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Errorf("read connect: %v", err)
			return
		}
		if string(data) != "40/event," {
			t.Errorf("connect packet %q", data)
			return
		}
		if err := writeText(ctx, conn, "2"); err != nil {
			return
		}
		_, pong, err := conn.Read(ctx)
		if err != nil {
			t.Errorf("read pong: %v", err)
			return
		}
		if string(pong) != "3" {
			t.Errorf("pong %q", pong)
			return
		}
		title := "Live"
		id := "live-1"
		if n > 1 {
			title = "Next"
			id = "live-2"
		}
		payload, _ := json.Marshal(map[string]any{"title": title, "blockGuid": id, "feedGuid": "feed-live", "itemGuid": "item-live", "eventGuid": guid})
		if err := writeText(ctx, conn, `42/event,["remoteValue",`+string(payload)+`]`); err != nil {
			return
		}
		if n == 1 {
			select {
			case <-seenLive:
			case <-ctx.Done():
			}
			return
		}
		<-ctx.Done()
	}))
	defer srv.Close()

	w := NewWatcher(srv.URL, []string{guid, guid}, srv.Client())
	w.backoffMin = 20 * time.Millisecond
	w.backoffMax = 200 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	waitFor(t, func() bool {
		block, ok := w.Current(context.Background(), guid)
		return ok && block.Title == "Live" && block.BlockGUID == "live-1"
	})
	close(seenLive)
	waitFor(t, func() bool {
		block, ok := w.Current(context.Background(), guid)
		return ok && block.Title == "Next" && block.FeedGUID == "feed-live" && block.ItemGUID == "item-live"
	})
}

func TestWatcherEmptyRemoteValueFallsBack(t *testing.T) {
	const guid = "event-2"
	var ready atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sk/getblocks" {
			block := Block{Title: "Seed", BlockGUID: "seed"}
			if ready.Load() {
				block = Block{Title: "Fallback", BlockGUID: "fallback", EventAPI: "https://example/api"}
			}
			writeBlocks(w, block)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		if err := writeText(ctx, conn, `0{"sid":"abc","upgrades":[],"pingInterval":25000,"pingTimeout":20000}`); err != nil {
			return
		}
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
		ready.Store(true)
		if err := writeText(ctx, conn, `42/event,["remoteValue",{}]`); err != nil {
			return
		}
		<-ctx.Done()
	}))
	defer srv.Close()

	w := NewWatcher(srv.URL, []string{guid}, srv.Client())
	w.backoffMin = 20 * time.Millisecond
	w.backoffMax = 200 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	waitFor(t, func() bool {
		block, ok := w.Current(context.Background(), guid)
		return ok && block.Title == "Fallback" && block.BlockGUID == "fallback" && block.EventAPI == "https://example/api"
	})
}

func TestFlexString(t *testing.T) {
	var dest Destination
	if err := json.Unmarshal([]byte(`{"address":"a@b.com","split":"5","customKey":696969}`), &dest); err != nil {
		t.Fatal(err)
	}
	if dest.Split != "5" || dest.CustomKey != "696969" {
		t.Fatalf("%#v", dest)
	}
}

func writeBlocks(w http.ResponseWriter, blocks ...Block) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"blocks": blocks})
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}
