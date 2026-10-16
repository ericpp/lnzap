// Package zap builds and signs NIP-57 kind 9734 zap requests.
package zap

import (
	"fmt"
	"strconv"
	"time"

	"github.com/nbd-wtf/go-nostr"

	"lnzap/pkg/nostrkey"
	"lnzap/pkg/splitkit"
)

// ParsePrivateKey accepts a hex secret or nsec bech32 string.
func ParsePrivateKey(s string) (string, error) {
	return nostrkey.ParsePrivateKey(s)
}

// Options describes a kind 9734 zap request.
// AmountMsat and LNURL are decimal millisatoshis and a bech32 lnurl.
// PayerName, when set, records the LUD-18 payer name.
// Block, when set, adds the current Split Kit show as NIP-73 and custom tags.
// ExtraTags are appended after the block tags.
type Options struct {
	PrivateKey      string
	RecipientPubkey string
	AmountMsat      string
	LNURL           string
	Relays          []string
	Comment         string
	PayerName       string
	Block           *splitkit.Block
	ExtraTags       nostr.Tags
}

// BuildZapRequest signs a kind 9734 zap request.
func BuildZapRequest(opts Options) (nostr.Event, error) {
	sk, err := nostrkey.ParsePrivateKey(opts.PrivateKey)
	if err != nil {
		return nostr.Event{}, err
	}
	recipient, err := nostrkey.ParsePublicKey(opts.RecipientPubkey)
	if err != nil || recipient == "" {
		return nostr.Event{}, fmt.Errorf("recipient pubkey is required")
	}
	if _, err := strconv.ParseUint(opts.AmountMsat, 10, 64); err != nil {
		return nostr.Event{}, fmt.Errorf("amount must be millisatoshis")
	}
	if opts.LNURL == "" {
		return nostr.Event{}, fmt.Errorf("lnurl is required")
	}
	if len(opts.Relays) == 0 {
		return nostr.Event{}, fmt.Errorf("at least one relay is required")
	}

	relaysTag := nostr.Tag{"relays"}
	relaysTag = append(relaysTag, opts.Relays...)
	tags := nostr.Tags{
		{"p", recipient},
		{"amount", opts.AmountMsat},
		relaysTag,
		{"lnurl", opts.LNURL},
	}
	if opts.PayerName != "" {
		tags = append(tags, nostr.Tag{"payerName", opts.PayerName})
	}
	tags = appendBlockTags(tags, opts.Block)
	tags = append(tags, opts.ExtraTags...)

	ev := nostr.Event{
		Kind:      9734,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Content:   opts.Comment,
		Tags:      tags,
	}
	if err := ev.Sign(sk); err != nil {
		return nostr.Event{}, fmt.Errorf("sign zap request: %w", err)
	}
	return ev, nil
}

func appendBlockTags(tags nostr.Tags, block *splitkit.Block) nostr.Tags {
	if block == nil {
		return tags
	}
	if block.FeedGUID != "" {
		tags = append(tags,
			nostr.Tag{"i", "podcast:guid:" + block.FeedGUID},
			nostr.Tag{"k", "podcast:guid"},
		)
	}
	if block.ItemGUID != "" {
		tags = append(tags,
			nostr.Tag{"i", "podcast:item:guid:" + block.ItemGUID},
			nostr.Tag{"k", "podcast:item:guid"},
		)
	}
	if block.EventGUID != "" {
		tags = append(tags, nostr.Tag{"eventGuid", block.EventGUID})
	}
	if block.BlockGUID != "" {
		tags = append(tags, nostr.Tag{"blockGuid", block.BlockGUID})
	}
	if block.Title != "" {
		tags = append(tags, nostr.Tag{"title", block.Title})
	}
	if block.EventAPI != "" {
		tags = append(tags, nostr.Tag{"eventAPI", block.EventAPI})
	}
	return tags
}
