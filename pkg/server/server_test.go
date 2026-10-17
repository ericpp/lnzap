package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"

	"lnzap/pkg/config"
	"lnzap/pkg/lnurl"
	"lnzap/pkg/splitkit"
)

const metadata = `[["text/plain","hello world"],["text/identifier","alice@example.com"]]`

type fakeBlocks struct {
	block  splitkit.Block
	ok     bool
	pinned map[string]splitkit.Block
}

func (f fakeBlocks) Current(context.Context, string) (splitkit.Block, bool) {
	return f.block, f.ok
}

func (f fakeBlocks) Block(_ context.Context, _, blockGUID string) (splitkit.Block, bool) {
	block, ok := f.pinned[blockGUID]
	return block, ok
}

type upstreamCapture struct {
	mu        sync.Mutex
	nostr     string
	amount    string
	comment   string
	foo       string
	payerdata string
	raw       url.Values
	metadata  int
}

func (c *upstreamCapture) snapshot() (nostrParam, amount, comment, foo string, metadataHits int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nostr, c.amount, c.comment, c.foo, c.metadata
}

func (c *upstreamCapture) payerDataParam() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.payerdata
}

func (c *upstreamCapture) has(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.raw[key]
	return ok
}

func TestMetadataRewrite(t *testing.T) {
	h, up, _ := newProxy(t, true, "", fakeBlocks{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/lnurlp/Alice", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("missing cors")
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["metadata"] != metadata {
		t.Fatalf("metadata %#v", body["metadata"])
	}
	if body["callback"] != "https://zap.example.com/lnurlp/alice/callback" {
		t.Fatalf("callback %#v", body["callback"])
	}
	if body["minSendable"].(float64) != 1000 || body["allowsNostr"] != true {
		t.Fatalf("body %#v", body)
	}
	payerData, _ := body["payerData"].(map[string]any)
	nameSpec, _ := payerData["name"].(map[string]any)
	if nameSpec["mandatory"] != false {
		t.Fatalf("payerData %#v", body["payerData"])
	}
	_, _, _, _, hits := up.snapshot()
	if hits != 1 {
		t.Fatalf("metadata hits %d", hits)
	}
}

func TestInjectsZapRequest(t *testing.T) {
	recipientSK := nostr.GeneratePrivateKey()
	recipient, err := nostr.GetPublicKey(recipientSK)
	if err != nil {
		t.Fatal(err)
	}
	blocks := fakeBlocks{
		ok: true,
		block: splitkit.Block{
			Title:     "Live Show",
			FeedGUID:  "feed-1",
			ItemGUID:  "item-1",
			EventGUID: "event-1",
			BlockGUID: "block-1",
			EventAPI:  "https://curiohoster.com/api/sk/event/lookup",
		},
	}
	h, up, signer := newProxy(t, true, recipient, blocks)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?amount=1000&comment=thanks&foo=bar", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != `{"pr":"lnbc1test","routes":[]}` {
		t.Fatalf("body %s", rec.Body.String())
	}
	gotNostr, amount, comment, foo, hits := up.snapshot()
	if amount != "1000" || comment != "thanks" || foo != "bar" {
		t.Fatalf("query amount=%s comment=%s foo=%s", amount, comment, foo)
	}
	if hits != 1 {
		t.Fatalf("metadata hits %d", hits)
	}
	var ev nostr.Event
	if err := json.Unmarshal([]byte(gotNostr), &ev); err != nil {
		t.Fatal(err)
	}
	ok, err := ev.CheckSignature()
	if err != nil || !ok {
		t.Fatalf("signature ok=%v err=%v", ok, err)
	}
	if ev.Kind != 9734 || ev.PubKey != signer || ev.Content != "thanks" {
		t.Fatalf("event %#v", ev)
	}
	if tagValue(ev, "p") != recipient || tagValue(ev, "amount") != "1000" {
		t.Fatalf("tags %#v", ev.Tags)
	}
	if tagValue(ev, "payerName") != "" {
		t.Fatalf("payerName %s", tagValue(ev, "payerName"))
	}
	decoded, err := lnurl.DecodeLNURL(tagValue(ev, "lnurl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(decoded, "/.well-known/lnurlp/alice") {
		t.Fatalf("lnurl %s", decoded)
	}
	if tagValue(ev, "title") != "Live Show" || tagValue(ev, "blockGuid") != "block-1" || tagValue(ev, "eventGuid") != "event-1" {
		t.Fatalf("block tags %#v", ev.Tags)
	}
	is := allTags(ev, "i")
	if len(is) != 2 || is[0][1] != "podcast:guid:feed-1" || is[1][1] != "podcast:item:guid:item-1" {
		t.Fatalf("i tags %#v", is)
	}
	ks := allTags(ev, "k")
	if len(ks) != 2 || ks[0][1] != "podcast:guid" || ks[1][1] != "podcast:item:guid" {
		t.Fatalf("k tags %#v", ks)
	}
}

func TestCapturesPayerName(t *testing.T) {
	h, up, _ := newProxy(t, true, recipientKey(t), fakeBlocks{})
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("payerdata", `{"name":"  Bob  "}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	ev := decodeZap(t, up)
	if tagValue(ev, "payerName") != "Bob" {
		t.Fatalf("payerName %q tags %#v", tagValue(ev, "payerName"), ev.Tags)
	}
	if got := up.payerDataParam(); got != "" {
		t.Fatalf("forwarded payerdata %s", got)
	}
}

func TestTruncatesPayerName(t *testing.T) {
	h, up, _ := newProxy(t, true, recipientKey(t), fakeBlocks{})
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("payerdata", `{"name":"`+strings.Repeat("é", 120)+`"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	ev := decodeZap(t, up)
	if got := []rune(tagValue(ev, "payerName")); len(got) != 100 {
		t.Fatalf("payerName len %d", len(got))
	}
}

func TestMalformedPayerDataIgnored(t *testing.T) {
	h, up, _ := newProxy(t, true, recipientKey(t), fakeBlocks{})
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("payerdata", `{`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	ev := decodeZap(t, up)
	if tagValue(ev, "payerName") != "" {
		t.Fatalf("payerName %s", tagValue(ev, "payerName"))
	}
	if got := up.payerDataParam(); got != "" {
		t.Fatalf("forwarded payerdata %s", got)
	}
}

func TestForwardsPayerDataWhenUpstreamAccepts(t *testing.T) {
	h, up, _ := newProxyWith(t, true, recipientKey(t), fakeBlocks{}, map[string]any{
		"email": map[string]any{"mandatory": false},
	})
	meta := httptest.NewRecorder()
	h.ServeHTTP(meta, httptest.NewRequest(http.MethodGet, "/.well-known/lnurlp/alice", nil))
	if meta.Code != http.StatusOK {
		t.Fatalf("status %d body %s", meta.Code, meta.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(meta.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	payerData, _ := body["payerData"].(map[string]any)
	if _, ok := payerData["email"]; !ok {
		t.Fatalf("payerData %#v", body["payerData"])
	}
	nameSpec, _ := payerData["name"].(map[string]any)
	if nameSpec["mandatory"] != false {
		t.Fatalf("payerData %#v", body["payerData"])
	}

	raw := `{"name":"Bob","email":"bob@example.com"}`
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("payerdata", raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	ev := decodeZap(t, up)
	if tagValue(ev, "payerName") != "Bob" {
		t.Fatalf("payerName %s", tagValue(ev, "payerName"))
	}
	if got := up.payerDataParam(); got != raw {
		t.Fatalf("payerdata %s", got)
	}
}

func TestMetadataRejectsUpstreamWithoutNostr(t *testing.T) {
	h, _, _ := newProxy(t, false, "", fakeBlocks{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/lnurlp/alice", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if reason := errorReason(t, rec); reason != "upstream does not support nostr zaps" {
		t.Fatalf("reason %s", reason)
	}
}

func TestKeepsUpstreamPayerName(t *testing.T) {
	h, _, _ := newProxyWith(t, true, "", fakeBlocks{}, map[string]any{
		"name":  map[string]any{"mandatory": true},
		"email": map[string]any{"mandatory": false},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/lnurlp/alice", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	payerData, _ := body["payerData"].(map[string]any)
	nameSpec, _ := payerData["name"].(map[string]any)
	email, _ := payerData["email"].(map[string]any)
	if nameSpec["mandatory"] != true || email["mandatory"] != false {
		t.Fatalf("payerData %#v", body["payerData"])
	}
}

func TestRSSPaymentSkipsZap(t *testing.T) {
	comment := "rss::payment::feed-guid::item-guid::100"
	h, up, _ := newProxy(t, true, recipientKey(t), showBlocks())
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("comment", comment)
	q.Set("foo", "bar")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	got, amount, gotComment, foo, _ := up.snapshot()
	if got != "" {
		t.Fatalf("constructed zap %s", got)
	}
	if amount != "1000" || gotComment != comment || foo != "bar" {
		t.Fatalf("amount=%s comment=%s foo=%s", amount, gotComment, foo)
	}
}

func TestRSSPaymentSkipsSignedZapComment(t *testing.T) {
	comment := "thanks rss::payment::feed::item"
	supplied := `{"id":"payer","kind":9734}`
	h, up, _ := newProxySetup(t, true, "", showBlocks(), proxySetup{signedZap: config.SignedZapComment})
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("comment", comment)
	q.Set("nostr", supplied)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	got, _, gotComment, _, _ := up.snapshot()
	if got != supplied {
		t.Fatalf("nostr %s", got)
	}
	if gotComment != comment {
		t.Fatalf("comment %q", gotComment)
	}
}

func TestRSSPaymentSkipsSignedZapReplace(t *testing.T) {
	raw, _ := signedZap(t, "from wallet", nostr.Tags{{"e", "evt1"}})
	comment := "rss::payment::feed::item"
	h, up, _ := newProxySetup(t, true, recipientKey(t), showBlocks(), proxySetup{signedZap: config.SignedZapReplace})
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("comment", comment)
	q.Set("nostr", raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	got, _, gotComment, _, _ := up.snapshot()
	if got != raw {
		t.Fatal("wallet zap was rewritten")
	}
	if gotComment != comment {
		t.Fatalf("comment %q", gotComment)
	}
}

func TestV4VPaymentSkipsZap(t *testing.T) {
	comments := []string{
		"V4V: https://boost.podcastguru.io/boost/we3Jw3QS payment::stream",
		"V4V: https://boost.podcastguru.io/boost/we3Jw3QS payment::boost",
		"V4V: https://boost.podcastguru.io/boost/we3Jw3QS",
	}
	for _, comment := range comments {
		t.Run(comment, func(t *testing.T) {
			h, up, _ := newProxy(t, true, recipientKey(t), showBlocks())
			q := url.Values{}
			q.Set("amount", "1000")
			q.Set("comment", comment)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
			got, _, gotComment, _, _ := up.snapshot()
			if got != "" {
				t.Fatalf("constructed zap %s", got)
			}
			if gotComment != comment {
				t.Fatalf("comment %q", gotComment)
			}
		})
	}
}

func TestRSSPaymentPassesThroughWithoutNostr(t *testing.T) {
	comment := "rss::payment::feed::item"
	h, up, _ := newProxy(t, false, "", fakeBlocks{})
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("comment", comment)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	got, _, gotComment, _, _ := up.snapshot()
	if got != "" {
		t.Fatalf("constructed zap %s", got)
	}
	if gotComment != comment {
		t.Fatalf("comment %q", gotComment)
	}
}

func TestPassesThroughNostrParam(t *testing.T) {
	supplied := `{"id":"payer","kind":9734}`
	h, up, _ := newProxy(t, true, "", fakeBlocks{ok: true, block: splitkit.Block{Title: "Live", BlockGUID: "b", FeedGUID: "feed"}})
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("comment", "thanks")
	q.Set("nostr", supplied)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	got, _, comment, _, _ := up.snapshot()
	if got != supplied {
		t.Fatalf("nostr %s", got)
	}
	if comment != "thanks" {
		t.Fatalf("comment %s", comment)
	}
}

func TestSignedZapComment(t *testing.T) {
	supplied := `{"id":"payer","kind":9734}`
	blocks := showBlocks()
	h, up, _ := newProxySetup(t, true, "", blocks, proxySetup{signedZap: config.SignedZapComment})
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("comment", "thanks")
	q.Set("nostr", supplied)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	got, _, comment, _, _ := up.snapshot()
	if got != supplied {
		t.Fatalf("nostr %s", got)
	}
	want := "thanks Live Show | podcast:guid:feed-1 podcast:item:guid:item-1"
	if comment != want {
		t.Fatalf("comment %q", comment)
	}
}

func TestSignedZapCommentTrims(t *testing.T) {
	blocks := showBlocks()
	h, up, _ := newProxySetup(t, true, "", blocks, proxySetup{
		signedZap:         config.SignedZapComment,
		commentAllowed:    10,
		commentAllowedSet: true,
	})
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("comment", "thanks")
	q.Set("nostr", `{"id":"payer"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	_, _, comment, _, _ := up.snapshot()
	if comment != "thanks Liv" {
		t.Fatalf("comment %q", comment)
	}
}

func TestSignedZapCommentSkipsWhenDisallowed(t *testing.T) {
	h, up, _ := newProxySetup(t, true, "", showBlocks(), proxySetup{
		signedZap:         config.SignedZapComment,
		commentAllowed:    0,
		commentAllowedSet: true,
	})
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("comment", "thanks")
	q.Set("nostr", `{"id":"payer"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	_, _, comment, _, _ := up.snapshot()
	if comment != "thanks" {
		t.Fatalf("comment %q", comment)
	}
}

func TestSignedZapCommentSkipsWithoutBlock(t *testing.T) {
	h, up, _ := newProxySetup(t, true, "", fakeBlocks{}, proxySetup{signedZap: config.SignedZapComment})
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("comment", "thanks")
	q.Set("nostr", `{"id":"payer"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	_, _, comment, _, _ := up.snapshot()
	if comment != "thanks" {
		t.Fatalf("comment %q", comment)
	}
}

func TestSignedZapReplace(t *testing.T) {
	raw, payer := signedZap(t, "from wallet", nostr.Tags{
		{"e", "evt1", "wss://relay.example.com", "root"},
		{"a", "30023:pub:slug"},
		{"p", "someone-else"},
	})
	h, up, signer := newProxySetup(t, true, recipientKey(t), showBlocks(), proxySetup{signedZap: config.SignedZapReplace})
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("comment", "ignored")
	q.Set("nostr", raw)
	q.Set("senderName", "Carol")
	q.Set("clientId", "client-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	got, _, comment, _, _ := up.snapshot()
	if got == raw {
		t.Fatal("wallet zap was forwarded")
	}
	if comment != "ignored" {
		t.Fatalf("comment %s", comment)
	}
	var ev nostr.Event
	if err := json.Unmarshal([]byte(got), &ev); err != nil {
		t.Fatal(err)
	}
	ok, err := ev.CheckSignature()
	if err != nil || !ok {
		t.Fatalf("signature ok=%v err=%v", ok, err)
	}
	if ev.PubKey != signer || ev.Content != "from wallet" || tagValue(ev, "amount") != "1000" {
		t.Fatalf("event pubkey=%s content=%q tags=%#v", ev.PubKey, ev.Content, ev.Tags)
	}
	e := allTags(ev, "e")
	if len(e) != 1 || len(e[0]) != 4 || e[0][1] != "evt1" || e[0][3] != "root" {
		t.Fatalf("e %#v", e)
	}
	if tagValue(ev, "a") != "30023:pub:slug" || tagValue(ev, "senderPubkey") != payer {
		t.Fatalf("tags %#v", ev.Tags)
	}
	if tagValue(ev, "payerName") != "Carol" || tagValue(ev, "clientId") != "client-1" || tagValue(ev, "title") != "Live Show" {
		t.Fatalf("tags %#v", ev.Tags)
	}
	if up.has("senderName") || up.has("clientId") || up.has("blockGuid") {
		t.Fatal("splitbox params were forwarded")
	}
}

func TestZapTargetOnUnsignedRequest(t *testing.T) {
	target := nostr.Tag{"a", "30023:pub:slug", "wss://relay.example.com"}
	h, up, _ := newProxySetup(t, true, recipientKey(t), fakeBlocks{}, proxySetup{zapTarget: target})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?amount=1000", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	ev := decodeZap(t, up)
	a := allTags(ev, "a")
	if len(a) != 1 || len(a[0]) != 3 || a[0][1] != "30023:pub:slug" || a[0][2] != "wss://relay.example.com" {
		t.Fatalf("a %#v", a)
	}
}

func TestZapTargetReplaceKeepsWalletTags(t *testing.T) {
	raw, _ := signedZap(t, "from wallet", nostr.Tags{
		{"e", "evt1", "wss://relay.example.com", "root"},
	})
	h, up, _ := newProxySetup(t, true, recipientKey(t), fakeBlocks{}, proxySetup{
		signedZap: config.SignedZapReplace,
		zapTarget: nostr.Tag{"a", "30023:pub:slug"},
	})
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("nostr", raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	ev := decodeZap(t, up)
	e := allTags(ev, "e")
	if len(e) != 1 || e[0][1] != "evt1" || tagValue(ev, "a") != "" {
		t.Fatalf("tags %#v", ev.Tags)
	}
}

func TestZapTargetReplaceFallback(t *testing.T) {
	raw, payer := signedZap(t, "from wallet", nostr.Tags{
		{"p", "someone"},
	})
	h, up, _ := newProxySetup(t, true, recipientKey(t), fakeBlocks{}, proxySetup{
		signedZap: config.SignedZapReplace,
		zapTarget: nostr.Tag{"e", "evt-target"},
	})
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("nostr", raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	ev := decodeZap(t, up)
	if tagValue(ev, "e") != "evt-target" || tagValue(ev, "senderPubkey") != payer {
		t.Fatalf("tags %#v", ev.Tags)
	}
}

func TestSignedZapReplaceRejectsBadSignature(t *testing.T) {
	raw, _ := signedZap(t, "hi", nil)
	raw = strings.Replace(raw, `"content":"hi"`, `"content":"no"`, 1)
	h, _, _ := newProxySetup(t, true, recipientKey(t), fakeBlocks{}, proxySetup{signedZap: config.SignedZapReplace})
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("nostr", raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if reason := errorReason(t, rec); reason != "invalid nostr zap request" {
		t.Fatalf("reason %s", reason)
	}
}

func TestBlockGUIDSelectsBlock(t *testing.T) {
	blocks := showBlocks()
	blocks.pinned = map[string]splitkit.Block{
		"pinned-1": {
			Title:     "Pinned",
			FeedGUID:  "feed-pinned",
			ItemGUID:  "item-pinned",
			BlockGUID: "pinned-1",
		},
	}
	h, up, _ := newProxy(t, true, recipientKey(t), blocks)
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("blockGuid", "pinned-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	ev := decodeZap(t, up)
	if tagValue(ev, "title") != "Pinned" || tagValue(ev, "blockGuid") != "pinned-1" {
		t.Fatalf("tags %#v", ev.Tags)
	}
	is := allTags(ev, "i")
	if len(is) != 2 || is[0][1] != "podcast:guid:feed-pinned" || is[1][1] != "podcast:item:guid:item-pinned" {
		t.Fatalf("i tags %#v", is)
	}
	if up.has("blockGuid") {
		t.Fatal("blockGuid was forwarded")
	}
}

func TestBlockGUIDFallsBack(t *testing.T) {
	h, up, _ := newProxy(t, true, recipientKey(t), showBlocks())
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("blockGuid", "missing")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	ev := decodeZap(t, up)
	if tagValue(ev, "title") != "Live Show" || tagValue(ev, "blockGuid") != "block-1" {
		t.Fatalf("tags %#v", ev.Tags)
	}
}

func TestSenderNameFallback(t *testing.T) {
	h, up, _ := newProxy(t, true, recipientKey(t), fakeBlocks{})
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("senderName", "  Carol  ")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	ev := decodeZap(t, up)
	if tagValue(ev, "payerName") != "Carol" {
		t.Fatalf("payerName %q", tagValue(ev, "payerName"))
	}
	if up.has("senderName") {
		t.Fatal("senderName was forwarded")
	}

	q.Set("payerdata", `{"name":"Bob"}`)
	q.Set("senderName", "Carol")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	ev = decodeZap(t, up)
	if tagValue(ev, "payerName") != "Bob" {
		t.Fatalf("payerName %q", tagValue(ev, "payerName"))
	}
}

func TestSenderNameTruncated(t *testing.T) {
	h, up, _ := newProxy(t, true, recipientKey(t), fakeBlocks{})
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("senderName", strings.Repeat("é", 120))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	ev := decodeZap(t, up)
	if got := []rune(tagValue(ev, "payerName")); len(got) != 100 {
		t.Fatalf("payerName len %d", len(got))
	}
}

func TestClientIDTag(t *testing.T) {
	h, up, _ := newProxy(t, true, recipientKey(t), fakeBlocks{})
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("clientId", "client-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	ev := decodeZap(t, up)
	if tagValue(ev, "clientId") != "client-1" {
		t.Fatalf("tags %#v", ev.Tags)
	}
	if up.has("clientId") {
		t.Fatal("clientId was forwarded")
	}
}

func TestDropsSplitboxParams(t *testing.T) {
	h, up, _ := newProxy(t, true, "", fakeBlocks{})
	q := url.Values{}
	q.Set("amount", "1000")
	q.Set("nostr", `{"id":"payer"}`)
	q.Set("blockGuid", "pinned-1")
	q.Set("senderName", "Carol")
	q.Set("clientId", "client-1")
	q.Set("foo", "bar")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if up.has("blockGuid") || up.has("senderName") || up.has("clientId") {
		t.Fatal("splitbox params were forwarded")
	}
	_, _, _, foo, _ := up.snapshot()
	if foo != "bar" {
		t.Fatalf("foo %s", foo)
	}
}

func TestCallbackRejectsUpstreamWithoutNostr(t *testing.T) {
	h, up, _ := newProxy(t, false, "", fakeBlocks{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?amount=1000", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if reason := errorReason(t, rec); reason != "upstream does not support nostr zaps" {
		t.Fatalf("reason %s", reason)
	}
	if got, _, _, _, hits := up.snapshot(); got != "" || hits != 1 {
		t.Fatalf("nostr %q hits %d", got, hits)
	}
}

func TestUnknownUser(t *testing.T) {
	h, _, _ := newProxy(t, true, "", fakeBlocks{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/lnurlp/bob", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ERROR" {
		t.Fatalf("%#v", body)
	}
}

func TestAmountOutOfRange(t *testing.T) {
	h, _, _ := newProxy(t, true, "", fakeBlocks{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lnurlp/alice/callback?amount=100", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["reason"] != "amount out of range" {
		t.Fatalf("%#v", body)
	}
}

func TestHealthz(t *testing.T) {
	h, _, _ := newProxy(t, true, "", fakeBlocks{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"status":"ok"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func recipientKey(t *testing.T) string {
	t.Helper()
	pub, err := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

type proxySetup struct {
	signedZap         string
	payerData         map[string]any
	commentAllowed    int
	commentAllowedSet bool
	zapTarget         nostr.Tag
}

func newProxy(t *testing.T, allowsNostr bool, configuredPubkey string, blocks BlockSource) (http.Handler, *upstreamCapture, string) {
	t.Helper()
	return newProxySetup(t, allowsNostr, configuredPubkey, blocks, proxySetup{})
}

func newProxyWith(t *testing.T, allowsNostr bool, configuredPubkey string, blocks BlockSource, payerData map[string]any) (http.Handler, *upstreamCapture, string) {
	t.Helper()
	return newProxySetup(t, allowsNostr, configuredPubkey, blocks, proxySetup{payerData: payerData})
}

func newProxySetup(t *testing.T, allowsNostr bool, configuredPubkey string, blocks BlockSource, setup proxySetup) (http.Handler, *upstreamCapture, string) {
	t.Helper()
	cap := &upstreamCapture{}
	var up *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/lnurlp/{user}", func(w http.ResponseWriter, r *http.Request) {
		cap.mu.Lock()
		cap.metadata++
		cap.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		allowed := 255
		if setup.commentAllowedSet {
			allowed = setup.commentAllowed
		}
		payload := map[string]any{
			"callback":       up.URL + "/invoice?foo=keep",
			"maxSendable":    100000000,
			"minSendable":    1000,
			"metadata":       metadata,
			"tag":            "payRequest",
			"commentAllowed": allowed,
			"allowsNostr":    allowsNostr,
			"nostrPubkey":    configuredPubkey,
		}
		if setup.payerData != nil {
			payload["payerData"] = setup.payerData
		}
		_ = json.NewEncoder(w).Encode(payload)
	})
	mux.HandleFunc("GET /invoice", func(w http.ResponseWriter, r *http.Request) {
		cap.mu.Lock()
		cap.raw = r.URL.Query()
		cap.nostr = cap.raw.Get("nostr")
		cap.amount = cap.raw.Get("amount")
		cap.comment = cap.raw.Get("comment")
		cap.foo = cap.raw.Get("foo")
		cap.payerdata = cap.raw.Get("payerdata")
		cap.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"pr":"lnbc1test","routes":[]}`)
	})
	up = httptest.NewTLSServer(mux)
	t.Cleanup(up.Close)

	host := up.Listener.Addr().String()
	sk := nostr.GeneratePrivateKey()
	signer, err := nostr.GetPublicKey(sk)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Listen:       ":0",
		PublicURL:    "https://zap.example.com",
		Relays:       []string{"wss://relay.example.com"},
		CacheTTL:     config.Duration(time.Minute),
		SplitKitHost: "https://curiohoster.com",
		SignedZap:    setup.signedZap,
		Users: map[string]config.User{
			"alice": {
				Upstream:  "alice@" + host,
				Pubkey:    "",
				EventGUID: "event-1",
			},
		},
	}
	if configuredPubkey != "" || setup.zapTarget != nil {
		user := cfg.Users["alice"]
		if configuredPubkey != "" {
			user.Pubkey = configuredPubkey
		}
		user.ZapTargetTag = setup.zapTarget
		cfg.Users["alice"] = user
	}
	handler := New(cfg, lnurl.NewClient(time.Minute, up.Client()), sk, blocks)
	return handler, cap, signer
}

func showBlocks() fakeBlocks {
	return fakeBlocks{
		ok: true,
		block: splitkit.Block{
			Title:     "Live Show",
			FeedGUID:  "feed-1",
			ItemGUID:  "item-1",
			EventGUID: "event-1",
			BlockGUID: "block-1",
		},
	}
}

func signedZap(t *testing.T, content string, tags nostr.Tags) (string, string) {
	t.Helper()
	sk := nostr.GeneratePrivateKey()
	ev := nostr.Event{
		Kind:      9734,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Content:   content,
		Tags:      tags,
	}
	if err := ev.Sign(sk); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return string(body), ev.PubKey
}

func errorReason(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body["reason"]
}

func decodeZap(t *testing.T, up *upstreamCapture) nostr.Event {
	t.Helper()
	got, _, _, _, _ := up.snapshot()
	var ev nostr.Event
	if err := json.Unmarshal([]byte(got), &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

func tagValue(ev nostr.Event, name string) string {
	for _, item := range ev.Tags {
		if len(item) > 0 && item[0] == name && len(item) > 1 {
			return item[1]
		}
	}
	return ""
}

func allTags(ev nostr.Event, name string) []nostr.Tag {
	var out []nostr.Tag
	for _, item := range ev.Tags {
		if len(item) > 0 && item[0] == name {
			out = append(out, item)
		}
	}
	return out
}
