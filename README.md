# lnzap

`lnzap` is a lightning address proxy. It answers [LUD-16](https://github.com/lnurl/luds/blob/luds/16.md) pay requests for configured usernames, forwards them to each user's real LNURL server, and rewrites the callback so invoice requests come back through the proxy.

When a wallet asks for an invoice without a [NIP-57](https://github.com/nostr-protocol/nips/blob/master/57.md) zap request, the proxy builds a kind 9734 event, signs it with the service nostr key, and adds it to the upstream request. `signed_zap` decides what happens when the wallet already sent one.

If the user is tied to a [Split Kit](https://www.thesplitkit.com) event, the proxy follows that event's Socket.IO `remoteValue` stream and stamps the current show onto zap requests it creates: NIP-73 `i`/`k` tags for the podcast and episode, plus `eventGuid`, `blockGuid`, `title`, and `eventAPI`.

The proxy requires the upstream to advertise `allowsNostr`. If it does not, the pay request and the invoice callback both return an error.

`signed_zap` controls a zap request the wallet already signed. `forward` (the default) sends it upstream unchanged. `comment` sends it unchanged and appends a short show line to the comment, cut to fit `commentAllowed`. Some upstreams ignore `comment` once a zap request is present. `replace` checks the wallet's signature and substitutes a service-signed request: the wallet's message stays the content, its `e` and `a` tags are copied, and its pubkey is stored as `senderPubkey`. The receipt then credits the service key.

`zap_target` attaches a nostr note or addressable event to zap requests this service signs. A `note`, `nevent`, or hex event id becomes an `e` tag, and an `naddr` becomes an `a` tag. In `replace` mode the wallet's `e` and `a` tags are kept, and `zap_target` is used only when the wallet sent none. `forward` and `comment` send a wallet-signed request unchanged, so the target is not added.

The invoice callback also accepts `blockGuid`, `senderName`, and `clientId`. `blockGuid` selects that Split Kit block, falling back to the current one when it does not match. `senderName` supplies the payer name when `payerdata` has none. `clientId` is stored as a tag on zap requests this service creates. None of the three is forwarded upstream.

The proxy asks for the payer's name with [LUD-18](https://github.com/lnurl/luds/blob/luds/18.md) `payerData`. If the upstream already requests a name, that spec is left as-is. Wallets that support it send `payerdata` on the invoice callback, and the proxy records the name as a `payerName` tag on zap requests it creates. `payerdata` is forwarded upstream only when that server already accepts it.

## Configure

Copy `config.example.yaml` to `config.yaml`.

```yaml
listen: ":8080"
public_url: "https://zap.example.com"
relays: ["wss://relay.damus.io", "wss://nos.lol"]
cache_ttl: 5m
signed_zap: forward # forward, comment, or replace
splitkit_host: "https://curiohoster.com"
users:
  alice:
    upstream: "alice@getalby.com"
    pubkey: ""          # optional hex or npub; otherwise the upstream nostrPubkey is used
    event_guid: ""      # optional Split Kit event id
    zap_target: ""      # optional note, nevent, naddr, or hex event id
```

`alice@zap.example.com` then proxies to `alice@getalby.com`. Usernames are matched without case sensitivity.

Set the signing key in the environment. It accepts 64 hex characters or an `nsec` bech32 secret:

```sh
export NOSTR_PRIVATE_KEY="nsec1..."
```

The config path is `-config` or `LNZAP_CONFIG`. It defaults to `config.yaml` in the working directory, or that same file in a parent directory. `vercel dev` runs the function from a temporary directory, so the parent search picks up the project `config.yaml`. A deployed function embeds `api/config.yaml`, which the build copies from `config.yaml`. If `public_url` is empty and `VERCEL_PROJECT_PRODUCTION_URL` is set, that host is used.

## Run

Go 1.24 or later:

```sh
go run ./cmd/lnzap -config config.yaml
```

Endpoints:

- `GET /.well-known/lnurlp/{user}` returns the upstream pay request with `callback` pointed at this service. `metadata` is left unchanged.
- `GET /lnurlp/{user}/callback` checks the amount, adds or adjusts the zap request, and returns the upstream invoice response.
- `GET /healthz` returns `{"status":"ok"}`.

LNURL responses send `Access-Control-Allow-Origin: *`.

The long-running server keeps a Socket.IO connection open for each `event_guid`. On Vercel each invoice request connects, takes the current `remoteValue`, and disconnects. `splitkit_wait` is how long that lookup waits before using the first block from getblocks, and `splitkit_cache_ttl` is how long a warm instance reuses it.

## Deploy to Vercel

`config.yaml` is uploaded with the project. The build copies it to `api/config.yaml`, and the Go function embeds that file. `config.yaml` is not listed in `.vercelignore`.

Set `NOSTR_PRIVATE_KEY` in the project environment. It is the hex or `nsec` service key. Redeploy after changing the key or `config.yaml`.

`LNZAP_CONFIG_YAML`, when the function can see it, replaces the embedded file.

From the repo:

```sh
vercel deploy
```

`vercel dev` runs the same function locally. `vercel.json` rewrites `/.well-known/lnurlp/:user`, `/lnurlp/:user/callback`, and `/healthz` onto the Go function.
