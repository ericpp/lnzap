package zap

import (
	"testing"

	"github.com/nbd-wtf/go-nostr"

	"lnzap/pkg/splitkit"
)

func TestBuildZapRequest(t *testing.T) {
	sk := nostr.GeneratePrivateKey()
	signer, err := nostr.GetPublicKey(sk)
	if err != nil {
		t.Fatal(err)
	}
	recipient := nostr.GeneratePrivateKey()
	recipientPub, err := nostr.GetPublicKey(recipient)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := BuildZapRequest(Options{
		PrivateKey:      sk,
		RecipientPubkey: recipientPub,
		AmountMsat:      "21000",
		LNURL:           "lnurl1example",
		Relays:          []string{"wss://relay.example.com", "wss://nos.lol"},
		Comment:         "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != 9734 {
		t.Fatalf("kind %d", ev.Kind)
	}
	if ev.Content != "hello" {
		t.Fatalf("content %q", ev.Content)
	}
	if ev.PubKey != signer {
		t.Fatalf("signer %s", ev.PubKey)
	}
	if ev.ID == "" || ev.ID != ev.GetID() {
		t.Fatalf("id %s computed %s", ev.ID, ev.GetID())
	}
	ok, err := ev.CheckSignature()
	if err != nil || !ok {
		t.Fatalf("signature ok=%v err=%v", ok, err)
	}
	if got := tagValue(ev, "p"); got != recipientPub {
		t.Fatalf("p %s", got)
	}
	if got := tagValue(ev, "amount"); got != "21000" {
		t.Fatalf("amount %s", got)
	}
	if got := tagValue(ev, "lnurl"); got != "lnurl1example" {
		t.Fatalf("lnurl %s", got)
	}
	relays := tag(ev, "relays")
	if len(relays) != 3 || relays[1] != "wss://relay.example.com" || relays[2] != "wss://nos.lol" {
		t.Fatalf("relays %#v", relays)
	}
	if tag(ev, "i") != nil {
		t.Fatal("unexpected i tag")
	}
	if tag(ev, "payerName") != nil {
		t.Fatal("unexpected payerName tag")
	}
	if got := tagValue(ev, "client"); got != clientName {
		t.Fatalf("client %s", got)
	}
}

func TestBuildZapRequestBlockTags(t *testing.T) {
	sk := nostr.GeneratePrivateKey()
	recipient := nostr.GeneratePrivateKey()
	recipientPub, err := nostr.GetPublicKey(recipient)
	if err != nil {
		t.Fatal(err)
	}
	block := &splitkit.Block{
		Title:     "Live Show",
		FeedGUID:  "feed-1",
		ItemGUID:  "item-1",
		EventGUID: "event-1",
		BlockGUID: "block-1",
		EventAPI:  "https://curiohoster.com/api/sk/event/lookup",
		Image:     "https://example.com/art.png",
	}
	ev, err := BuildZapRequest(Options{
		PrivateKey:      sk,
		RecipientPubkey: recipientPub,
		AmountMsat:      "1000",
		LNURL:           "lnurl1example",
		Relays:          []string{"wss://relay.example.com"},
		PayerName:       "Bob",
		Block:           block,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Content != "" {
		t.Fatalf("content %q", ev.Content)
	}
	is := allTags(ev, "i")
	if len(is) != 2 || is[0][1] != "podcast:guid:feed-1" || is[1][1] != "podcast:item:guid:item-1" {
		t.Fatalf("i tags %#v", is)
	}
	ks := allTags(ev, "k")
	if len(ks) != 2 || ks[0][1] != "podcast:guid" || ks[1][1] != "podcast:item:guid" {
		t.Fatalf("k tags %#v", ks)
	}
	if tagValue(ev, "eventGuid") != "event-1" || tagValue(ev, "blockGuid") != "block-1" || tagValue(ev, "title") != "Live Show" {
		t.Fatalf("tags %#v", ev.Tags)
	}
	if tagValue(ev, "eventAPI") != "https://curiohoster.com/api/sk/event/lookup" {
		t.Fatalf("eventAPI %s", tagValue(ev, "eventAPI"))
	}
	if tagValue(ev, "payerName") != "Bob" {
		t.Fatalf("payerName %s", tagValue(ev, "payerName"))
	}
	ok, err := ev.CheckSignature()
	if err != nil || !ok {
		t.Fatalf("signature ok=%v err=%v", ok, err)
	}
}

func TestBuildZapRequestSkipsEmptyBlockFields(t *testing.T) {
	sk := nostr.GeneratePrivateKey()
	recipientPub, err := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	ev, err := BuildZapRequest(Options{
		PrivateKey:      sk,
		RecipientPubkey: recipientPub,
		AmountMsat:      "1000",
		LNURL:           "lnurl1example",
		Relays:          []string{"wss://relay.example.com"},
		Comment:         "note",
		Block:           &splitkit.Block{Title: "Only"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tag(ev, "i") != nil || tag(ev, "k") != nil || tag(ev, "eventGuid") != nil {
		t.Fatalf("tags %#v", ev.Tags)
	}
	if tagValue(ev, "title") != "Only" {
		t.Fatalf("title %s", tagValue(ev, "title"))
	}
}

func TestBuildZapRequestExtraTags(t *testing.T) {
	sk := nostr.GeneratePrivateKey()
	recipientPub, err := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	ev, err := BuildZapRequest(Options{
		PrivateKey:      sk,
		RecipientPubkey: recipientPub,
		AmountMsat:      "1000",
		LNURL:           "lnurl1example",
		Relays:          []string{"wss://relay.example.com"},
		ExtraTags: nostr.Tags{
			{"e", "evt1", "wss://relay.example.com"},
			{"a", "30023:pub:slug"},
			{"senderPubkey", "payer"},
			{"clientId", "client-1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	e := tag(ev, "e")
	if len(e) != 3 || e[1] != "evt1" || e[2] != "wss://relay.example.com" {
		t.Fatalf("e %#v", e)
	}
	if tagValue(ev, "a") != "30023:pub:slug" || tagValue(ev, "senderPubkey") != "payer" || tagValue(ev, "clientId") != "client-1" {
		t.Fatalf("tags %#v", ev.Tags)
	}
	ok, err := ev.CheckSignature()
	if err != nil || !ok {
		t.Fatalf("signature ok=%v err=%v", ok, err)
	}
}

func tag(ev nostr.Event, name string) nostr.Tag {
	for _, item := range ev.Tags {
		if len(item) > 0 && item[0] == name {
			return item
		}
	}
	return nil
}

func tagValue(ev nostr.Event, name string) string {
	item := tag(ev, name)
	if len(item) < 2 {
		return ""
	}
	return item[1]
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
