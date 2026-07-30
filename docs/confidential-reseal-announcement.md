# Announcement: grandfathered plaintext history is being re-sealed (ready-402)

Owner ruling, ready-336, 2026-07-30: **RE-SEAL.** Every confidential board's
grandfathered plaintext cards are being republished sealed under the board's
current CEK epoch. Options that would have kept plaintext readable
("relabel", "seal forward only") were withdrawn — the standing policy is
"confidentiality is the default and privacy is mandatory."

This document says which boards, what a reader loses, what they need, and
when — with the affected-reader list derived from real `kind-39301` grant
history read directly off `wss://relay.3dl.network`, not assumed.

## Which boards

11 boards carry grandfathered plaintext (confidential mode is enabled, but at
least one card predates that and was never re-sealed). Measured fresh
**2026-07-30T03:55Z**, `#a`-coordinate-filtered, paged, no `authors` filter,
latest-event-per-coordinate (what an outsider bootstrapping from the relay
actually sees today):

| board | total cards | plaintext (readable by anyone) | sealed |
|---|---:|---:|---:|
| 3dl | 555 | 535 | 20 |
| analyst0 | 43 | 16 | 27 |
| dontguess | 647 | 547 | 100 |
| forge | 201 | 183 | 18 |
| galtrader | 754 | 646 | 108 |
| mainframe | 172 | 60 | 112 |
| mallcoppro | 1029 | 647 | 382 |
| nostrrelay | 52 | 10 | 42 |
| ready | 515 | 167 | 348 |
| resonant | 101 | 100 | 1 |
| vat | 242 | 20 | 222 |
| **total** | **4311** | **2931** | **1380** |

(Board selection: every LIVE board owned by `a9f766ae56...` with
plaintext > 0 AND sealed > 0 in ready-336's full 24-board portfolio table —
i.e. confidential-with-a-plaintext-tail. Boards that are fully plaintext
(confidential mode never enabled) are out of scope: there is no epoch to
re-seal under. Boards that are already fully sealed have nothing left to do.
This table supersedes ready-336's 2026-07-29T04:19Z snapshot for these 11
boards — the portfolio is live and numbers drift; re-measure again
immediately before the pass executes, per ready-207.)

## What a reader loses

Today, **anyone** — a grantee, a passerby who found the relay, an archive, a
scraper — can read these 2,931 plaintext cards with no key and no grant. That
is the defect ready-336 exists to fix, and it is the whole point of this
operation: **that universal readability ends.** Once a card is re-sealed,
only someone holding the board's *current* CEK epoch can read it. This is a
real capability being removed, not a formality — do not read the rest of
this document as "nobody loses anything."

Two categories of reader are affected, and they are not the same:

**1. The general public / unaffiliated readers — the deliberate target.**
Anyone reading these boards today without a grant loses read access
entirely, permanently, by design. There is no one to notify individually
here; cutting this off is the fix ready-336 was raised for.

**2. Named grant-holders who do not hold the board's current CEK epoch.**
This is the category the item asked to derive from real data rather than
assume. Method: for each of the 11 boards, every `kind-39301` grant was
pulled off `wss://relay.3dl.network` by `#a` coordinate filter (paged,
`until`-walked, limit 500 — never an `authors` filter), giving, per grantee,
their latest role and the highest CEK epoch ever wrapped to them (per-epoch
grant slots, ready-889, mean every epoch a member ever received survives
independently on the relay, so "highest epoch held" is a straight max, not
an inference).

**Result, measured 2026-07-30T03:55Z: zero.** Every current, non-revoked
grantee on all 11 boards already holds the board's current CEK epoch:

| board | current epoch | grantees checked | below current epoch |
|---|---:|---:|---:|
| 3dl, analyst0, dontguess, forge, galtrader, mainframe, mallcoppro, nostrrelay, resonant, vat | 1 | owner + 1 contributor, each | 0 |
| ready | 2 | owner + 3 contributors (+ 1 revoked, excluded — already has no read access) | 0 |

This is not a promise that no one will ever be affected — it is what the
grant log says **right now**. It holds because of how membership on this
project actually works: every grantee here was granted (or re-granted) after
their board's most recent `confidential enable`/`rotate`, so nobody is
carrying a stale key forward. **Re-verify this table immediately before
executing the pass on each board** (`cmd/measure402tmp`-style query, or its
successor in ready-207/ready-43d) — an active session between now and
execution could add a grantee who received a role but no CEK wrap, or a
revoked member could be reinstated without a fresh grant. Either would land
in the "below current epoch" column and needs a grant before that board is
re-sealed, not after.

## What an affected reader needs

A **current-epoch grant**. Concretely: the board owner runs

```
rd grant <your-pubkey-hex> [role]
```

(or `--all-boards` to cover every board they own at once). On a confidential
board this wraps the CEK for the epoch in effect *at grant time* into the
signed grant, so a grant issued after the re-seal pass on a given board
receives that board's post-pass (current) epoch and can read everything,
including the freshly re-sealed history. There is no separate "give me back
the old plaintext" step, and there should not be one — see the rollback
runbook for why.

If you are already a grantee and this table shows you below the current
epoch by the time the pass runs on your board, ask the owner for a fresh
`rd grant` before that board's pass starts, not after.

## When

Not yet executed. The pass is ready-5e7, blocked on:
- **ready-43d** — a dry run that proves, per board, exactly what would change
  and writes nothing, approved per board before any write touches it.
- **ready-fcd** — `rd relay audit` learning to report a re-sealed coordinate
  as superseded, not missing, so the first post-pass audit does not cry wolf.
- **ready-402** (this item) — this announcement + the rollback runbook.

Execution is **per board**, not one sweeping operation, so a problem on one
board does not touch the other ten. Watch `rd gates` / this document's
follow-up notice for the actual go date once ready-43d's dry run is approved.

## What does NOT change

- Nothing is deleted from any project's local append-only log
  (`.ready/nostr-log.jsonl`). The original plaintext event stays there,
  forever, on the machine(s) that already hold it. What changes is what a
  relay serves to a fresh reader who was not already holding a copy.
- Already-sealed cards are untouched. This pass only touches the 2,931
  grandfathered-plaintext coordinates.
- The 3 cards already known to be stranded past the relay's 64 KiB limit
  (ready-c3e) are not silently dropped — they are refused up front by the
  now-shipped client-side size guard and stay in their current (plaintext)
  state, flagged for separate disposition, not lost.
