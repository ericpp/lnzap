// Package nostrkey parses nostr secret keys and public keys from hex or bech32.
package nostrkey

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

// ParsePrivateKey accepts a 64-character hex secret or an nsec bech32 string
// and returns the hex secret.
func ParsePrivateKey(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("NOSTR_PRIVATE_KEY is empty")
	}
	if isHex64(s) {
		return strings.ToLower(s), nil
	}
	prefix, value, err := nip19.Decode(s)
	if err != nil {
		return "", fmt.Errorf("NOSTR_PRIVATE_KEY must be hex or nsec")
	}
	if prefix != "nsec" {
		return "", fmt.Errorf("NOSTR_PRIVATE_KEY has prefix %q, want nsec", prefix)
	}
	hexKey, ok := value.(string)
	if !ok || !isHex64(hexKey) {
		return "", fmt.Errorf("NOSTR_PRIVATE_KEY nsec payload is invalid")
	}
	return strings.ToLower(hexKey), nil
}

// ParsePublicKey accepts a 64-character hex pubkey or an npub bech32 string
// and returns the hex pubkey. An empty string is returned unchanged.
func ParsePublicKey(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if isHex64(s) {
		return strings.ToLower(s), nil
	}
	prefix, value, err := nip19.Decode(s)
	if err != nil {
		return "", fmt.Errorf("pubkey must be hex or npub")
	}
	if prefix != "npub" {
		return "", fmt.Errorf("pubkey has prefix %q, want npub", prefix)
	}
	hexKey, ok := value.(string)
	if !ok || !isHex64(hexKey) {
		return "", fmt.Errorf("pubkey npub payload is invalid")
	}
	return strings.ToLower(hexKey), nil
}

// ParseZapTarget accepts a note, nevent, naddr, or 64-character hex event id.
// A note, nevent, or hex id becomes an "e" tag. An naddr becomes an "a" tag.
// The first relay hint, when present, is the third element. An empty string
// returns a nil tag.
func ParseZapTarget(s string) (nostr.Tag, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if isHex64(s) {
		return nostr.Tag{"e", strings.ToLower(s)}, nil
	}
	prefix, value, err := nip19.Decode(s)
	if err != nil {
		return nil, fmt.Errorf("zap target must be a note, nevent, naddr, or hex event id")
	}
	switch prefix {
	case "note":
		id, ok := value.(string)
		if !ok || !isHex64(id) {
			return nil, fmt.Errorf("zap target note payload is invalid")
		}
		return nostr.Tag{"e", strings.ToLower(id)}, nil
	case "nevent":
		ptr, ok := value.(nostr.EventPointer)
		if !ok || !isHex64(ptr.ID) {
			return nil, fmt.Errorf("zap target nevent payload is invalid")
		}
		return eventTag(ptr.ID, ptr.Relays), nil
	case "naddr":
		ptr, ok := value.(nostr.EntityPointer)
		if !ok || !isHex64(ptr.PublicKey) || ptr.Identifier == "" {
			return nil, fmt.Errorf("zap target naddr payload is invalid")
		}
		return addressTag(ptr), nil
	default:
		return nil, fmt.Errorf("zap target has prefix %q, want note, nevent, or naddr", prefix)
	}
}

func eventTag(id string, relays []string) nostr.Tag {
	tag := nostr.Tag{"e", strings.ToLower(id)}
	if relay := firstRelay(relays); relay != "" {
		tag = append(tag, relay)
	}
	return tag
}

func addressTag(ptr nostr.EntityPointer) nostr.Tag {
	tag := nostr.Tag{"a", ptr.AsTagReference()}
	if relay := firstRelay(ptr.Relays); relay != "" {
		tag = append(tag, relay)
	}
	return tag
}

func firstRelay(relays []string) string {
	if len(relays) == 0 {
		return ""
	}
	return relays[0]
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
