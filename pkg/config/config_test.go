package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

func TestLoad(t *testing.T) {
	sk := nostr.GeneratePrivateKey()
	pub, err := nostr.GetPublicKey(sk)
	if err != nil {
		t.Fatal(err)
	}
	npub, err := nip19.EncodePublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := []byte(`
listen: ":9090"
public_url: "https://zap.example.com/"
relays: ["wss://relay.example.com"]
cache_ttl: 1m
splitkit_host: "https://curiohoster.com"
users:
  Alice:
    upstream: "alice@getalby.com"
    pubkey: "` + npub + `"
    event_guid: " 303fe7e4-32c1-4f67-977d-967f251cb6b0 "
`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":9090" {
		t.Fatalf("listen %s", cfg.Listen)
	}
	if cfg.PublicURL != "https://zap.example.com" {
		t.Fatalf("public url %s", cfg.PublicURL)
	}
	if cfg.CacheTTL.Std() != time.Minute {
		t.Fatalf("ttl %s", cfg.CacheTTL.Std())
	}
	user, ok := cfg.Users["alice"]
	if !ok {
		t.Fatal("missing alice")
	}
	if user.Pubkey != pub {
		t.Fatalf("pubkey %s", user.Pubkey)
	}
	if user.EventGUID != "303fe7e4-32c1-4f67-977d-967f251cb6b0" {
		t.Fatalf("guid %q", user.EventGUID)
	}
	if cfg.CallbackURL("alice") != "https://zap.example.com/lnurlp/alice/callback" {
		t.Fatalf("callback %s", cfg.CallbackURL("alice"))
	}
	if cfg.SignedZap != SignedZapForward {
		t.Fatalf("signed_zap %s", cfg.SignedZap)
	}
}

func TestSignedZapConfig(t *testing.T) {
	for _, mode := range []string{SignedZapForward, SignedZapComment, SignedZapReplace, " Comment "} {
		cfg, err := parse([]byte(`
public_url: "https://zap.example.com"
relays: ["wss://relay.example.com"]
signed_zap: "` + mode + `"
users:
  alice:
    upstream: alice@getalby.com
`))
		if err != nil {
			t.Fatal(mode, err)
		}
		want := strings.ToLower(strings.TrimSpace(mode))
		if cfg.SignedZap != want {
			t.Fatalf("mode %q got %s", mode, cfg.SignedZap)
		}
	}
	if _, err := parse([]byte(`
public_url: "https://zap.example.com"
relays: ["wss://relay.example.com"]
signed_zap: "drop"
users:
  alice:
    upstream: alice@getalby.com
`)); err == nil {
		t.Fatal("expected invalid signed_zap error")
	}
}

func TestZapTargetConfig(t *testing.T) {
	pub := strings.Repeat("cd", 32)
	naddr, err := nip19.EncodeEntity(pub, 30023, "my-slug", []string{"wss://relay.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parse([]byte(`
public_url: "https://zap.example.com"
relays: ["wss://relay.example.com"]
users:
  alice:
    upstream: alice@getalby.com
    zap_target: " ` + naddr + ` "
`))
	if err != nil {
		t.Fatal(err)
	}
	user := cfg.Users["alice"]
	if user.ZapTarget != naddr {
		t.Fatalf("zap_target %q", user.ZapTarget)
	}
	want := nostr.Tag{"a", "30023:" + pub + ":my-slug", "wss://relay.example.com"}
	if len(user.ZapTargetTag) != len(want) {
		t.Fatalf("tag %#v", user.ZapTargetTag)
	}
	for i := range want {
		if user.ZapTargetTag[i] != want[i] {
			t.Fatalf("tag %#v", user.ZapTargetTag)
		}
	}

	if _, err := parse([]byte(`
public_url: "https://zap.example.com"
relays: ["wss://relay.example.com"]
users:
  alice:
    upstream: alice@getalby.com
    zap_target: "not-a-target"
`)); err == nil {
		t.Fatal("expected invalid zap_target error")
	}
}

func TestLoadRejectsDuplicateUsers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := []byte(`
public_url: "https://zap.example.com"
relays: ["wss://relay.example.com"]
users:
  alice:
    upstream: "alice@getalby.com"
  Alice:
    upstream: "other@getalby.com"
`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected duplicate user error")
	}
}

func TestFindUp(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, ".vercel", "cache", "go", "abc")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(want, []byte("listen: \":8080\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findUp(nested, "config.yaml"); got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if got := findUp(nested, "missing.yaml"); got != "" {
		t.Fatalf("got %s", got)
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("LNZAP_CONFIG_YAML", `
relays: ["wss://relay.example.com"]
splitkit_wait: 2s
users:
  alice:
    upstream: alice@getalby.com
`)
	t.Setenv("VERCEL_PROJECT_PRODUCTION_URL", "zap.example.com")
	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL != "https://zap.example.com" {
		t.Fatalf("public url %s", cfg.PublicURL)
	}
	if cfg.SplitKitWait.Std() != 2*time.Second {
		t.Fatalf("wait %s", cfg.SplitKitWait.Std())
	}
	if cfg.SplitKitCacheTTL.Std() != 15*time.Second {
		t.Fatalf("cache ttl %s", cfg.SplitKitCacheTTL.Std())
	}
	if _, ok := cfg.Users["alice"]; !ok {
		t.Fatal("missing alice")
	}
}
