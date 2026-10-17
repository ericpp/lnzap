package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func TestHandlerRoutes(t *testing.T) {
	var got url.Values
	var hits int
	var up *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/lnurlp/{user}", func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"callback":       up.URL + "/invoice",
			"maxSendable":    100000,
			"minSendable":    1000,
			"metadata":       `[["text/plain","hello"]]`,
			"tag":            "payRequest",
			"commentAllowed": 40,
			"allowsNostr":    true,
			"nostrPubkey":    "",
		})
	})
	mux.HandleFunc("GET /invoice", func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"pr":"lnbc1test","routes":[]}`)
	})
	up = httptest.NewTLSServer(mux)
	defer up.Close()

	sk := nostr.GeneratePrivateKey()
	pub, err := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	host := up.Listener.Addr().String()
	t.Setenv("LNZAP_CONFIG_YAML", `
public_url: "https://zap.example.com"
relays: ["wss://relay.example.com"]
users:
  alice:
    upstream: "alice@`+host+`"
    pubkey: "`+pub+`"
`)
	t.Setenv("NOSTR_PRIVATE_KEY", sk)
	httpClientOverride = up.Client()

	rec := httptest.NewRecorder()
	Handler(rec, httptest.NewRequest(http.MethodGet, "/api/index?lnzap_route=metadata&lnzap_user=Alice", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metadata %d %s", rec.Code, rec.Body.String())
	}
	var meta map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta["callback"] != "https://zap.example.com/lnurlp/alice/callback" {
		t.Fatalf("callback %#v", meta["callback"])
	}
	if meta["metadata"] != `[["text/plain","hello"]]` {
		t.Fatalf("metadata %#v", meta["metadata"])
	}

	rec = httptest.NewRecorder()
	Handler(rec, httptest.NewRequest(http.MethodGet, "/api/index?lnzap_route=callback&lnzap_user=alice&amount=1000&comment=hi&foo=bar", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("callback %d %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != `{"pr":"lnbc1test","routes":[]}` {
		t.Fatalf("body %s", rec.Body.String())
	}
	if got.Get("lnzap_route") != "" || got.Get("lnzap_user") != "" {
		t.Fatalf("routing params forwarded: %s", got.Encode())
	}
	if got.Get("amount") != "1000" || got.Get("comment") != "hi" || got.Get("foo") != "bar" {
		t.Fatalf("query %s", got.Encode())
	}
	if got.Get("nostr") == "" {
		t.Fatal("missing injected zap request")
	}
	var ev nostr.Event
	if err := json.Unmarshal([]byte(got.Get("nostr")), &ev); err != nil {
		t.Fatal(err)
	}
	ok, err := ev.CheckSignature()
	if err != nil || !ok || ev.Kind != 9734 {
		t.Fatalf("zap ok=%v err=%v kind=%d", ok, err, ev.Kind)
	}

	rec = httptest.NewRecorder()
	Handler(rec, httptest.NewRequest(http.MethodGet, "/api/index?lnzap_route=health", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"status":"ok"}` {
		t.Fatalf("health %d %s", rec.Code, rec.Body.String())
	}
	if hits != 1 {
		t.Fatalf("upstream metadata hits %d", hits)
	}
}
