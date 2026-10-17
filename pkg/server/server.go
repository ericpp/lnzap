// Package server exposes LUD-16 lightning address endpoints that proxy to an
// upstream LNURL server and attach a zap request when the payer did not.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/nbd-wtf/go-nostr"

	"lnzap/pkg/config"
	"lnzap/pkg/lnurl"
	"lnzap/pkg/nostrkey"
	"lnzap/pkg/splitkit"
	"lnzap/pkg/zap"
)

// BlockSource supplies Split Kit blocks for an event guid.
type BlockSource interface {
	Current(ctx context.Context, eventGUID string) (splitkit.Block, bool)
	Block(ctx context.Context, eventGUID, blockGUID string) (splitkit.Block, bool)
}

// Server is the lightning address proxy.
type Server struct {
	cfg    *config.Config
	lnurl  *lnurl.Client
	sk     string
	blocks BlockSource
}

// NewServer builds the proxy used by the standalone server and the Vercel handler.
func NewServer(cfg *config.Config, client *lnurl.Client, privateKey string, blocks BlockSource) *Server {
	return &Server{cfg: cfg, lnurl: client, sk: privateKey, blocks: blocks}
}

// New returns an HTTP handler for the proxy.
func New(cfg *config.Config, client *lnurl.Client, privateKey string, blocks BlockSource) http.Handler {
	s := NewServer(cfg, client, privateKey, blocks)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/lnurlp/{user}", s.handleMetadata)
	mux.HandleFunc("GET /lnurlp/{user}/callback", s.handleCallback)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	return withCORS(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.Health(w, r)
}

// Health writes the process health response.
func (s *Server) Health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, []byte(`{"status":"ok"}`))
}

func (s *Server) handleMetadata(w http.ResponseWriter, r *http.Request) {
	s.Metadata(w, r, r.PathValue("user"))
}

// Metadata writes the proxied LNURL pay request for a local username.
// When the upstream accepts nostr zaps or payer data, the response asks for the payer's name.
func (s *Server) Metadata(w http.ResponseWriter, r *http.Request, username string) {
	name, user, ok := s.lookup(username)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown lightning address")
		return
	}
	params, raw, err := s.lnurl.Fetch(r.Context(), user.Upstream)
	if err != nil {
		slog.Warn("lnurl fetch", "user", name, "err", err)
		writeError(w, http.StatusBadGateway, "upstream lnurl lookup failed")
		return
	}
	if !params.AllowsNostr {
		slog.Warn("lnurl nostr", "user", name, "reason", "upstream does not support nostr zaps")
		writeError(w, http.StatusBadGateway, "upstream does not support nostr zaps")
		return
	}
	body, err := lnurl.RewriteCallback(raw, s.cfg.CallbackURL(name))
	if err != nil {
		slog.Warn("lnurl rewrite", "user", name, "err", err)
		writeError(w, http.StatusBadGateway, "upstream lnurl response was invalid")
		return
	}
	body, err = lnurl.AddPayerName(body)
	if err != nil {
		slog.Warn("payer data", "user", name, "err", err)
		writeError(w, http.StatusBadGateway, "upstream lnurl response was invalid")
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	s.Callback(w, r, r.PathValue("user"), nil)
}

// Callback proxies an invoice request for a local username.
// drop lists query keys that are removed before the upstream request is built.
func (s *Server) Callback(w http.ResponseWriter, r *http.Request, username string, drop []string) {
	name, user, ok := s.lookup(username)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown lightning address")
		return
	}
	params, _, err := s.lnurl.Fetch(r.Context(), user.Upstream)
	if err != nil {
		slog.Warn("lnurl fetch", "user", name, "err", err)
		writeError(w, http.StatusBadGateway, "upstream lnurl lookup failed")
		return
	}
	passthrough := rssPaymentComment(r.URL.Query().Get("comment"))
	if !passthrough && !params.AllowsNostr {
		slog.Warn("lnurl nostr", "user", name, "reason", "upstream does not support nostr zaps")
		writeError(w, http.StatusBadGateway, "upstream does not support nostr zaps")
		return
	}
	amount := r.URL.Query().Get("amount")
	msat, err := strconv.ParseInt(amount, 10, 64)
	if err != nil || amount == "" || msat < 0 {
		writeError(w, http.StatusBadRequest, "invalid amount")
		return
	}
	if msat < params.MinSendable || msat > params.MaxSendable {
		writeError(w, http.StatusBadRequest, "amount out of range")
		return
	}

	incoming := cloneQuery(r.URL.Query())
	for _, key := range drop {
		incoming.Del(key)
	}
	blockGUID := strings.TrimSpace(incoming.Get("blockGuid"))
	sender := limitName(incoming.Get("senderName"))
	clientID := strings.TrimSpace(incoming.Get("clientId"))
	incoming.Del("blockGuid")
	incoming.Del("senderName")
	incoming.Del("clientId")
	payer := payerName(incoming)
	if payer == "" {
		payer = sender
	}
	if len(params.PayerData) == 0 {
		incoming.Del("payerdata")
	}
	var injected string
	if !passthrough {
		var ok bool
		injected, ok = s.applyZap(w, r, name, user, params, msat, payer, blockGUID, clientID, incoming)
		if !ok {
			return
		}
	}

	target, err := mergeQuery(params.Callback, incoming, injected)
	if err != nil {
		slog.Warn("upstream callback", "user", name, "err", err)
		writeError(w, http.StatusBadGateway, "upstream callback was invalid")
		return
	}
	status, body, err := s.lnurl.Get(r.Context(), target)
	if err != nil {
		slog.Warn("upstream invoice", "user", name, "err", err)
		writeError(w, http.StatusBadGateway, "upstream invoice request failed")
		return
	}
	writeJSON(w, status, body)
}

// applyZap decides the upstream nostr and comment fields.
// ok is false when a response has already been written.
func (s *Server) applyZap(w http.ResponseWriter, r *http.Request, name string, user config.User, params lnurl.PayParams, msat int64, payer, blockGUID, clientID string, incoming url.Values) (string, bool) {
	supplied := incoming.Get("nostr")
	if supplied == "" {
		nostrJSON, err := s.zapRequest(r.Context(), user, params, msat, incoming.Get("comment"), payer, blockGUID, clientID, zapTargetTags(user))
		if err != nil {
			slog.Warn("zap request", "user", name, "err", err)
			writeError(w, http.StatusBadGateway, "could not build zap request")
			return "", false
		}
		return nostrJSON, true
	}
	switch s.signedZapMode() {
	case config.SignedZapComment:
		s.appendShowComment(r.Context(), user, params, blockGUID, incoming)
		return "", true
	case config.SignedZapReplace:
		wallet, err := parseWalletZap(supplied)
		if err != nil {
			slog.Warn("zap request", "user", name, "err", err)
			writeError(w, http.StatusBadRequest, "invalid nostr zap request")
			return "", false
		}
		extra := referenceTags(wallet)
		if len(extra) == 0 {
			extra = zapTargetTags(user)
		}
		if wallet.PubKey != "" {
			extra = append(extra, nostr.Tag{"senderPubkey", wallet.PubKey})
		}
		nostrJSON, err := s.zapRequest(r.Context(), user, params, msat, wallet.Content, payer, blockGUID, clientID, extra)
		if err != nil {
			slog.Warn("zap request", "user", name, "err", err)
			writeError(w, http.StatusBadGateway, "could not build zap request")
			return "", false
		}
		return nostrJSON, true
	default:
		return "", true
	}
}

const rssPaymentMarker = "rss::payment::"

// rssPaymentComment reports a Podcasting 2.0 payment carried in the lightning comment.
// Those payments are forwarded unchanged and never turned into a zap request.
func rssPaymentComment(comment string) bool {
	return strings.Contains(comment, rssPaymentMarker)
}

func (s *Server) signedZapMode() string {
	if s.cfg.SignedZap == "" {
		return config.SignedZapForward
	}
	return s.cfg.SignedZap
}

func (s *Server) appendShowComment(ctx context.Context, user config.User, params lnurl.PayParams, blockGUID string, incoming url.Values) {
	if params.CommentAllowed <= 0 {
		return
	}
	block := s.showBlock(ctx, user, blockGUID)
	line := contextLine(block)
	if line == "" {
		return
	}
	incoming.Set("comment", fitComment(incoming.Get("comment"), line, params.CommentAllowed))
}

func (s *Server) zapRequest(ctx context.Context, user config.User, params lnurl.PayParams, msat int64, comment, payerName, blockGUID, clientID string, extra nostr.Tags) (string, error) {
	recipient := user.Pubkey
	if recipient == "" {
		var err error
		recipient, err = nostrkey.ParsePublicKey(params.NostrPubkey)
		if err != nil {
			return "", err
		}
	}
	if recipient == "" {
		return "", errNoPubkey
	}
	rawURL, err := lnurl.AddressURL(user.Upstream)
	if err != nil {
		return "", err
	}
	encoded, err := lnurl.EncodeLNURL(rawURL)
	if err != nil {
		return "", err
	}
	if clientID != "" {
		extra = append(extra, nostr.Tag{"clientId", clientID})
	}
	ev, err := zap.BuildZapRequest(zap.Options{
		PrivateKey:      s.sk,
		RecipientPubkey: recipient,
		AmountMsat:      strconv.FormatInt(msat, 10),
		LNURL:           encoded,
		Relays:          s.cfg.Relays,
		Comment:         comment,
		PayerName:       payerName,
		Block:           s.showBlock(ctx, user, blockGUID),
		ExtraTags:       extra,
	})
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (s *Server) showBlock(ctx context.Context, user config.User, blockGUID string) *splitkit.Block {
	if user.EventGUID == "" || s.blocks == nil {
		return nil
	}
	if blockGUID != "" {
		if pinned, ok := s.blocks.Block(ctx, user.EventGUID, blockGUID); ok {
			return &pinned
		}
	}
	if current, ok := s.blocks.Current(ctx, user.EventGUID); ok {
		return &current
	}
	return nil
}

func parseWalletZap(raw string) (nostr.Event, error) {
	var ev nostr.Event
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return nostr.Event{}, err
	}
	if ev.Kind != 9734 {
		return nostr.Event{}, fmt.Errorf("not a zap request")
	}
	ok, err := ev.CheckSignature()
	if err != nil {
		return nostr.Event{}, err
	}
	if !ok {
		return nostr.Event{}, fmt.Errorf("invalid zap request signature")
	}
	return ev, nil
}

func zapTargetTags(user config.User) nostr.Tags {
	if user.ZapTargetTag == nil {
		return nil
	}
	return nostr.Tags{append(nostr.Tag(nil), user.ZapTargetTag...)}
}

func referenceTags(ev nostr.Event) nostr.Tags {
	var out nostr.Tags
	for _, tag := range ev.Tags {
		if len(tag) > 0 && (tag[0] == "e" || tag[0] == "a") {
			out = append(out, append(nostr.Tag(nil), tag...))
		}
	}
	return out
}

func contextLine(block *splitkit.Block) string {
	if block == nil {
		return ""
	}
	var ids []string
	if block.FeedGUID != "" {
		ids = append(ids, "podcast:guid:"+block.FeedGUID)
	}
	if block.ItemGUID != "" {
		ids = append(ids, "podcast:item:guid:"+block.ItemGUID)
	}
	joined := strings.Join(ids, " ")
	switch {
	case block.Title != "" && joined != "":
		return block.Title + " | " + joined
	case block.Title != "":
		return block.Title
	default:
		return joined
	}
}

func fitComment(comment, line string, limit int) string {
	if line == "" || limit <= 0 {
		return comment
	}
	combined := line
	if comment != "" {
		combined = comment + " " + line
	}
	runes := []rune(combined)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return combined
}

func (s *Server) lookup(name string) (string, config.User, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	user, ok := s.cfg.Users[name]
	return name, user, ok
}

type pubkeyError string

func (e pubkeyError) Error() string { return string(e) }

const errNoPubkey pubkeyError = "no nostr pubkey available for zap request"

const maxPayerNameRunes = 100

func payerName(q url.Values) string {
	raw := q.Get("payerdata")
	if raw == "" {
		return ""
	}
	var data struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return ""
	}
	return limitName(data.Name)
}

func limitName(name string) string {
	name = strings.TrimSpace(name)
	runes := []rune(name)
	if len(runes) > maxPayerNameRunes {
		return string(runes[:maxPayerNameRunes])
	}
	return name
}

func cloneQuery(in url.Values) url.Values {
	out := make(url.Values, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func mergeQuery(callback string, incoming url.Values, injectedNostr string) (string, error) {
	u, err := url.Parse(callback)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errInvalidCallback
	}
	q := u.Query()
	for key, values := range incoming {
		q.Del(key)
		for _, value := range values {
			q.Add(key, value)
		}
	}
	if injectedNostr != "" {
		q.Set("nostr", injectedNostr)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

type callbackError string

func (e callbackError) Error() string { return string(e) }

const errInvalidCallback callbackError = "invalid callback url"

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, status int, reason string) {
	body, _ := json.Marshal(map[string]string{
		"status": "ERROR",
		"reason": reason,
	})
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
