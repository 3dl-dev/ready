# web/board

Static placeholder page for `ready.3dl.dev/board` (ready-2f1). This is
**not** the board UI — it exists to prove the TypeScript build pipeline
reaches production. The eventual board UI will read `docs/design/board-fold-spec.md`.

## Toolchain

- **Bundler**: Vite. Chosen over esbuild-direct because Vite's default
  config already produces hashed, content-addressed asset filenames and a
  self-contained `dist/` with zero runtime CDN references, which is exactly
  what this item needs, without hand-rolling that in a raw esbuild script.
- **Language**: TypeScript, checked with `tsc -b` before bundling (`npm run
  build` runs `tsc -b && vite build` — a type error fails the build).
- **Node version**: pinned in `web/.nvmrc` (20.17.0). CI reads it via
  `actions/setup-node`'s `node-version-file`.
- **Lockfile**: `package-lock.json` is committed. CI runs `npm ci`, never
  `npm install`, so the build is reproducible from a clean clone.

## Build

```sh
cd web/board
npm ci
npm run build      # outputs to web/board/dist/
```

`dist/` is git-ignored — CI builds it fresh on every deploy and copies it
into the Pages artifact at `/board/` (see `.github/workflows/pages.yml`).
The root `ready.3dl.dev` site (`site/`) is copied in unmodified alongside
it, so this does not touch the existing root site.

The page embeds a build-stamp (the deploying commit SHA, via the
`VITE_BUILD_STAMP` env var set in CI) rendered as `<code id="build-stamp">`
in the page, e.g. `build:abcdef0…` — useful for confirming a specific
commit actually deployed.

## Known accepted risk

`npm audit` reports a moderate esbuild advisory (GHSA-67mh-4wv8-2f99) that
only affects Vite's **dev server** accepting arbitrary requests; it does
not affect the production `vite build` output used here, and this
toolchain never runs `vite dev` in CI or production. Revisit when Vite
ships a release with a patched esbuild in the 5.x line, or on the next
routine dependency bump.

## Live proofs (manual — CI cannot run these)

`scripts/` holds the harnesses that exercise the real relay, a real Chromium
and the real `rd` binary. They need a live relay, a Chromium on disk and the
local machine's allowlisted rd signing key, so they are run by hand, not in CI.
Each one states in its own header exactly what is real in it and what is not.

```sh
node scripts/live-parity.mjs                        # the fold agrees with rd, live
node scripts/live-write-roundtrip.mjs [--confidential]   # the 7 write ops, read back independently
node scripts/live-roundtrip-both-ways.mjs [--confidential]  # both directions (ready-4359)
node scripts/live-stranger-walk.mjs                 # the 8-step stranger walk (ready-48f)
node scripts/live-portfolio.mjs                     # every board on one page (ready-27b)
node scripts/live-cache.mjs [--only a|b]            # cold vs WARM, and a stale cache losing (ready-fe4)
```

`live-stranger-walk.mjs` is the one that uses a **real NIP-07 extension**. It
clones nos2x at a pinned commit, builds its MV3 bundles, loads it unpacked into
three separate cold Chromium profiles (`--load-extension`), seeds each one's own
`chrome.storage.local` with a freshly generated key, and serves the built bundle
over real TLS. It then walks ready-48f end to end with no manual step: `rd board
share` mints a claim link, an ungranted stranger opens it over https and sees the
board's cards as `[encrypted]` placeholders, the owner runs `rd grant --claim
<nonce> <pubkey>`, the **still-open page** unwraps the CEK through the extension
and fills the titles in (no reload — a `window` sentinel proves it), what is on
screen is compared against `rd list --json` read by an independent `rd`, a second
fresh key is refused the spent nonce, and `rd board share <npub>` lands a third
fresh key on a populated board in one command. It needs network access to fetch
the extension's source on first run (cached under the OS temp dir afterwards).

`live-roundtrip-both-ways.mjs` is the end-to-end one: a real browser moves a
card, edits a title and approves a gate; an independent `rd` (clean `RD_HOME`,
empty log, trust set = the board owner) reads all three back off the relay with
the right actor and reason; then the **rd CLI** changes the board and the
still-open browser shows it through its live subscription, with no reload — the
page's `window` sentinel is checked afterwards, because "it reloaded itself"
would explain the same screen.

`live-cache.mjs` is ready-fe4's, and it measures the cache the only way a cache
can honestly be measured: **cold against warm**, same build, same relay, one
visit apart. Part A opens the operator's real portfolio twice in one Chromium
profile and stamps every instant *inside* the page — a `MutationObserver` marks
the mutation that put the first card in the DOM, and a `WebSocket` subclass
installed before any page script marks the first inbound relay frame, so "paints
before any relay round-trip completes" is checked rather than assumed. Part B
proves condition 4 by the method the condition names: the board is loaded once
so it caches, the browser leaves, the **rd CLI renames an item**, and the next
visit paints the *stale* title from localStorage (asserted at the first-paint
instant — a cache that painted nothing could not lose to anything) and then, in
that same document, converges to the newer one. A second rename lands while the
page is on screen. A `window` sentinel is checked after both, because "it
reloaded itself" would explain the same screen.

The deterministic halves of the same guarantees run in CI:
`src/lib/relaylive.test.ts`, `src/main.live.test.ts`,
`src/board/nostrwriter.absorb.test.ts`, `src/board/render.liveupdate.test.ts`,
`src/board/writeevents.vectors.test.ts`, `src/board/nostrwriter.test.ts`.

## Confidential boards (ready-c4b)

A confidential rd board encrypts only free text — title, description,
`waiting_on`, labels — into `event.Content`; every relay-indexed routing tag
stays clear. The wire contract is frozen in
`docs/design/confidential-boards-envelope.md`; the Go writer is
`pkg/sync/envelope.go`. The browser reads it like this:

1. Fetch the board's owner-signed kind-39301 role grants alongside the
   kind-30301 board (`main.ts`). A confidential board's read key rides inside
   the grant, so one REQ carries both "which board" and "can I read it".
2. Unwrap the per-board CEK by asking the NIP-07 signer —
   `window.nostr.nip44.decrypt(ownerPubkey, wrappedCEK)` (`keyunwrap.ts`).
   **The secret key never enters the page.** That is the architectural premise
   of the feature, not an optimisation.
3. Open `base64(nonce(12) ‖ ChaCha20-Poly1305(CEK, plaintext))` under that CEK
   (`envelope.ts`), and project the cards into items (`carditems.ts`).

The wrapped key's NIP-44 plaintext is 64 lowercase hex characters, not 32 raw
bytes, because NIP-07's `nip44.decrypt` returns a *string* and every extension
finishes with a UTF-8 `TextDecoder` — raw key bytes would come back corrupted
with no error anywhere. See `pkg/sync/keydist.go`'s `WrapKey` and
`TestKeydistWrapPayloadIsHexForBrowserSigners`.

### Fail closed

When a title cannot be decrypted — no grant, the wrong CEK epoch, a tampered
ciphertext or auth tag, a truncated envelope — the UI renders the placeholder
`[encrypted]`, byte-identical to what `rd list` prints. It never renders
ciphertext, a partially-decrypted string, a blank title, or the clear `title`
tag (a confidential card does not carry one; if one appears, that is an attack
shape). ChaCha20-Poly1305 is an AEAD: a tag mismatch means tampering *or* the
wrong key, and both fail. `src/lib/envelope.test.ts` mutation-proves each of
those cases against real Go-sealed bytes.

### Nothing is persisted

There is no cache of decrypted titles and no cache of key material. The keyring
is rebuilt from signed relay events on every load. That costs one signer prompt
per load and buys "a stolen browser profile contains no board keys and no board
text". `src/nostorage.test.ts` enforces it structurally: no shipped module may
even reference `localStorage`, `sessionStorage`, `indexedDB` or
`document.cookie`.

### Dependencies

`@noble/ciphers` is the one runtime dependency (ChaCha20-Poly1305). Earlier
board crypto (`sha256.ts`, `secp256k1.ts`, `bech32.ts`) was hand-rolled because
`dist_test.go`'s external-reference scan rejected every `//` in the bundle,
which banned dependency license banners outright; ready-8c5 fixed that guard, so
a vetted library is viable again — and an AEAD is a much worse hand-roll
candidate than a signature verifier (silent failure mode, constant-time
130-bit arithmetic). `@noble/curves` and `@noble/hashes` are **dev**
dependencies only: they back `nip44ref.ts`, the spec-validated NIP-44 v2
implementation that stands in for a browser extension in tests. Nothing
reachable from `index.html` imports them.

### Regenerating the test fixtures

`src/lib/confidential.fixtures.ts` is generated from the real Go writer:

```sh
go run ./web/board/testdata/genconfidential/main.go > web/board/src/lib/confidential.fixtures.ts
```

Every key in it is freshly generated test-only material.
