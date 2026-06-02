# Upgrade: rd on campfire v0.17.5 → v0.32.0

This release moves rd onto campfire **v0.32.0** (the cf 0.30 layered/frozen-wire
release plus 0.31 storage scaling and 0.32 multi-consumer surface) and adopts the
cf-authority delegation-grant model for authorization. This note is the upgrade
story: **what happens to existing data when the new rd binary drops in.**

**TL;DR — it is drop-in safe.** Existing campfires, items, members, and local
stores are read transparently with no migration step. The authority change is
dual-write / dual-read, so existing members keep working from the moment the new
binary lands.

## What is read transparently (no action required)

| Area | Why it is safe | Evidence |
|------|----------------|----------|
| **Work items (`work:*` messages)** | The wire format is frozen at cf 0.30; message envelopes, signing domain, and hop chains are unchanged. | `docs/0.30-wire-format-freeze.md` (campfire) |
| **Message IDs** | rd has minted RFC-4122 UUID message IDs since v0.17.5, so existing messages already satisfy the UUID validation added in 0.31. Validation runs only on **write/ingress**, never on reading existing messages. | `cf-protocol/internal/message/message.go` (`uuid.New()`), `…/transport/fs/fs.go` (`ValidateID` on write) |
| **On-disk store layout** | The 0.31 filesystem read path **dual-reads** both the legacy flat layout (`messages/*.cbor`) and the new bucketed layout (`messages/<YYYY-MM>/<DD>/*.cbor`). Old stores are read as-is. | `cf-protocol/internal/transport/fs/fs.go` ("v0.31 dual-read"); proven end-to-end by `test/e2e/upgrade_test.go` |
| **Existing members (`work:role-grant`)** | The new convention-server gate **falls back to allow** for any sender that has no cf-authority `delegation:grant` yet. Members admitted by the old rd keep acting unchanged. | `pkg/conventionserver/gate.go`; `TestGrantGate_NoGrantLegacyFallbackAllowed` |
| **`.ready/mutations.jsonl` and `.ready/pending.jsonl`** | Formats unchanged. Buffered pending entries carry UUID IDs (always have), so they still flush. | — |

`cf migrate-store` (which rewrites a flat store into the bucketed layout) is a
**storage-performance optimization, not a correctness requirement** — the new rd
reads the flat layout regardless.

## What to be aware of (non-fatal)

1. **Local SQLite schema.** 0.31/0.32 added tables (`fs_sync_cursors`,
   `pending_messages`). The store creates them on open (create-if-not-exists), so
   this self-heals. If you maintain a long-lived store, smoke-test once after
   upgrading (`rd list` / `rd ready` against a copy).

2. **Beacon directory relocation.** As of 0.31, `beacon.DefaultBeaconDir()` is
   `CF_HOME`-scoped. A single-`CF_HOME` machine still falls back to
   `~/.cf/beacons` / `~/.campfire/beacons`, so normal installs find their beacons.
   Only multi-identity / explicit-`CF_HOME` setups (e.g. simulating two machines
   on one box, or a shared-filesystem mount) need an explicit
   `CF_BEACON_DIR`, or a re-run of the idempotent `rd init`.

## The authority model change (dual-write migration)

The new rd replaces the homegrown `work:role-grant` authorization with
cf-authority delegation grants, **without a flag day**:

- `rd admit` now **dual-writes**: it posts both the legacy `work:role-grant`
  (so existing readers keep recognizing the member) **and** a cf-authority
  `delegation:grant` (so the gate can enforce).
- The convention-server gate **dual-reads**: a sender with a `delegation:grant`
  is enforced (revoked or out-of-scope → denied); a sender with none is treated
  as a legacy member and allowed.
- `rd kill <pubkey>` revokes a grant-holder (`identity:revoked`); the gate denies
  them within one sync cycle. `rd sessions` lists active grant-holders;
  `rd show --audit` renders each history entry's authority.

**Carry-forward (deferred, tracked):**
- **Cutover** — flipping the gate to *deny* senders with no delegation grant —
  is deliberately deferred until every active member has been re-granted (i.e.
  has been through a new-rd `rd admit`, or a backfill is run). Until then the
  legacy fallback keeps the system open.
- Admit-issued grants currently use a 365-day TTL; a renewal/backfill story is a
  follow-up.
- Relay-aware `client.Join` is still pending upstream (`campfire-agent`
  `campfireagent-848`); relay *create* already landed in v0.32.0.

## Verification

- `test/e2e/upgrade_test.go` — seeds a campfire, **flattens its store to the
  legacy pre-0.31 layout**, then has a fresh member join and list: the items must
  come through, proving the dual-read end-to-end through rd.
- `pkg/conventionserver/gate_test.go` — proves the gate allows owner / in-scope /
  legacy-no-grant and denies revoked / out-of-scope, i.e. that the upgrade does
  not block legitimate existing members.
