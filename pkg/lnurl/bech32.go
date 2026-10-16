package lnurl

import (
	"fmt"

	"github.com/btcsuite/btcd/btcutil/bech32"
)

// EncodeLNURL bech32-encodes a URL with the "lnurl" human-readable part.
func EncodeLNURL(rawURL string) (string, error) {
	if rawURL == "" {
		return "", fmt.Errorf("empty lnurl")
	}
	conv, err := bech32.ConvertBits([]byte(rawURL), 8, 5, true)
	if err != nil {
		return "", err
	}
	return bech32.Encode("lnurl", conv)
}

// DecodeLNURL decodes an lnurl bech32 string into the original URL.
func DecodeLNURL(s string) (string, error) {
	hrp, data, err := bech32.DecodeNoLimit(s)
	if err != nil {
		return "", err
	}
	if hrp != "lnurl" {
		return "", fmt.Errorf("unexpected hrp %q", hrp)
	}
	decoded, err := bech32.ConvertBits(data, 5, 8, false)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}
