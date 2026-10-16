// Package config loads and validates the proxy YAML configuration.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"gopkg.in/yaml.v3"

	"lnzap/pkg/nostrkey"
)

const (
	// SignedZapForward sends a wallet-signed zap request upstream unchanged.
	SignedZapForward = "forward"
	// SignedZapComment sends a wallet-signed zap request unchanged and appends show context to the comment.
	SignedZapComment = "comment"
	// SignedZapReplace substitutes a service-signed zap request for one the wallet already signed.
	SignedZapReplace = "replace"
)

// Duration is a YAML string parsed with time.ParseDuration (for example "5m").
type Duration time.Duration

// UnmarshalYAML parses a duration string such as "5m".
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	if strings.TrimSpace(s) == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// Std returns the duration as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Config is the proxy configuration.
type Config struct {
	Listen           string          `yaml:"listen"`
	PublicURL        string          `yaml:"public_url"`
	Relays           []string        `yaml:"relays"`
	CacheTTL         Duration        `yaml:"cache_ttl"`
	SplitKitHost     string          `yaml:"splitkit_host"`
	SplitKitWait     Duration        `yaml:"splitkit_wait"`
	SplitKitCacheTTL Duration        `yaml:"splitkit_cache_ttl"`
	SignedZap        string          `yaml:"signed_zap"`
	Users            map[string]User `yaml:"users"`
}

// User maps a local lightning-address username to an upstream address.
// ZapTarget is an optional note, nevent, naddr, or hex event id.
// ZapTargetTag is that value parsed into an "e" or "a" tag.
type User struct {
	Upstream     string    `yaml:"upstream"`
	Pubkey       string    `yaml:"pubkey"`
	EventGUID    string    `yaml:"event_guid"`
	ZapTarget    string    `yaml:"zap_target"`
	ZapTargetTag nostr.Tag `yaml:"-"`
}

// Load reads path and returns a validated config.
func Load(path string) (*Config, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return parse(body)
}

// LoadFromEnv reads YAML from LNZAP_CONFIG_YAML when that variable is set.
// Otherwise it reads the file named by LNZAP_CONFIG, then config.yaml.
// A relative config.yaml is also taken from a parent directory. vercel dev
// runs the function from a temporary directory under the project, so that
// search finds the project config.yaml.
func LoadFromEnv() (*Config, error) {
	if yamlText := strings.TrimSpace(os.Getenv("LNZAP_CONFIG_YAML")); yamlText != "" {
		return parse([]byte(yamlText))
	}
	if path := strings.TrimSpace(os.Getenv("LNZAP_CONFIG")); path != "" {
		return Load(path)
	}
	return Load(resolveConfig("config.yaml"))
}

// resolveConfig returns path when it exists. A relative path that is missing
// from the working directory is resolved by walking up from the working
// directory and then from the executable.
func resolveConfig(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	if wd, err := os.Getwd(); err == nil {
		if found := findUp(wd, path); found != "" {
			return found
		}
	}
	if exe, err := os.Executable(); err == nil {
		if found := findUp(filepath.Dir(exe), path); found != "" {
			return found
		}
	}
	return path
}

func findUp(start, name string) string {
	dir := start
	for {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// Parse validates YAML config bytes.
func Parse(body []byte) (*Config, error) {
	return parse(body)
}

func parse(body []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// CallbackURL is the proxied LNURL callback for a local username.
func (c *Config) CallbackURL(user string) string {
	return strings.TrimRight(c.PublicURL, "/") + "/lnurlp/" + url.PathEscape(user) + "/callback"
}

// EventGUIDs returns the distinct Split Kit event ids configured on users.
func (c *Config) EventGUIDs() []string {
	seen := make(map[string]struct{})
	var out []string
	for _, user := range c.Users {
		if user.EventGUID == "" {
			continue
		}
		if _, ok := seen[user.EventGUID]; ok {
			continue
		}
		seen[user.EventGUID] = struct{}{}
		out = append(out, user.EventGUID)
	}
	sort.Strings(out)
	return out
}

func (c *Config) normalize() error {
	if strings.TrimSpace(c.Listen) == "" {
		c.Listen = ":8080"
	}
	publicRaw := strings.TrimSpace(c.PublicURL)
	if publicRaw == "" {
		if host := strings.TrimSpace(os.Getenv("VERCEL_PROJECT_PRODUCTION_URL")); host != "" {
			publicRaw = "https://" + strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
		}
	}
	public, err := url.Parse(publicRaw)
	if err != nil || (public.Scheme != "http" && public.Scheme != "https") || public.Host == "" {
		return fmt.Errorf("public_url must be an absolute http(s) URL")
	}
	if public.RawQuery != "" || public.Fragment != "" {
		return fmt.Errorf("public_url must not include a query or fragment")
	}
	c.PublicURL = strings.TrimRight(public.String(), "/")

	if len(c.Relays) == 0 {
		return fmt.Errorf("at least one relay is required")
	}
	for i, relay := range c.Relays {
		relay = strings.TrimSpace(relay)
		u, err := url.Parse(relay)
		if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
			return fmt.Errorf("relay %q must be a ws(s) URL", c.Relays[i])
		}
		c.Relays[i] = relay
	}

	if c.CacheTTL.Std() == 0 {
		c.CacheTTL = Duration(5 * time.Minute)
	}
	if c.CacheTTL.Std() < 0 {
		return fmt.Errorf("cache_ttl must be positive")
	}
	if c.SplitKitWait.Std() == 0 {
		c.SplitKitWait = Duration(3 * time.Second)
	}
	if c.SplitKitWait.Std() < 0 {
		return fmt.Errorf("splitkit_wait must be positive")
	}
	if c.SplitKitCacheTTL.Std() == 0 {
		c.SplitKitCacheTTL = Duration(15 * time.Second)
	}
	if c.SplitKitCacheTTL.Std() < 0 {
		return fmt.Errorf("splitkit_cache_ttl must be positive")
	}
	switch strings.ToLower(strings.TrimSpace(c.SignedZap)) {
	case "", SignedZapForward:
		c.SignedZap = SignedZapForward
	case SignedZapComment:
		c.SignedZap = SignedZapComment
	case SignedZapReplace:
		c.SignedZap = SignedZapReplace
	default:
		return fmt.Errorf("signed_zap must be forward, comment, or replace")
	}

	if len(c.Users) == 0 {
		return fmt.Errorf("at least one user is required")
	}

	needsSplitKit := false
	users := make(map[string]User, len(c.Users))
	for name, user := range c.Users {
		key := strings.ToLower(strings.TrimSpace(name))
		if !validUsername(key) {
			return fmt.Errorf("username %q must contain only letters, digits, '.', '_' or '-'", name)
		}
		if _, ok := users[key]; ok {
			return fmt.Errorf("duplicate user %q", key)
		}
		if _, _, err := SplitAddress(user.Upstream); err != nil {
			return fmt.Errorf("user %s: %w", key, err)
		}
		user.Upstream = strings.TrimSpace(user.Upstream)
		pub, err := nostrkey.ParsePublicKey(user.Pubkey)
		if err != nil {
			return fmt.Errorf("user %s: %w", key, err)
		}
		user.Pubkey = pub
		user.EventGUID = strings.TrimSpace(user.EventGUID)
		user.ZapTarget = strings.TrimSpace(user.ZapTarget)
		tag, err := nostrkey.ParseZapTarget(user.ZapTarget)
		if err != nil {
			return fmt.Errorf("user %s: %w", key, err)
		}
		user.ZapTargetTag = tag
		if user.EventGUID != "" {
			needsSplitKit = true
		}
		users[key] = user
	}
	c.Users = users

	c.SplitKitHost = strings.TrimSpace(c.SplitKitHost)
	if needsSplitKit {
		if c.SplitKitHost == "" {
			c.SplitKitHost = "https://curiohoster.com"
		}
		u, err := url.Parse(c.SplitKitHost)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("splitkit_host must be an absolute http(s) URL")
		}
		c.SplitKitHost = strings.TrimRight(u.String(), "/")
	}
	return nil
}

func validUsername(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// SplitAddress splits a lightning address into its local part and host.
func SplitAddress(address string) (name, host string, err error) {
	address = strings.TrimSpace(address)
	at := strings.IndexByte(address, '@')
	if at <= 0 || at != strings.LastIndexByte(address, '@') || at == len(address)-1 {
		return "", "", fmt.Errorf("invalid lightning address %q", address)
	}
	name = address[:at]
	host = address[at+1:]
	if name == "" || host == "" || strings.ContainsAny(host, "/?#") {
		return "", "", fmt.Errorf("invalid lightning address %q", address)
	}
	return name, host, nil
}
