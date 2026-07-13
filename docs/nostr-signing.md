# rd nostr signing (ready-41d)

Generic nostr NIP-01 event pipeline used by rd to publish work-management state
to the self-hosted strfry relays. This item proves the generic
sign → publish → relay-accept → verify loop and the tamper-rejection path. It
does **not** map rd items to events (that is the downstream item ready-a13).

## Package `pkg/nostr`

| File | Responsibility |
|------|----------------|
| `event.go` | `Event` type, NIP-01 canonical serialization, event-id derivation (sha256), `Sign`, `Verify` |
| `key.go` | secp256k1 portfolio key: generate / hex round-trip / file persistence (0600) / `LoadOrCreatePortfolioKey` |
| `client.go` | `Publish` (send `["EVENT",…]`, await `["OK",id,true,…]`) and `Fetch` (`["REQ",…]` read-back) over websocket |

### Canonical event id (NIP-01, exact)

The event id is `sha256` of the UTF-8 JSON array

```
[0, <pubkey>, <created_at>, <kind>, <tags>, <content>]
```

serialized with **no insignificant whitespace** and the NIP-01 string escape
set: backslash escapes for `"` `\` `\n` `\r` `\t` `\b` `\f`, `\u00XX` for other
control bytes below `0x20`, and every other byte (including `<` `>` `&` and all
multi-byte UTF-8) emitted verbatim. `encoding/json` is deliberately **not** used
for the id preimage because it HTML-escapes `<` `>` `&`, which would change every
id. See `serializeString` in `event.go`.

### Signature

BIP-340 schnorr over secp256k1 (`btcec/v2/schnorr`), signed over the 32-byte id
digest, x-only pubkey, lowercase hex. btcec's default schnorr signing is
deterministic, so rd and the reference `nak` client agree on **both** id and sig
byte-for-byte for the same input.

## Key storage

The secp256k1 signing key (`nostr-identity.json`) lives under `$RD_HOME`
(default `~/.config/rd`) — the sole identity home post-cutover; there is no
`.cf` and no separate campfire identity to compose with. `$RD_HOME` is
git-ignored, so the secret is never committed. See
[docs/design/nostr-identity-model.md](design/nostr-identity-model.md) for the
full `$RD_HOME` resolution cascade and per-actor key layout.

*(Historical note: this doc was written for ready-41d, before the `$RD_HOME`
cutover, when the key lived in `.cf` alongside the campfire identity. The
signing/serialization mechanics below are unchanged; the storage location
described above is current.)*

## Ground-source proof (live, no mocks)

`scripts/nostr-sign-demo.sh` (Go helper `scripts/nostr-demo`):

1. Cross-checks the Go signer against `nak` — asserts id **and** schnorr sig
   match byte-for-byte.
2. Against a **live strfry relay** (endpoints from `pkg/rdconfig`, not
   hardcoded): builds + signs an event in Go, publishes via the Go publisher
   (relay answers `OK,true`), reads it back, independently `Verify`s (ACCEPT),
   then tampers a byte and `Verify` REJECTS.

Captured output: `docs/nostr-sign-demo-output.txt` (both relay-a and relay-b).

Go unit tests (`pkg/nostr/*_test.go`) cover the known NIP-01 id vector, the
escaping rules, a byte-exact sign vector cross-checked against nak, sign/verify
round-trip, forged-signature rejection, and a full tamper-rejection matrix. The
live relay test (`live_relay_test.go`) is gated behind `RD_NOSTR_LIVE_RELAY=1`
so the default suite stays green where no relay is reachable.
