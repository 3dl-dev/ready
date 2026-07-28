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
