# Announcement: grandfathered plaintext history is being re-sealed (ready-402)

Owner ruling, ready-336, 2026-07-30: **RE-SEAL.** Every confidential board's
grandfathered plaintext cards are being republished sealed under the board's
current CEK epoch. Options that would have kept plaintext readable
("relabel", "seal forward only") were withdrawn — the standing policy is
"confidentiality is the default and privacy is mandatory."

This document says which boards, what a reader loses, what they need, and
when.

## Where these numbers come from — and how to re-derive them

Every figure below is the output of **ready-43d's dry run**, run against the
public relay:

```
go run ./scripts/resealplan            # add --json-out plan.json for per-coordinate detail
```

It reads only, proves it wrote nothing (it hashes the local append-only log
before and after and refuses to exit 0 if the digest moved), and reports per
board what a re-seal *would* do. **Run it again immediately before the pass
executes.** The portfolio is live and eight projects write to it
concurrently; the numbers drift daily, and an earlier revision of this
document carried a hand-rolled query that was wrong about both the board
count and the affected-reader count within five days.

Snapshot below: **2026-08-05T15:52:53Z**.

## Which boards

**21 boards are confidential** (they carry a CEK-bearing grant). Across them,
5,104 cards; **3,045 would be re-sealed**.

| board | epoch | cards | would re-seal | already sealed |
|---|---:|---:|---:|---:|
| 3dl | 1 | 555 | 535 | 20 |
| analyst0 | 1 | 43 | 16 | 27 |
| augur | 1 | 45 | 0 | 45 |
| dap | 1 | 6 | 0 | 6 |
| dontguess | 1 | 649 | 547 | 102 |
| enterpriseaiframework | 1 | 201 | 0 | 201 |
| forge | 1 | 202 | 183 | 19 |
| galtrader | 1 | 757 | 648 | 109 |
| mainframe | 1 | 173 | 60 | 113 |
| mallcoppro | 1 | 1091 | 657 | 434 |
| nostrrelay | 1 | 52 | 10 | 42 |
| olmo3dl | 1 | 17 | 0 | 17 |
| os | 1 | 4 | 3 | 1 |
| pcjsvax | 1 | 154 | 0 | 154 |
| producer | 1 | 40 | 38 | 2 |
| proj | 1 | 72 | 68 | 4 |
| ready | 2 | 560 | 168 | 392 |
| resonant | 1 | 115 | 100 | 15 |
| vat | 1 | 292 | 12 | 280 |
| vat2 | 1 | 74 | 0 | 74 |
| wfa71fdb4e6242 | 1 | 2 | 0 | 2 |
| **total** | | **5104** | **3045** | **2059** |

**261 boards / 1,667 cards are OUT OF SCOPE.** They carry no CEK-bearing
grant — they were never confidential, their plaintext is intended, and
sealing them would make them unreadable to their own audience. The re-seal
does not touch them. Their plaintext stays world-readable, deliberately.

`proj` is a throwaway board created for this item's own rollback dry-walk,
not a project board. It is in scope because it is genuinely confidential, and
it is where both of the pass's only irregularities live (see below).

## What a reader loses

Today, **anyone** — a grantee, a passerby who found the relay, an archive, a
scraper — can read those 3,045 plaintext cards with no key and no grant. That
is the defect ready-336 exists to fix, and ending that universal readability
is the whole point of the operation. Once a card is re-sealed, only someone
holding the board's *current* CEK epoch can read it. This is a real
capability being removed, not a formality.

Two categories of reader are affected, and they are not the same:

**1. The general public / unaffiliated readers — the deliberate target.**
Anyone reading these boards today without a grant loses read access entirely,
permanently, by design. There is no one to notify individually here; cutting
this off is the fix ready-336 was raised for.

**2. Named grant-holders who do not hold the board's current CEK epoch.**
This is the category the item asked to derive from real grant data rather
than assume. The dry run derives it from `kind-39301` grants read off
`wss://relay.3dl.network` and reports it as `readers`.

**Result at 2026-08-05T15:52:53Z: ONE.**

| pubkey | board | consequence |
|---|---|---|
| `3032a516d23509f20e47147e2fc546e53bb1c3ec0fb59780a65f11fd4b0a4ca5` | `proj` | holds an older CEK epoch than `proj`'s current epoch; can read that board's plaintext tail today and **will not be able to read it after the pass** |

That reader must be told before `proj` is re-sealed, and needs a fresh
current-epoch grant if they are to keep access. **This is a loss, not a
formality** — after the pass, without a new grant, history they can read
today is closed to them and there is no path back (see the rollback runbook:
re-publishing the plaintext to restore access is never on the table).

An earlier revision of this document claimed this number was **zero** across
every board. It was not; it was derived from a one-off query rather than from
the dry run, and it was wrong. Do not carry any figure here forward without
re-running `scripts/resealplan`.

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

If you are already a grantee and the dry run shows you under `readers` by the
time the pass runs on your board, ask the owner for a fresh `rd grant`
**before that board's pass starts**, not after.

## When

Not yet executed. The pass is ready-5e7, blocked on:

- **ready-43d** — the dry run above. Done; it must be re-run and approved per
  board before any write touches that board.
- **ready-fcd** — `rd relay audit` learning to report a re-sealed coordinate
  as superseded, not missing, so the first post-pass audit does not cry wolf.
- **ready-e7a** — contributor-authored coordinates, which the owner cannot
  re-seal (the addressable coordinate includes the event author). The dry run
  reports `foreign = 0` on every confidential board: that class is currently
  empty, so it blocks nothing today.
- **ready-402** (this item) — this announcement + the rollback runbook.

Execution is **per board**, not one sweeping operation, so a problem on one
board does not touch the other twenty.

## What does NOT change

- Nothing is deleted from any project's local append-only log
  (`.ready/nostr-log.jsonl`). The original plaintext event stays there,
  forever, on the machine(s) that already hold it. What changes is what a
  relay serves to a fresh reader who was not already holding a copy.
- Already-sealed cards are untouched (2,059 of them). This pass only touches
  the grandfathered-plaintext coordinates.
- The 261 non-confidential boards are untouched.

## Two things the dry run currently reports, stated rather than buried

- **Nothing would halt the pass on size.** ready-c3e's 64 KiB relay limit was
  the stranding risk when the ruling was made; at this snapshot the largest
  projected sealed card is well under it and the dry run reports
  `WOULD HALT THE PASS: none`. This is a live figure — re-check it, do not
  inherit it.
- **26 references on `proj` would break.** They are inert per ready-c9d, and
  they are all on the throwaway dry-walk board. Every other board reports
  zero.
