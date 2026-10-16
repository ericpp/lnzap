package nostrkey

import (
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

func TestParseZapTarget(t *testing.T) {
	id := strings.Repeat("ab", 32)
	pub := strings.Repeat("cd", 32)
	note, err := nip19.EncodeNote(id)
	if err != nil {
		t.Fatal(err)
	}
	nevent, err := nip19.EncodeEvent(id, []string{"wss://relay.example.com", "wss://other.example.com"}, pub)
	if err != nil {
		t.Fatal(err)
	}
	neventBare, err := nip19.EncodeEvent(id, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	naddr, err := nip19.EncodeEntity(pub, 30023, "my-slug", []string{"wss://relay.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	npub, err := nip19.EncodePublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		in   string
		want nostr.Tag
		err  bool
	}{
		{name: "empty", in: "", want: nil},
		{name: "blank", in: "  ", want: nil},
		{name: "note", in: " " + note + " ", want: nostr.Tag{"e", id}},
		{name: "hex", in: strings.ToUpper(id), want: nostr.Tag{"e", id}},
		{name: "nevent", in: nevent, want: nostr.Tag{"e", id, "wss://relay.example.com"}},
		{name: "nevent without relay", in: neventBare, want: nostr.Tag{"e", id}},
		{name: "naddr", in: naddr, want: nostr.Tag{"a", "30023:" + pub + ":my-slug", "wss://relay.example.com"}},
		{name: "npub", in: npub, err: true},
		{name: "garbage", in: "not-a-target", err: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseZapTarget(tt.in)
			if tt.err {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %#v want %#v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %#v want %#v", got, tt.want)
				}
			}
		})
	}
}
