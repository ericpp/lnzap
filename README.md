# lnzap

**TL;DR:** A lightning address proxy. `alice@zap.example.com` → `alice@getalby.com`, but every zap gets tagged with nostr + podcast info on the way through.

---

## Quick start (5 minutes)

1. **Copy the config**

   ```sh
   cp config.example.yaml config.yaml
   ```

2. **Add a user** in `config.yaml`:

   ```yaml
   public_url: "https://zap.example.com"
   users:
     alice:
       upstream: "alice@getalby.com"
   ```

3. **Set your nostr key** (64 hex chars or `nsec1...`):

   ```sh
   export NOSTR_PRIVATE_KEY="nsec1..."
   ```

4. **Run it** (Go 1.24+):

   ```sh
   go run ./cmd/lnzap -config config.yaml
   ```

5. **Check it's alive:** `GET /healthz` → `{"status":"ok"}`

Done. Everything below is reference. Skim the headers, read what you need.

---

## What it does

- Answers [LUD-16](https://github.com/lnurl/luds/blob/luds/16.md) pay requests for your configured usernames.
- Forwards them to each user's real LNURL server.
- Rewrites the callback so invoice requests come back through the proxy.
- If the wallet didn't send a [NIP-57](https://github.com/nostr-protocol/nips/blob/master/57.md) zap request, the proxy **builds one** (kind 9734), signs it with the service key, and adds it. The request includes a `client` tag, `lnzap`. `replace` does the same.
- A comment that contains `rss::payment::` is left alone. No zap is built or rewritten, and the invoice request is forwarded as received.

> **Gotcha:** the upstream **must** advertise `allowsNostr`. If it doesn't, both the pay request and the invoice callback return an error.

---

## Full config

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

- Usernames are **not** case sensitive.
- If `public_url` is empty and `VERCEL_PROJECT_PRODUCTION_URL` is set, that host is used.

### Where it looks for the config

The long-running server, in order:

1. `-config` flag
2. `LNZAP_CONFIG` env var
3. `config.yaml` in the working directory
4. `config.yaml` in a parent directory

On Vercel the function reads `LNZAP_CONFIG_YAML` only.

---

## `signed_zap`: what to do when the wallet already signed a zap request

| Value | What happens |
| --- | --- |
| `forward` (default) | Sent upstream unchanged. |
| `comment` | Sent unchanged, plus a short show line appended to the comment (trimmed to `commentAllowed`). |
| `replace` | Wallet's signature is checked, then swapped for a service-signed request. |

**`comment` heads-up:** some upstreams ignore `comment` once a zap request is present.

**`replace` details:**

- Wallet's message becomes the content.
- Wallet's `e` and `a` tags are copied.
- Wallet's pubkey is stored as `senderPubkey`.
- The receipt credits the **service** key, not the wallet.

---

## `zap_target`: point zaps at a nostr note

Only applies to zap requests **this service signs**.

- `note`, `nevent`, or hex event id → `e` tag
- `naddr` → `a` tag

By mode:

- **`replace`:** wallet's `e`/`a` tags win. `zap_target` is used only if the wallet sent none.
- **`forward` / `comment`:** the wallet's request goes through untouched, so no target is added.

---

## Split Kit (podcast info)

Set `event_guid` on a user to tie them to a [Split Kit](https://www.thesplitkit.com) event.

The proxy follows the event's Socket.IO `remoteValue` stream and stamps the **current show** onto zap requests it creates:

- NIP-73 `i` / `k` tags for the podcast and episode
- `eventGuid`, `blockGuid`, `title`, `eventAPI`

**Long-running server vs. Vercel:**

- **Server:** keeps one Socket.IO connection open per `event_guid`.
- **Vercel:** each invoice request connects, grabs the current `remoteValue`, disconnects.
  - `splitkit_wait`: how long to wait before falling back to the first block from getblocks.
  - `splitkit_cache_ttl`: how long a warm instance reuses the result.

---

## Payer name ([LUD-18](https://github.com/lnurl/luds/blob/luds/18.md))

- The proxy asks for the payer's name via `payerData`.
- Upstream already asks for a name? Its spec is left alone.
- The name gets saved as a `payerName` tag on zap requests this service creates.
- `payerdata` is forwarded upstream **only** if that server already accepts it.

---

## Endpoints

| Endpoint | Does |
| --- | --- |
| `GET /.well-known/lnurlp/{user}` | Upstream pay request, `callback` pointed here. `metadata` unchanged. |
| `GET /lnurlp/{user}/callback` | Checks amount, adds/adjusts the zap request, returns upstream invoice. |
| `GET /healthz` | `{"status":"ok"}` |

All LNURL responses send `Access-Control-Allow-Origin: *`.

### Extra callback params

None of these are forwarded upstream.

- **`blockGuid`:** pick a specific Split Kit block. Falls back to the current one if it doesn't match.
- **`senderName`:** payer name, used when `payerdata` has none.
- **`clientId`:** stored as a tag on zap requests this service creates.

---

## Deploy to Vercel

Checklist:

- [ ] `LNZAP_CONFIG_YAML` is the YAML config (same shape as `config.yaml`).
- [ ] `NOSTR_PRIVATE_KEY` is set in the project environment (hex or `nsec`).
- [ ] Run:

  ```sh
  vercel deploy
  ```

> **Don't forget:** redeploy after changing the key or `LNZAP_CONFIG_YAML`.

How it works, if you care:

- The Go function reads its config from `LNZAP_CONFIG_YAML`.
- `vercel.json` rewrites `/.well-known/lnurlp/:user`, `/lnurlp/:user/callback`, and `/healthz` onto the Go function.
- `vercel dev` runs the same function locally. Set `LNZAP_CONFIG_YAML` there too.
