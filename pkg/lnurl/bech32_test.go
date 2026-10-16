package lnurl

import (
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcutil/bech32"
)

func TestEncodeLNURLMatchesBech32(t *testing.T) {
	raw := "https://example.com/pay"
	got, err := EncodeLNURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := bech32.ConvertBits([]byte(raw), 8, 5, true)
	if err != nil {
		t.Fatal(err)
	}
	want, err := bech32.Encode("lnurl", conv)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	decoded, err := DecodeLNURL(got)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != raw {
		t.Fatalf("decoded %q", decoded)
	}
}

func TestEncodeLNURLLongURL(t *testing.T) {
	raw := "https://payments.example.com/.well-known/lnurlp/" + strings.Repeat("a", 80)
	got, err := EncodeLNURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "lnurl1") {
		t.Fatalf("prefix %s", got)
	}
	decoded, err := DecodeLNURL(got)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != raw {
		t.Fatalf("decoded %q", decoded)
	}
}
