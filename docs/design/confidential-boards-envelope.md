# Confidential rd Boards — Envelope Spec (FROZEN wire contract)

Status: **FROZEN** (ready-670, epic ready-216). Every downstream item — crypto
(ready-62d, done), write (ready-e63), read (ready-ce2), keydist (ready-a8a),
foldgate (ready-710), guardrail (ready-aea), label tokenization (ready-c83) —
implements against THIS document. If code and this doc diverge, this doc wins
until it is deliberately revised.

Graph-leak decision (ready-47f) ruling: **option (b) TOKENIZE** — the labels tag
(`l`) is HMAC-tokenized (§7). Baron, 2026-07-15. Every other routing tag stays
clear.

---

## 0. The one load-bearing idea: the envelope split

A confidential board encrypts ONLY free text. Every relay-INDEXED routing tag
stays PLAINTEXT (the label value is replaced by an owner-keyed token — still a
clear, equality-comparable tag). The relay (strfry) dedupes addressable cards by
`(author pubkey, kind:30302, d-tag)` and filters `REQ`s by clear tag VALUES; it
never reads `Content`. Therefore:

- **No relay schema change, no re-indexing.** Dedupe and every existing `REQ`
  filter keep working byte-for-byte.
- The ONLY new client-side step is a decrypt in `rd list` / `rd show`.

---

## 1. Card clear-tag vs encrypted-field list (kind-30302)

Source of truth: `BuildCardEvent` at `pkg/sync/nostrwire.go:237-310`. Line-by-line
disposition of every tag it emits (verified against the current tree):

| Tag | Emit site | Confidential-mode disposition |
|-----|-----------|-------------------------------|
| `d` = ItemID | :242 | **CLEAR** — addressable dedupe key. MUST stay clear. |
| `title` = Title | :243 | **ENCRYPTED** — value moves into `Content`; the clear `title` tag is DROPPED (not emitted) in confidential mode. rd never queries by title. |
| `a` = board coord | :246 | **CLEAR** (board membership) |
| `s` = Status | :249 | **CLEAR** |
| `rank` = Priority | :253 | **CLEAR** |
| `priority` = Priority | :254 | **CLEAR** |
| `itype` = Type | :257 | **CLEAR** |
| `p` = Assignee | :260 | **CLEAR** |
| `i` = each Dep | :264 | **CLEAR** (one per dep) |
| `gate` = Gate | :268 | **CLEAR** |
| `waiting_type` | :271 | **CLEAR** |
| `waiting_on` = WaitingOn | :273-274 | **ENCRYPTED** — ⚠ currently CLEAR; confidential mode moves the VALUE into `Content` and does NOT emit the clear `waiting_on` tag. |
| `l` = each Label | :278 | **TOKENIZED** — value replaced by `hex(HMAC-SHA256(LTK, label))` (§7). Tag KEY stays `l`; one per label. Plaintext labels ALSO travel in `Content` for member rendering. |
| `eta` = ETA | :282 | **CLEAR** |
| `level` = Level | :289 | **CLEAR** |
| `for` = For | :292 | **CLEAR** |
| `parent` = ParentID | :295 | **CLEAR** |
| `due` = Due | :298 | **CLEAR** |
| `Content` = Context | :304 | **ENCRYPTED** — the description; becomes the AEAD payload (§3). |

Always-clear nostr envelope (never touched): author `pubkey`, `kind`,
`created_at`, `sig`.

**NEW always-clear MARKER tags** (added by the write item, the ONLY new clear
tags):
- `["enc","1"]` — envelope-version discriminator. Readers and the fold gate
  version-dispatch on this.
- `["cek_epoch","<int>"]` — integer id of the CEK epoch that sealed this
  `Content`, e.g. `["cek_epoch","1"]`.

No other new clear tag is added. In particular **no content-hash tag** (§6).

---

## 2. Status-event clear-tag vs encrypted-field list (NIP-34)

Source: `BuildStatusEvent` at `pkg/sync/nostrwire.go:319-344`.

| Tag / field | Emit site | Confidential-mode disposition |
|-------------|-----------|-------------------------------|
| `a` = CardCoord | :327 | **CLEAR** |
| `d` = ItemID | :328 | **CLEAR** |
| `status` = rdStatus | :329 | **CLEAR** |
| `e` = cardEventID | :332 | **CLEAR** (when supplied) |
| `Content` = reason | :338 | **ENCRYPTED** — the close/change reason becomes the AEAD payload. |

Plus the same always-clear `enc` / `cek_epoch` markers.

> The LIVE write path emits status events via `BuildStatusEventWithIssueRoot`
> (`nostrwire.go:346`), which wraps `BuildStatusEvent` (:374) and adds only extra
> CLEAR routing tags (a second `a` board coord, a NIP-10 root `e`). The encrypted
> `Content = reason` contract is identical — the envelope threads through the
> wrapper, and the extra tags stay clear.

---

## 3. Content wire format (canonical)

```
event.Content = base64Std( nonce(12) ‖ ChaCha20-Poly1305(CEK, nonce, plaintext) )
```

- **AEAD** = ChaCha20-Poly1305 (IETF, 12-byte nonce, 16-byte tag). NOTE: this is
  the AEAD for the per-board CONTENT body. It is DISTINCT from the NIP-44 v2
  envelope (§5), which wraps the 32-byte CEK itself. Do not conflate them.
- **nonce** = 12 random bytes from `crypto/rand`, PREPENDED to the ciphertext
  before base64. A fresh nonce per event.
- **CEK** = the per-board 32-byte current-epoch key (§4).
- **base64** = standard encoding (`encoding/base64.StdEncoding`).

### 3.1 Plaintext payload structs (write and read MUST agree byte-for-byte)

**Card** (`enc`="1"):
```json
{"title": "...", "context": "...", "waiting_on": "...", "labels": ["...","..."]}
```
- `title` — the item title value (moved off the clear `title` tag).
- `context` — the description (was clear `Content`).
- `waiting_on` — the waiting-on value (was clear `waiting_on` tag); omit / empty
  string when unset.
- `labels` — plaintext labels, present ONLY because of tokenization ruling (b),
  so a granted member renders human labels without a reverse dictionary. Omit /
  empty array when the item has no labels. (See §7.)

**Status event** (`enc`="1"):
```json
{"reason": "..."}
```

Encoding is UTF-8 JSON. A future format bumps the `enc` version discriminator;
readers version-dispatch and never guess.

---

## 4. cek_epoch + CEK model (reference — full mechanism = keydist, ready-a8a)

- ONE random 32-byte per-board CEK from `crypto/rand`. **NEVER content-derived**
  — a content-derived key recreates a convergent-encryption equality /
  guess-confirmation oracle (§6).
- The clear `cek_epoch` marker selects WHICH epoch key sealed a given `Content`.
- **Epoch rotation = forward secrecy.** On `rd revoke` / `rd kill`, mint a fresh
  epoch CEK, re-wrap it to the remaining members, and stamp FUTURE cards with the
  new epoch. Historical events stay under their old epoch (a revoked member keeps
  what it already decrypted — an accepted limit; a capability dontguess
  deliberately lacks).
- **CEK distribution:** the 32-byte CEK is NIP-44-v2-wrapped (§5) per member and
  carried INSIDE the owner-signed kind-39301 role grant, so a single owner-signed
  action confers BOTH write authority (grant + relay allowlist) AND the read key.
  The grantee pubkey is bound from the SIGNED grant (its `p` tag / d-tag), NEVER a
  payload field (NIP-44 v2 has no AAD — anti-replay rests on the Schnorr
  signature over the grant). This doc states only the marker contract; the wrap
  mechanism is specified by keydist (ready-a8a).

---

## 5. Crypto primitives (done — ready-62d)

- **CEK wrap** = NIP-44 v2 (`pkg/nip44`, vendored from dontguess, KAT-validated
  against the official paulmillr/nip44 v2 vectors). `Seal(k, counterpartyXOnlyHex,
  cek)` / `Open(k, counterpartyXOnlyHex, payload)`.
- **ECDH** = `*nostr.Key.ECDH(counterpartyXOnlyHex)` returns the RAW 32-byte
  shared-X (NOT sha256(X), NOT `btcec.GenerateSharedSecret`) via a single BIP-340
  even-Y lift. This is the value NIP-44 v2 consumes.
- The one secp256k1 key in `$RD_HOME` both Schnorr-signs AND ECDH-decrypts.

---

## 6. No-plaintext-hash invariant (canonical — enforcement = guardrail, ready-aea)

**rd MUST NEVER emit a plaintext content-hash tag** (e.g. `sha256(title)` or
`sha256(description)`). Such a tag is a **guess-confirmation oracle** (any passive
relay REQ-er hashes a guessed plaintext and confirms it for free, defeating the
AEAD) plus a **cross-card correlation oracle** (identical hashes reveal identical
content across cards/boards).

rd avoids it **by construction**: addressable latest-wins dedupe is
`(author pubkey, kind:30302, d-tag)` — the `d` tag alone is the dedupe key, so no
content-hash tag ever has a legitimate reason to appear. This is an absolute
prohibition, not a "salt it" mitigation, and it is asserted by the guardrail
item's value-scanning lint/test (ready-aea).

The label token of §7 is NOT a violation: it is a KEYED HMAC of a ROUTING tag
value under a secret per-board key, not a hash of any free-text field. The
guardrail still forbids hashing title / description / waiting_on / reason.

---

## 7. Label tokenization (ruling (b), ready-47f → implemented by ready-c83)

Because the graph-leak gate ruled **(b) TOKENIZE**, the labels tag `l` is not
emitted in plaintext on a confidential board.

- **Label Token Key (LTK):** 32 random bytes from `crypto/rand`, minted once when
  a board is made confidential. **STABLE across CEK epochs** — rotating it would
  break equality-filtering of already-published cards' labels. **NEVER
  content-derived.** Distributed to each member NIP-44-v2-wrapped inside the
  owner-signed kind-39301 grant, ALONGSIDE the CEK (a second wrapped blob).
- **Token:** `tag l value = lowercaseHex( HMAC-SHA256(LTK, labelBytes) )`. The tag
  KEY stays `l`; one tag per label; ordering preserved. Equality is preserved
  (same label + same board ⇒ same token), so the relay does exact-match
  `REQ #l:[token]` without seeing plaintext; a different board (different LTK)
  yields a different token, so tokens do not correlate across boards.
- **Rendering:** the plaintext labels ALSO travel in the encrypted `Content` blob
  (§3.1 `labels`). A granted member decrypts and renders human labels; a
  non-member sees only the opaque tokens (honest — preserves label count, hides
  plaintext).
- **Query:** rd filters labels CLIENT-SIDE — `rd list --label X` applies
  `views.LabelFilter` over the projected `Item.Labels` (`cmd/rd/list.go`), and
  board sync fetches whole boards by coordinate (`BoardSyncFilter`, `#a`), never
  pushing an `#l` filter to the relay. So a granted member queries on the
  DECRYPTED plaintext labels — no relay-side tokenization is needed; the token
  exists purely to hide the label VALUE at rest from a non-member relay REQ.
  (Reconciled during ready-c83: the original clause assumed a relay-side `#l`
  filter rd does not use.) IF a future path ever pushes an `#l` filter to the
  relay it MUST tokenize X with the LTK first (`#l:[hex(HMAC(LTK,X))]`) — the
  member holds the LTK via `BoardKeyring.LTK` for exactly that.
- **Do NOT** tokenize `d` or any tag the latest-wins projection keys on. ONLY `l`.
- **Accepted limit:** a revoked member retaining the stable LTK can still compute
  label tokens and correlate them on FUTURE cards (equality only, never
  plaintext) — the same class of accepted limit as historical-read retention, and
  labels are lower-sensitivity than free text. If label forward-secrecy is ever
  required, rotate the LTK on revoke at the cost of cross-epoch query fan-out.

---

## 8. Encrypted-mode fold rules (reference — full gate = foldgate, ready-710)

strfry cannot validate payload shape, so the **LOCAL fold/ingest is the single
enforcement point.** On a confidential board:

- **Drop / quarantine** any incoming 30302 card lacking a well-formed `enc`
  envelope, version-dispatched on the `enc` tag. An unknown `enc` version is
  treated as malformed → dropped.
- **A v-shaped card with smuggled cleartext or a present-but-malformed `enc`** is
  NOT legacy — fail-closed dropped, in both replay and live.
- **Grandfather** genuine pre-cutover plaintext cards ONLY on replay, keyed on
  `created_at` predating the board's first CEK epoch — NEVER on content identity.
- The projection is otherwise UNCHANGED — it still dedupes by clear `d`, filters
  by clear tags, and never reads `Content`.

The "encrypted mode + cutover timestamp" signal is established by keydist
(ready-a8a); foldgate consumes that signal rather than inventing a marker.

---

## 9. Risks inherited from dontguess-541

`dontguess/docs/design/content-confidentiality-envelope-541.md` is "approved
direction, conditional on 3 CRITICAL construction gaps." Each maps to an owning rd
item BY ROLE:

1. **Public plaintext hash = guess-confirmation + correlation oracle** (541 §4.4,
   lines 150-158; A1/P1/P3 in §5). **Owner: guardrail (ready-aea).** rd's
   structural advantage: addressable dedupe `(pubkey,kind,d)` never needed a
   content hash, so rd avoids the oracle by construction (§6 here). The guardrail
   test asserts it can never regress.

2. **Nothing enforces ciphertext-only; a rogue/old admitted client publishes
   cleartext** (541 §6, lines 193-200; A5/P2 in §5). **Owner: foldgate
   (ready-710).** rd's local fail-closed fold is the single enforcement point
   (§8 here). The relay NIP-42 write-allowlist is the first line; residual risk
   collapses to "an admitted member runs a bad client and leaks their OWN
   content."

3. **Partial-plaintext leak via preview/teaser** (541 §4.1, lines 126-138;
   A6/P7 in §5). rd analog: ANY free-text field left on the CLEAR card — the
   `title` value or `waiting_on`. **Owner: write (ready-e63)** — it must move ALL
   free-text fields into `Content`, leaving NO plaintext teaser. rd has no
   preview/teaser feature to delete; the risk is purely "don't leave a clear
   free-text tag behind."

Plus the **Signer-port ECDH gap** (541 §4.5 item 5, line 168; §7 line 206): the
one secp256k1 key must both Schnorr-sign AND ECDH-decrypt — the identity needed an
ECDH accessor. **Owner: crypto (ready-62d, DONE)** — `*nostr.Key.ECDH` added,
KAT-validated.

---

## 10. Divergences — explicitly NOT in this spec

Not copied from dontguess (out of scope for rd confidential boards):

- scrip / pricing / x402 escrow; the settle/deliver gate; `wrapped_cek_buyer`.
- preview-chunk / teaser / Blossom machinery.
- the permissionless sybil stack (behavioral decay / K_eff / burn-floor) — rd's
  flat owner allowlist IS the sybil defense.
- per-ENTRY re-wrap pivot and the always-online re-wrap OPERATOR — rd uses ONE
  per-board CEK wrapped per member inside the owner-signed grant; owner-as-pivot
  only for late-joiner historical reads.
- cross-org / cross-board federation — deferred (decision ready-0af): single owner
  per board for this rollout.

**Owner decision, stated plainly (NOT a bug):** rd's clear projection LEAKS the
whole work GRAPH (status, deps, assignee pubkeys, priority, parent/child tree,
due dates) to any relay REQ — only free text (and, per ruling (b), label
VALUES via tokenization) is hidden. This is the accepted consequence of keeping
routing tags relay-filterable. Revisit trigger: a confidential board is ever
replicated outside the LAN/trusted boundary, or specific labels/assignees
themselves become sensitive.
