// Package lnurl fetches LUD-16 pay requests and encodes lnurl bech32 strings.
package lnurl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"lnzap/pkg/config"
)

const (
	maxBodyBytes = 1 << 20
	userAgent    = "lnzap/1.0"
)

// PayParams is an LNURL-pay payRequest. Metadata is the raw metadata string
// so description hashes stay stable.
type PayParams struct {
	Callback       string          `json:"callback"`
	MaxSendable    int64           `json:"maxSendable"`
	MinSendable    int64           `json:"minSendable"`
	Metadata       string          `json:"metadata"`
	Tag            string          `json:"tag"`
	CommentAllowed int             `json:"commentAllowed"`
	AllowsNostr    bool            `json:"allowsNostr"`
	NostrPubkey    string          `json:"nostrPubkey"`
	PayerData      json.RawMessage `json:"payerData"`
	Status         string          `json:"status"`
	Reason         string          `json:"reason"`
}

// Client fetches and caches upstream pay requests.
type Client struct {
	ttl   time.Duration
	http  *http.Client
	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	params PayParams
	raw    []byte
	at     time.Time
}

// NewClient returns a client that caches pay requests for ttl.
// If hc is nil, a client with a 10 second timeout is used.
func NewClient(ttl time.Duration, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &Client{
		ttl:   ttl,
		http:  hc,
		cache: make(map[string]cacheEntry),
	}
}

// HTTP returns the underlying HTTP client.
func (c *Client) HTTP() *http.Client { return c.http }

// AddressURL is the LUD-16 well-known URL for a lightning address.
func AddressURL(address string) (string, error) {
	name, host, err := config.SplitAddress(address)
	if err != nil {
		return "", err
	}
	u := url.URL{
		Scheme: "https",
		Host:   host,
		Path:   "/.well-known/lnurlp/" + url.PathEscape(name),
	}
	return u.String(), nil
}

// Fetch returns the cached or freshly loaded pay request for address.
// The raw body is the upstream JSON, with metadata preserved.
func (c *Client) Fetch(ctx context.Context, address string) (PayParams, []byte, error) {
	key := strings.ToLower(strings.TrimSpace(address))
	c.mu.Lock()
	if ent, ok := c.cache[key]; ok && time.Since(ent.at) < c.ttl {
		c.mu.Unlock()
		return ent.params, append([]byte(nil), ent.raw...), nil
	}
	c.mu.Unlock()

	rawURL, err := AddressURL(address)
	if err != nil {
		return PayParams{}, nil, err
	}
	status, body, err := c.Get(ctx, rawURL)
	if err != nil {
		return PayParams{}, nil, err
	}
	if status != http.StatusOK {
		return PayParams{}, nil, fmt.Errorf("upstream status %d", status)
	}
	params, err := decodePayParams(body)
	if err != nil {
		return PayParams{}, nil, err
	}

	c.mu.Lock()
	c.cache[key] = cacheEntry{params: params, raw: append([]byte(nil), body...), at: time.Now()}
	c.mu.Unlock()
	return params, body, nil
}

// Get performs a GET and returns the status code and a size-limited body.
func (c *Client) Get(ctx context.Context, rawURL string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := readLimited(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

func decodePayParams(body []byte) (PayParams, error) {
	var params PayParams
	if err := json.Unmarshal(body, &params); err != nil {
		return PayParams{}, fmt.Errorf("decode pay request: %w", err)
	}
	if strings.EqualFold(params.Status, "ERROR") {
		if params.Reason == "" {
			params.Reason = "upstream error"
		}
		return PayParams{}, fmt.Errorf("%s", params.Reason)
	}
	if params.Tag != "payRequest" || params.Callback == "" {
		return PayParams{}, fmt.Errorf("upstream response is not a payRequest")
	}
	return params, nil
}

// AddPayerName returns raw with payerData.name set to {"mandatory": false}.
// Existing payerData fields are left alone. If name is already requested, raw is returned unchanged.
func AddPayerName(raw []byte) ([]byte, error) {
	payload, err := decodeObject(raw)
	if err != nil {
		return nil, err
	}
	switch existing := payload["payerData"].(type) {
	case nil:
		payload["payerData"] = map[string]any{
			"name": map[string]any{"mandatory": false},
		}
	case map[string]any:
		if _, ok := existing["name"]; ok {
			return append([]byte(nil), raw...), nil
		}
		existing["name"] = map[string]any{"mandatory": false}
	default:
		return append([]byte(nil), raw...), nil
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RewriteCallback returns raw with its callback field replaced.
// The metadata string value is preserved.
func RewriteCallback(raw []byte, callback string) ([]byte, error) {
	payload, err := decodeObject(raw)
	if err != nil {
		return nil, err
	}
	payload["callback"] = callback
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func decodeObject(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	dec.UseNumber()
	var payload map[string]any
	if err := dec.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode lnurl response: %w", err)
	}
	return payload, nil
}

func readLimited(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBodyBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxBodyBytes)
	}
	return body, nil
}
