# Rollback runbook: the confidential-boards re-seal pass (ready-402)

Companion to `docs/confidential-reseal-announcement.md`. That document says
who is affected; this one says what a human does if the re-seal pass
(ready-5e7, executing option (b) of the ready-336 ruling) needs to stop
partway, and exactly what "rollback" can and cannot mean here.

## The honest constraint, stated first

**"Rollback" does NOT mean "restore the plaintext."** Publishing the original
plaintext again — even to reverse a mistake — re-exposes the exact content
this operation exists to protect, to the exact audience (anyone with relay
access, no grant needed) it exists to cut off. Doing that *defeats the
operation to fix the operation*. It is never the right move here, no matter
how far the pass got or how it failed.

What rollback actually means, for this pass, in order:

1. **Stop the pass.** Nothing more gets touched.
2. **Establish what was already re-sealed** — which coordinates on which
   boards already got a sealed replacement before the stop.
3. **Confirm those coordinates are intact and readable** by everyone who
   currently holds the board's current CEK epoch, verified **off the relay**,
   not off any machine's local log.
4. **State plainly what is not recoverable** — and it is a short, specific
   list, not "everything is fine."

There is no step 5. There is no "undo." A coordinate that got re-sealed
before the stop stays re-sealed; a coordinate that had not been reached yet
stays plaintext (unaffected, not itself a failure) until the pass resumes.

## Step 1 — Stop the pass

The pass (ready-5e7) is per-board and per-coordinate by mandate, specifically
so this step is cheap and safe:

```
# however the pass is being driven for that board (the ready-5e7 executor,
# or a manual loop over ready-207's per-board coordinate list) — interrupt it:
kill <pid-of-the-reseal-process>       # or Ctrl-C if running attached
```

There is no partial-write hazard to clean up from the interrupt itself: each
coordinate's re-seal is one signed, addressable-replacement publish. It
either reached a relay and superseded the plaintext coordinate, or it did
not — there is no "half-resealed" event. The only thing "stopping mid-pass"
produces is a **board with some coordinates resealed and others still
plaintext**, which is an expected, safe, resumable state (this is exactly
what a genuine crash mid-board would also leave, so the same recovery steps
below apply to "we chose to stop" and "it fell over" alike).

## Step 2 — Establish what was already re-sealed

Re-run the per-board coordinate inventory (ready-207's script, or its
successor) against the board **right now**, and classify every coordinate by
what the relay currently serves for it:

```
rd relay audit --relay wss://relay.3dl.network --board 30301:<owner>:<boardD>
```

This confirms the coordinate set and the winning event per coordinate match
between the local log and the relay (dry-walked below: `match=true`,
`missing_coords=0`, `stale_coords=0` even with one coordinate already
re-sealed — the standard audit check already tolerates a re-seal mid-board).
For the plaintext/sealed split specifically — which is what "what got
re-sealed" actually means here — query the board's cards directly by `#a`
coordinate filter, take the latest event per `(kind, pubkey, d)` (NIP-01
replaceable semantics — what a relay actually serves), and check the `enc`
tag:

- **`enc` present, `cek_epoch` matches the board's current epoch** →
  already re-sealed by this pass. Leave it. Do not touch it again.
- **no `enc` tag** → not yet reached. This is where the pass resumes from —
  re-derive the remaining set from ready-207's inventory, do not trust a
  stale checkpoint file (the mandate in ready-5e7: other projects are
  writing concurrently).

Diff this against the last full pre-pass inventory (ready-207) to get an
exact per-board "N of M resealed, M-N remaining" count — that count, not a
guess, is what goes in the stop-point report to the owner.

## Step 3 — Confirm intact + readable, off the relay, never the log

This is the step that actually matters and the one most likely to be faked
by accident: reading the re-sealed card back through the **same machine that
just wrote it** proves nothing, because that machine's local log already
holds the plaintext original and will happily decrypt (or worse, silently
serve the cached plaintext) regardless of whether the relay is in any state
at all. The verification has to go through a keyring built **only** from
what the relay currently serves.

```
# fresh, empty RD_HOME + fresh project dir — no pre-existing local log for
# this board coordinate:
rd --rd-home <clean-home> init --public --relay wss://relay.3dl.network \
    --name verify-stub --no-commit-binding
rd --rd-home <clean-home> link 30301:<owner>:<boardD>

# forces a reconcile against the configured relay for the now-linked board —
# assert the log had nothing for this coordinate before this step:
rd --rd-home <clean-home> list --all
rd --rd-home <clean-home> show <resealed-item-id>     # must render the real
                                                       # title/context, not
                                                       # "[encrypted]"
rd --rd-home <clean-home> show <untouched-item-id>    # must still render its
                                                       # plaintext title
                                                       # directly (proves the
                                                       # stop point, not a
                                                       # partial write)
```

(In production, the equivalent clean-bootstrap path a real second machine or
teammate would use is `rd follow <owner>` / `rd join <token>` into a fresh
`RD_HOME` — the `init --public && link` sequence above is a same-owner
stand-in used for this dry-walk because it needed no invite ceremony; either
path produces a log that starts with nothing for the board being verified.)

If `show` renders the resealed item's real content from a clean bootstrap,
every current-epoch holder can do the same — that is the property "rollback"
is actually protecting: not that history looks unchanged, but that the
people who are supposed to still have access, still do.

## Step 4 — What is NOT recoverable

State this without softening:

- **The original plaintext event, once superseded, is gone from that relay.**
  Dry-walked below: after re-seal, the old event id could not be fetched
  even directly by id (not just by coordinate) — this relay evicts on
  addressable replacement, it does not keep a shadow copy. Nothing in rd
  recovers it from the relay side.
- **The original plaintext survives only in local append-only logs** — the
  machine(s) that wrote or synced it before the re-seal. That is exactly
  what makes the re-seal safe to execute at all (nothing is destroyed at the
  source of truth) and exactly why it is not a path back to "undo": those
  logs are wherever they already were, not a recovery mechanism the pass
  can reach into.
- **Anyone who already read the plaintext before the re-seal keeps whatever
  they read.** Caches, archives, another relay, a screenshot, a scraper that
  ran last week — the re-seal changes what a relay serves **from now on**;
  it cannot reach into the past and un-serve what already went out. This is
  true regardless of whether the pass completes cleanly, gets interrupted,
  or is rolled back in the only sense rollback has here. It is the reason
  "restore the plaintext to undo a mistake" is never on the table: the
  exposure a mistake here could cause is already-served history repeating,
  not history disappearing.
- **A coordinate stranded by the 64 KiB relay limit (ready-c3e) is not lost
  either way** — the client-side size guard refuses the oversized publish
  before it happens, so that coordinate simply stays plaintext, flagged, not
  silently dropped and not a rollback case.

## Dry-walk record

Walked live against a throwaway test board on the real relay
(`wss://relay.3dl.network`), 2026-07-30, board coordinate
`30301:a9f766ae56bbf466d2d361e5b1788b7cd689fd8e3b418e35b002b313f478db25:proj`
(created for this dry-walk; not a project board):

1. Created two plaintext items on a `--public` board (`proj-33e` "item A",
   `proj-312` "item B"). Confirmed both plaintext on the relay (title in a
   plaintext tag, no `enc` tag).
2. `rd confidential enable` → board confidential at epoch 1; both items
   confirmed **still plaintext on the relay** (grandfathered), matching the
   documented behavior.
3. Simulated "the pass reaches item A, then stops": one mutation on item A
   only (`rd progress`, which rebuilds and republishes the whole card).
   Read-back off the relay: item A now carries `enc=1 cek_epoch=1`, a new
   event id, `content_len=236` (sealed ciphertext), no plaintext title tag.
   Item B: **unchanged**, still the original plaintext event id and title —
   this is the stop-point state step 2 above is written to detect.
4. `rd relay audit --relay wss://relay.3dl.network` against this board:
   `match=true items local=2 relay=2 events local=9 relay=6 definition=true
   missing_coords=0 stale_coords=0 missing_status=0 verify_failures=0` — the
   standard audit already tolerates one coordinate mid-board being resealed
   and the other not.
5. From a clean `RD_HOME` (no prior events for this board coordinate,
   asserted by log-line count immediately after linking) reconciling only
   from the relay: `rd show proj-33e` rendered the real title
   ("grandfathered plaintext item A"), not `[encrypted]` — step 3's
   verification, done for real. `rd show proj-312` still rendered its
   original plaintext title directly, confirming the untouched coordinate is
   exactly where the pass left it.
6. Attempted to fetch item A's **original** (pre-reseal) event id directly
   by id, not by coordinate: not found — this relay evicts fully on
   addressable replacement. This is the concrete basis for step 4's "gone
   from that relay" claim; it says nothing about copies elsewhere, which is
   exactly the point step 4 also makes.

Known caveat of the dry-walk rig, not a product defect: the clean-verify
project stub was bootstrapped with its own local `--public` flag before
being re-linked to the test board, so its own `rd confidential status`
display reported "PUBLIC" (reading its own stale local opt-out flag) even
though `rd show` correctly decrypted the sealed card using only relay-sourced
grants — the decrypt path (what this runbook depends on) reads the board's
actual state; the status *display* command in this particular stand-in setup
did not. A real `rd follow`/`rd join` bootstrap does not carry this
mismatch, since it derives the local flag from the discovered board rather
than an operator-supplied `--public` at init time.
