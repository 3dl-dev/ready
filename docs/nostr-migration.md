# campfire → nostr migration + dual-read (ready-d65, the CUTOVER)

This documents the **non-destructive** cutover: the existing campfire rd item set
is re-emitted as nostr events with item-for-item parity, a dual-read window lets rd
resolve items from either backend, and rd is proven to operate nostr-only behind a
flag — **without** removing the campfire dependency, changing the default backend,
or disconnecting campfire. Campfire remains the authoritative default backend after
this work. The destructive operational flip (drop the `cf-protocol/store` import,
make nostr the default) is a separate operator-gated item, **ready-f94**.

Epic design (hybrid materialized-card + append-only log, web-of-trust, locked
relays): `rd show ready-a14`.

## What the migration does

`rd nostr migrate` reads the **current campfire/JSONL item set** (the default
backend — never the nostr projection, which would be circular) and re-emits every
item as nostr events, preserving the full item:

| rd field | nostr carrier |
|---|---|
| item id | 30302 card `d` tag |
| title / context | card `title` tag / content |
| status (current) | card `s` tag **and** the last NIP-34 status event |
| priority | card `rank` + `priority` tags |
| type | card `itype` tag |
| deps (`blocked_by`) | card `i` tags (NIP-100 inter-card) |
| gate / waiting | card `gate` / `waiting_type` / `waiting_on` tags |
| labels / eta | card `l` / `eta` tags |
| assignee (`by`) | card `p` tag |
| **full audit trail** | one NIP-34 status event **per history entry** |
| **close-with-reason** | status event `content` |
| **provenance** (who acted) | status event `by` tag (see below) |

The 30301 board (project) is emitted once. Every event is schnorr-signed with the
**allowlisted portfolio key** and appended to the local append-only signed-event
log (`.ready/nostr-log.jsonl`, the epic's source of truth), then best-effort
published to the **locked** write relays.

### Provenance preservation — the `by` tag

Only the portfolio key can *sign* a re-emitted event, but the audit trail must still
record **who originally acted** (e.g. `baron@3dl.dev`, `atlas/worker-3`, `system`).
Each migrated status event therefore carries the original campfire actor in an
rd-extension **`by`** tag. The projection (`pkg/sync.ProjectItems`) prefers the `by`
tag over the event signer when reconstructing `HistoryEntry.ChangedBy`. For live
self-writes there is no `by` tag, so the changer is the signer — identical to the
pre-migration behaviour. This is what keeps `rd show` history provenance
item-for-item with campfire after migration.

### Idempotence

Every nostr event id is a content hash. Re-running the migration over the same item
set re-derives byte-identical events, and `NostrLog.AppendUnique` / relay dedup drop
the repeats — a re-run appends **0** events and can never fork or duplicate an
item's history. The board is stamped at a deterministic `created_at` (the earliest
item's second) rather than `time.Now()` so it, too, dedups on re-run.

## Dual-read

`RD_NOSTR_READ=1` flips a **single process** to resolve rd items from the nostr
projection instead of campfire/JSONL — it is the controlled, nostr-only
verification context. It is **off by default** and additive: rd's whole read surface
(`list` / `ready` / `show`) runs against nostr without changing the live default, so
the campfire-backed rd everyone else runs is never disturbed. `rd nostr show`,
`rd nostr ready`, and `rd nostr parity` are always nostr-sourced regardless of the
flag.

## Parity proof (`rd nostr parity`)

Derives the source from campfire/JSONL, projects the nostr log, and asserts
item-for-item parity on: count, status, priority, type, deps, gate, history length,
close-reasons, and provenance. Exits non-zero on any mismatch (a lost or silently
altered item). Ground-source: it reads the **real** live item set — never fabricated.

Ground-source demo: `scripts/nostr-migration-parity-demo.sh`
(captured run: `docs/nostr-migration-parity-demo.out`). On the live 1565-item set:

```
STEP 1  FULL local-authoritative parity: source=1565 projected=1565 matched=1565 mismatched=0
STEP 1b re-run appended 0 events; log stayed at 9244 lines  (idempotent)
STEP 2  35-item dep-closed sample round-tripped through the LOCKED relays; reconstructed field-for-field
STEP 3  dual-read `rd list` == campfire: 1565 items (RD_NOSTR_READ=1; campfire still default)
```

The **local authoritative log** is the parity substrate, per the epic invariant
(relays are replaceable caches). The live-relay step proves the events survive the
**locked** strfry relays (the allowlisted key passes the ready-266 write-allowlist,
`buffered=false`) and dual-read reconstructs them — done per-item (`#d`-filtered
reconcile) to avoid strfry's per-query result cap and cross-project events that
accumulate on the shared relays.

## Accepted limitation — same-second ordering (ready-194)

`created_at` is **seconds** (NIP-01 granularity), and replay orders events by
`(created_at, event-id)` — a same-second pair is ordered by the NIP-01 id tie-break
(lowest id first), i.e. **same-second == concurrent**. Two consequences for
migration, both handled:

1. **Within one item's history**, campfire's monotonic-nanosecond order is
   ground-truth. Two entries in the same second would otherwise be reorderable by
   the id tie-break — which can silently corrupt the item's *current status* (a
   create+cancel in one second projecting back as `inbox` because the lower-id
   cancel sorts first). The migration preserves the known chain order by stamping
   **strictly-increasing** `created_at` within an item's history (nudging a
   colliding entry forward by whole seconds). The only cost is that a same-second
   entry's *displayed* timestamp may shift up to a few seconds. Parity compares
   history order-independently (multiset of `(to_status, note, actor)`), so the
   shift is not a false diff while a lost/added/altered entry always is.

2. **Across items** (and across machines), two genuinely same-second events are
   treated as concurrent and ordered by id — the accepted ready-194 limitation. rd's
   low write concurrency makes real same-second collisions on one item rare; the
   deterministic id tie-break keeps replay **convergent** across the local log, a
   relay reconcile, and a cross-machine merge (none of them can change an event's
   `(created_at, id)` key).

The current-state card is stamped at the item's `UpdatedAt` second (floored to just
after the newest status event) so a card-only edit (`rd dep add`, `rd label`,
`rd defer`) — which advances `UpdatedAt` without adding a history entry — produces a
card that deterministically beats any older card of the same item on a shared relay.
