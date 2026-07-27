# rd Board Fold Spec — event log → item projection → board columns

Status: **NORMATIVE**. Companion to `docs/design/confidential-boards-envelope.md`
(FROZEN), which specifies the *wire envelope* and deliberately leaves out the
*projection*. This document specifies the projection: which events fold, in what
order, into what item state, and which predicates form the board's columns
(**Part I**, §1–§15), and the inverse — which signed events every browser-writable
mutation must produce (**Part II**, §16–§27).

Scope note on the frozen doc: envelope §4 (cek epoch / CEK model) and §8
(encrypted-mode fold rules) are marked **"reference"** there. This document
**promotes them to normative** in §11. Nothing here modifies the frozen doc; where
this document and the frozen doc could be read as disagreeing, the frozen doc wins
on *wire format* and this document wins on *fold behaviour*.

## How to read a clause

Every clause is numbered `§N.M` and cites the Go function/branch that implements
it as `path:line`. Line numbers are against the tree at the commit that introduced
this file. A clause describes **what the code does today**, not what it should do.
Where the code's behaviour looks wrong, the clause still states the actual
behaviour and an entry is filed in §15 (Open questions) — no bug is silently
specced as intent, and no code was changed to make a clause tidier.

Two coverage guarantees this document must keep (the reviewer item enforces both
directions):

- **clause → code**: every clause carries a `file:line` citation.
- **code → clause**: every exported filter in `pkg/views/views.go` maps to at
  least one clause (§13); every derive branch in `pkg/state/state.go` maps to a
  clause or appears in §14 (Deliberately unspecified) with a reason.
- **mutation ↔ clause** (Part II): every mutation the board can perform maps to
  at least one clause, and every mutation body in `cmd/rd/nostrwrite.go` maps
  back to a clause — both directions are tabulated in §26, with no orphans.

---

## 1. Scope: there are two folds, and only one of them is live

**§1.1** The **live fold** — the one `rd ready`, `rd list`, `rd show` and every
write command read through — is `ProjectItems`, which replays the local
append-only signed-event log into `map[itemID]*state.Item`
(`pkg/sync/nostrproject.go:146`). An independent client MUST implement §2–§12
against this function. It is reached from the CLI via `nostrProjectAllItems`
(`cmd/rd/nostr.go:884`), which is the sole read spine on a nostr-native project.

**§1.2** `pkg/state.DeriveAll` (`pkg/state/state.go:405`) is the **campfire-era
fold**: it replays `work:*` convention messages from `[]msgrec.MessageRecord`. On
the shipped nostr path it is never called with a non-empty message slice. Its only
two live call sites pass `nil`, purely to obtain the built-in seed label registry
(`cmd/rd/list.go:204`, `cmd/rd/label.go:87`). Its message handlers are therefore
**not** part of the live fold; each is dispositioned in §14.

**§1.3** Both folds materialize the same value type, `*state.Item`
(`pkg/state/state.go:86-156`), and the same `HistoryEntry`
(`pkg/state/state.go:159-171`). `pkg/views` operates on that type only
(`pkg/views/views.go:25`) and so is shared by both. This is why §13's predicates
are normative for the nostr board even though `pkg/views` predates it.

**§1.4** Conformance target: given the same set of signed events, an independent
client MUST produce the same item map (field-for-field, history-for-history) and
the same view membership as §2–§13 describe. Ordering *within* a rendered list is
a CLI concern, not a fold concern (§13.13, §15.7).

---

## 2. Event kinds that fold

**§2.1 kind 30301 — board.** Addressable board = an rd project.
`KindBoard = 30301` (`pkg/sync/nostrwire.go:41`); built by `BuildBoardEvent`
(`pkg/sync/nostrwire.go:201`) with tags `d`=boardD, `title`, and one `p` per
maintainer. A board event carries **status-authority policy only**; it never
produces an item (`pkg/sync/nostrproject.go:253-260`).

**§2.2 kind 30302 — card.** Addressable card = an rd work item, the materialized
CURRENT state. `KindCard = 30302` (`pkg/sync/nostrwire.go:43`); built by
`BuildCardEvent` (`pkg/sync/nostrwire.go:246`). The clear-tag/sealed-field split is
the frozen envelope spec §1; the tag → field projection is §5 here.

**§2.3 kinds 1630 / 1631 / 1632 — NIP-34 status.** `KindStatusOpen = 1630`,
`KindStatusResolved = 1631`, `KindStatusClosed = 1632`
(`pkg/sync/nostrwire.go:48-52`). `KindStatusDraft = 1633`
(`pkg/sync/nostrwire.go:54`) is reserved and never written by rd, but IS accepted
by the fold because `isStatusKind` is a range test `1630 <= kind <= 1633`
(`pkg/sync/nostrwire.go:534-536`). The rd status → kind map is `statusKindFor`
(`pkg/sync/nostrwire.go:72-82`) and is **lossy**: the authoritative rd status is
read from the `status` tag, never from the kind (§6.5).

**§2.4 kind 1621 — NIP-34 issue.** `KindIssue = 1621`
(`pkg/sync/nostrwire.go:66`), published at most once per item
(`BuildIssueEvent`, `pkg/sync/nostrwire.go:477`; `FindIssueEventID`,
`pkg/sync/nostrwire.go:502`). It exists purely for generic-client interop and
**does not fold**: `itemIDForEvent` returns `""` for it
(`pkg/sync/nostrwire.go:585-602`), so the loop skips it at
`pkg/sync/nostrproject.go:261-264`.

**§2.5 kind 39301 — rd role grant.** `KindRoleGrant = 39301`
(`pkg/sync/rolegrant.go:45`), addressable per `(boardD, grantee)` slot via
`d = "<boardD>:<grantee>"` (`roleGrantD`, `pkg/sync/rolegrant.go:166`). It
produces no item; it feeds read-trust, status authority and confidential key
material (§11, §12).

**§2.6** No other kind participates. Any event whose kind is not 30301, 30302,
1630–1633 or 39301 is dropped by the `itemIDForEvent == ""` guard
(`pkg/sync/nostrproject.go:261-264`) or is simply never inspected.

---

## 3. Admission gates, in order

The main replay loop (`pkg/sync/nostrproject.go:207-296`) applies these gates in
exactly this sequence. **The order is normative** — an independent client that
reorders them will disagree on edge cases (e.g. a duplicate of an untrusted
event, or a board event that would fail the board-pin test).

Citation shorthand for this section only: a bare `:N` means
`pkg/sync/nostrproject.go:N`. Any other file is named in full.

**§3.1** A `nil` event is skipped (`:208-210`).

**§3.2 Dedup by event id.** The first occurrence of an event id is authoritative;
later copies are skipped (`seen`, `:206`, `:211-213`). Because the id is a content
hash, duplicates are byte-identical, so "first wins" is order-independent. Note
`seen[e.ID]` is only *set* for events that reach `:286` (or a board event at
`:254`) — an event dropped by §3.3–§3.9 is not recorded, so a later duplicate of
it re-runs the same gates and is dropped again, identically.

**§3.3 Signature.** `e.Verify()` must pass; a forged or tampered line is ignored
(`:214-216`).

**§3.4 Read-trust.** The author must satisfy `opts.trusts(e.PubKey)` OR
`grantTrusts(levels, e.PubKey)` (`:235-237`; `trusts` at `:121-126`, `grantTrusts`
at `:135-138`). `opts.Trusted == nil` disables the allowlist entirely (`:122-124`)
— production always passes a non-nil set (`cmd/rd/nostr.go:898` and
`cmd/rd/nostr.go:906-911`). `levels` is the grant-derived membership for the
pinned board (§12.8) and is empty when no board is pinned
(`pkg/sync/nostrproject.go:153-160`).

**§3.5 Point-in-time read-trust (prospective revocation).** If the author has a
bounded `until`, the event is dropped when `e.CreatedAt >= until[e.PubKey]`
(`pkg/sync/nostrproject.go:246-248`). A revoked key's **past** events survive; its
**future** events drop.
Non-revoked keys map to `authoritativeForever = MaxInt64`
(`pkg/sync/rolegrant.go:67`), so the comparison is inert for them.

**§3.6 Board branch.** A `KindBoard` event is recorded as the latest-wins board
for its coordinate `BoardCoord(e.PubKey, tagValue(e,"d"))` and the loop
`continue`s (`pkg/sync/nostrproject.go:253-260`). This runs BEFORE the item-id
guard because a board's `d` tag is a boardD, not an item id.

**§3.7 Item-id guard.** `itemIDForEvent(e)` must be non-empty
(`pkg/sync/nostrproject.go:261-264`). For a card that is the `d` tag; for a status event it is `d`, else
the third field of the first `a` coordinate (`pkg/sync/nostrwire.go:585-602`).

**§3.8 Board pinning (cards only).** When `opts.PinnedBoard != ""`, a `KindCard`
whose FIRST `a` tag is not exactly `PinnedBoard` is rejected
(`pkg/sync/nostrproject.go:271-273`). This
kills parallel-board self-escalation. Status events are NOT gated here — their
authority is already coordinate-bound (§6.1). Inert when no board is pinned.

**§3.9 Fail-closed fold gate (confidential boards).** A card or status event that
`shouldQuarantine` returns true for is skipped entirely
(`pkg/sync/nostrproject.go:283-285`). Full rule in §11.3–§11.4.

**§3.10 Classification.** Surviving events are marked seen
(`pkg/sync/nostrproject.go:286`) and routed: a card competes for
`winningCard[itemID]` under §4.1 (`pkg/sync/nostrproject.go:288-292`); a status
event is appended to `statusEvents[itemID]` in log order, to be sorted later
(`pkg/sync/nostrproject.go:293-295`).

**§3.11** An item exists in the output **iff** it has at least one surviving card
(`pkg/sync/nostrproject.go:324-325`). Status events for an item with no surviving card produce nothing —
they are neither an item nor an error.

---

## 4. Replay ordering and tiebreak

**§4.1 Card latest-wins.** Among surviving cards for an item, the winner is the
one for which `newerThan` holds against the incumbent: greater `created_at`; on a
`created_at` TIE, the **lexicographically LOWEST event id** wins
(`pkg/sync/nostrproject.go:552-557`, applied at `:289-292`). This matches NIP-01's
replaceable-event rule and strfry's own tie-break, so the relay's retained event
and the local winner agree.

**§4.2 Status chain ordering.** The authoritative status events for an item are
sorted by `(created_at ASC, event-id ASC)`
(`pkg/sync/nostrproject.go:371-377`). History is emitted in that order (§6.5) and
the LAST entry sets current status (§6.10).

**§4.3 No append-index dependence.** Neither §4.1 nor §4.2 may consult log-append
position, relay fetch order, or merge order. Both keys are pure functions of the
event set, which is what makes replay convergent across machines
(`pkg/sync/nostrproject.go:362-370` documents the prior divergent behaviour).

**§4.4 Grant ordering.** 39301 grants replay oldest-first under the same key:
`newerGrant` is `newerThan` on `(created_at, id)`
(`pkg/sync/rolegrant.go:568-573`), and the ascending sort is expressed as
`newerGrant(grants[j], grants[i])` (`pkg/sync/rolegrant.go:449-451`). Last
cap-valid grant applied per grantee wins (`:454-486`).

**§4.5 Board ordering.** Latest-wins per board coordinate under `newerThan`
(`pkg/sync/nostrproject.go:256-258`). Only the WINNING board's `p` tags name
maintainers (§6.1) — historical boards are NOT unioned, so republishing a board
without a `p` tag revokes that maintainer.

**§4.6 Time units.** Event `created_at` is unix **seconds** (NIP-01). `state.Item`
timestamps are unix **nanoseconds**: `itemFromCard` multiplies by
`int64(time.Second)` (`pkg/sync/nostrproject.go:564-566`), and `UpdatedAt` from a
status event does the same (`:424`). `HistoryEntry.Timestamp` is RFC3339 UTC at
second granularity (`:418`).

**§4.7 Write-side monotonic stamping (per causal chain).** A new event's
`created_at` is `max(now, newestInScope+1)` where scope is the event's causal
chain (`nostrNextCreatedAt`, `cmd/rd/nostr.go:222-241`). Scope keys come from
`DriftScope` (`pkg/sync/nostrwire.go:549-569`): `item:<id>` for a card / status /
issue, `grant:<boardD>:<grantee>` for a 39301, `board:<d>` for a 30301. Scoping the
bump to one chain bounds future-drift so an unrelated write burst cannot inflate a
card's `created_at` past a genuinely-later cross-machine edit. This is a **write**
rule; a folding client does not need it to read, but a conformance vector
generator does, to reproduce byte-identical event sets.

**§4.8 Conformance-vector JSON encoding of item timestamps (ready-414).**
`state.Item.CreatedAt`/`UpdatedAt` are declared as arbitrary int64 unix
nanoseconds. §4.6's derivation (`sec * int64(time.Second)`) happens to make
every value the live fold can currently produce exactly representable as an
IEEE-754 double: a multiple of `1e9` is always a multiple of `2^9`, and a
multiple of `2^9` round-trips through `float64` losslessly at any magnitude an
`int64` can hold. But the FIELD'S TYPE carries no such guarantee — it is
"arbitrary int64 unix nanoseconds," not "a whole number of seconds times
1e9" — and `testdata/fold.vectors.json` must be trustworthy for an independent
client for any value the type permits, not only the ones today's formula
happens to produce. JavaScript's `Number` cannot represent an arbitrary 64-bit
integer exactly (`Number.MAX_SAFE_INTEGER` = 2^53-1), so the **vector file**
(not `state.Item`'s Go JSON tags, which are unchanged, and not rd's own
CLI/wire output) encodes `expect.items[].created_at` / `.updated_at` as
DECIMAL STRINGS — e.g. `"created_at":"1700000000000000000"` — so a client
recovers them with `BigInt()`, never `Number()`. `internal/foldvectors.EncodeItem`
(`internal/foldvectors/vectors.go`) applies this at the one point every item is
turned into vector JSON, for both the hand-authored expectation and the
live-fold comparison, so authoring and verification can never silently
disagree about it. The vector file's own `timestamp_encoding` field restates
this for a reader who never opens the Go source. `FormatVersion` was bumped
1 → 2 for this change (a client on version 1 was silently trusting a bare
number that is only safe by accident).

---

## 5. Card → item field projection

**§5.1** `itemFromCard` (`pkg/sync/nostrproject.go:562-623`) maps the winning
card's tags and content onto `*state.Item`:

| Item field | Source | Cite |
|---|---|---|
| `ID` | `d` tag | `:563` |
| `MsgID` | the card's own **event id** | `:568` |
| `Title` | `title` tag (absent when confidential) | `:569` |
| `Status` | `s` tag | `:570` |
| `Priority` | `priority` tag, falling back to `rank` | `:571` |
| `Type` | `itype` tag | `:572` |
| `Context` / `Description` | `Content` (both set to the same value) | `:573-574` |
| `CreatedAt` / `UpdatedAt` | `created_at * 1e9` | `:575-576` |
| `BlockedBy` | **raw** `i` tags, unvalidated (staging; see §8.1) | `:579` |
| `Gate` | `gate` tag | `:580` |
| `WaitingType` | `waiting_type` tag | `:581` |
| `WaitingOn` | `waiting_on` tag (absent when confidential) | `:582` |
| `Labels` | all `l` tags, in tag order | `:583` |
| `ETA` | `eta` tag | `:584` |
| `Level` | `level` tag | `:588` |
| `For` | `for` tag | `:589` |
| `ParentID` | `parent` tag | `:590` |
| `Due` | `due` tag | `:591` |
| `By` | `p` tag, only when non-empty | `:593-595` |

**§5.2** A missing tag projects to the zero value — this is the backward-compat
rule for cards written before a tag existed (`pkg/sync/nostrproject.go:585-591`).

**§5.3** `CampfireID` is NEVER set by the nostr fold; it is `omitempty` precisely
so the shipped nostr JSON surface carries no `campfire_id`
(`pkg/state/state.go:96`).

**§5.4** `WaitingSince` is not a card tag. It is derived in the gate pass (§9.6).

**§5.5** `History` is NOT sourced from the card. The 30302 card is a latest-wins
projection with no history of its own; the append-only status chain IS the audit
trail (§6.5, `pkg/sync/nostrproject.go:346-354`).

**§5.6** The inverse mapping (item → card) is `CardSpecFromItem`
(`pkg/sync/nostrmigrate.go:106-127`) → `BuildCardEvent`
(`pkg/sync/nostrwire.go:246-353`). It is the single item→card source of truth, so
every republish carries the WHOLE item and cannot clobber a field by omission.
Note `CardSpec.Assignee` ← `item.By` (`pkg/sync/nostrmigrate.go:112`) and emits the
`p` tag (`pkg/sync/nostrwire.go:272-274`) — `p` is the **actor** (`By`), distinct from the `for`
tag (**scope**, `For`).

---

## 6. Status authority and history replay

**§6.1 Board-derived maintainers.** For each winning board coordinate, the
maintainer set is the board **author** plus every `p` tag on that winning board
(`pkg/sync/nostrproject.go:302-307`; `addBoardMaintainer` at `:177-187`). Keyed by
coordinate `30301:<author>:<boardD>` — deriving per-coordinate is what stops a
trusted key minting status authority for another author's item by publishing its
own board.

**§6.2 Grant-derived maintainers.** When a board is pinned, every key with derived
level `>= LevelMaintainer` (2) is ALSO a maintainer of the pinned coordinate
(`pkg/sync/nostrproject.go:316-322`). Revoked keys are deliberately NOT stripped
here: revocation is prospective and already enforced by §3.5; stripping would
erase past authority and reopen completed items (`:308-315`).

**§6.3 Explicit maintainers.** `opts.Maintainers` is unioned in per item
(`pkg/sync/nostrproject.go:342-344`). Production passes `nil`
(`cmd/rd/nostr.go:906-911`); it exists for tests and for event sets constructed
without a 30301 board (`pkg/sync/nostrproject.go:32-34`).

**§6.4 Authoritative filter.** A status event counts only if its author is the
item's card author OR a member of the item's maintainer set
(`pkg/sync/nostrproject.go:355-361`). A non-authoritative status event contributes
**neither state nor history** — it is excluded entirely. The item's maintainer set
is looked up by the winning card's FIRST `a` tag (`:336-341`).

**§6.5 History emission.** Every authoritative status event, in §4.2 order,
becomes one `HistoryEntry` with `Timestamp` (RFC3339 UTC), `FromStatus` =
`prevStatus` (initially `""`), `ToStatus`, `ChangedBy`, `Note`
(`pkg/sync/nostrproject.go:379-426`).

**§6.6 Missing status tag.** If a status event has no `status` tag, `ToStatus`
inherits `prevStatus` (`pkg/sync/nostrproject.go:381-384`) — the kind is NOT
consulted as a fallback.

**§6.7 `ChangedBy` and the `by` spoof guard.** `ChangedBy` defaults to the event's
signer. An rd-extension `by` tag overrides it ONLY when the signer is a board
maintainer (`pkg/sync/nostrproject.go:401-404`). A bare item author's `by` tag is
ignored, so a trusted-but-not-maintainer signer cannot attribute a transition to a
third party. Migrated campfire history relies on the maintainer-signed case.

**§6.8 `Note`.** Plaintext status events carry the close/change reason in
`Content` verbatim. A confidential status event carries sealed ciphertext:
a granted reader gets the decrypted reason, everyone else gets `placeholderText`
(`pkg/sync/nostrproject.go:405-416`; §11.8).

**§6.9 `UpdatedAt`.** Advanced to `max(current, s.CreatedAt * 1e9)` per
authoritative status event (`pkg/sync/nostrproject.go:424`). It is initialized
from the winning card (§5.1), so an item with no status events keeps the card's
timestamp.

**§6.10 Current status.** When at least one authoritative status event exists, the
item's `Status` is the LAST one's `ToStatus`, overriding the card's `s` tag
(`pkg/sync/nostrproject.go:427-432`).

**§6.11** With zero authoritative status events, the card's `s` tag stands as
current status (§5.1) and `History` is empty.

---

## 7. The status lattice

**§7.1 Values.** `inbox`, `active`, `scheduled`, `waiting`, `blocked` (non-terminal)
and `done`, `cancelled`, `failed` (terminal) — `pkg/state/state.go:28-37`.

**§7.2 Terminal set.** `TerminalStatuses = {done, cancelled, failed}`
(`pkg/state/state.go:78-82`); tested via `IsTerminal`
(`pkg/state/state.go:1041-1043`). `IsBlocked` is `Status == "blocked"`
(`pkg/state/state.go:1036-1038`).

**§7.3 Kind mapping.** `done → 1631`; `cancelled`, `failed → 1632`; everything
else (including any unknown string) `→ 1630`
(`statusKindFor`, `pkg/sync/nostrwire.go:72-82`). The mapping is lossy by design —
the exact status rides the `status` tag (§2.3).

**§7.4 Initial status.** A newly created item is built with
`Status: state.StatusInbox` (`cmd/rd/nostrwrite.go:541`, and `:618` on the
playbook-engage path) and published as card + a 1630 status event by
`publishItemFullCreateNostr` (`cmd/rd/nostrwrite.go:155`, called at `:547` and
`:622`).

**§7.5 Close resolutions.** `rd done/fail/cancel` map through
`closeResolutionToStatus` (`cmd/rd/nostrwrite.go:243`); a close is refused when the
item is already terminal (`:239-241`).

**§7.6 `blocked` is DERIVED, not authored.** The fold recomputes it in §8.4 on
every replay. A card MAY carry `s=blocked` (because `CardSpecFromItem` copies
`item.Status` verbatim, `pkg/sync/nostrmigrate.go:110`), and such a value is
accepted at §5.1 — but it is then overwritten by §8 for items whose blockers are
terminal, and there is no path that *keeps* an item blocked without a live
non-terminal blocker edge.

**§7.7 `waiting` is partly derived.** It is authored by `rd gate`
(`cmd/rd/nostrwrite.go:281-284`) and also PROMOTED at fold time from card-declared
gate tags (§9.4).

**§7.8 `scheduled` is defined but never authored.** No write path in `cmd/rd`
produces `status=scheduled`; `rd defer` is a card-only ETA edit
(`cmd/rd/aliases.go:124-161`, routing to `runUpdateNostr`). The only live
references to `StatusScheduled` are the two view predicates
(`pkg/views/views.go:68`, `:86`). See §15.1.

---

## 8. Dependency edge derivation

**§8.1 Staging.** `itemFromCard` puts the card's raw `i` tags into `BlockedBy`
unvalidated (`pkg/sync/nostrproject.go:579`). `applyDepAndGateStatus` then drains
that field into an edge list and CLEARS it, rebuilding it from validated edges
only (`pkg/sync/nostrproject.go:453-460`). So `BlockedBy` on the returned item is
never the raw tag set.

**§8.2 Unresolvable edges are dropped silently.** An edge whose blocker or blocked
id is not present in this projection is skipped
(`pkg/sync/nostrproject.go:462-465`) — no warning, no field, no error.

**§8.3 Terminal blocked items are skipped.** An edge whose *blocked* item is
terminal contributes nothing at all — not even a `BlockedBy` entry
(`pkg/sync/nostrproject.go:467-469`).

**§8.4 Blocked status.** For a surviving edge, if the BLOCKER is non-terminal the
blocked item's status is set to `blocked`
(`pkg/sync/nostrproject.go:470-472`). This overwrites whatever §6.10 decided.

**§8.5 Edge fields.** For every surviving edge (regardless of the blocker's
terminal state) `blocked.BlockedBy += blockerID` and `blocker.Blocks += blockedID`
(`pkg/sync/nostrproject.go:473-474`), deduped by `appendUniqueStr`
(`:531-538`). So `BlockedBy` records the *dependency*, not only *active* blockers
— matching `pkg/state/state.go:992-993`.

**§8.6 No cycle detection.** A dependency cycle is not detected, rejected, or
reported at fold time. Each member of a cycle simply blocks the others
(`pkg/sync/nostrproject.go:461-475` has no visited set).

**§8.7 Implicit unblock is a WRITE rule, not a fold rule.** On close, rd
re-publishes the cards of every item this item was blocking
(`publishImplicitUnblockNostrNative`, `cmd/rd/nostrwrite.go:635`, called from
`runCloseNostr`, `:247`). The fold itself needs no such step: §8.4 already ignores
terminal blockers on the next replay.

**§8.8 Dep writes.** `rd dep add` appends the blocker id to the blocked item's dep
set and re-publishes the card only (`runDepAddNostr`,
`cmd/rd/nostrwrite.go:350-368`); `rd dep remove` strips it
(`:373`). Blocked status is never written directly.

**§8.9 Cross-board deps.** On the nostr path a cross-board reference is REFUSED at
write time with an error (`runDepAddNostr`, `cmd/rd/nostrwrite.go:351-353`, using
`state.IsCrossCampfireRef`, `pkg/state/state.go:1050-1059`). Should one reach the
fold anyway (a hand-written `i` tag), §8.2 drops it silently: it is non-blocking,
but **no warning is produced** — `Item.CrossCampfireWarnings`
(`pkg/state/state.go:155`) is never populated by the nostr fold. The
"non-blocking WITH warnings" behaviour exists only in the campfire fold
(`pkg/state/state.go:862-881`). See §15.2.

---

## 9. Gate open → resolve, and how `Gate` / `GateMsgID` clear

**§9.1 Open.** `rd gate` sets `Status=waiting`, `Gate=<type>`,
`WaitingType="gate"`, `WaitingOn=<description>` and publishes a status change; the
description doubles as the status-event reason
(`runGateNostr`, `cmd/rd/nostrwrite.go:273-296`). Terminal items are refused
(`:278-280`). It then re-resolves the item to report the projection-derived
`GateMsgID` (`:288-293`) — i.e. even the writer learns the gate id from the fold.

**§9.2 Approve.** `rd approve` requires a pending gate (`GateMsgID != ""` OR
`Gate != ""` OR `WaitingType == "gate"`) and `Status == waiting`
(`runApproveNostr`, `cmd/rd/nostrwrite.go:304-309`). It sets `Status=active` and
CLEARS `Gate`, `WaitingType`, `WaitingOn`, `WaitingSince`, `GateMsgID`
(`:310-315`), then publishes. Because the republished card omits the gate tags,
§9.4's promotion cannot re-gate the item.

**§9.3 Reject.** `rd reject` publishes a status event that RE-AFFIRMS `waiting`
with the rejection reason, changing no field
(`runRejectNostr`, `cmd/rd/nostrwrite.go:327-345`). The gate stays open and the
ruling is preserved in history.

**§9.4 Card-declared gate promotion.** Define
`declaresGate := WaitingType != "" || WaitingOn != "" || Gate != ""`
(`pkg/sync/nostrproject.go:490`). A non-blocked, non-terminal item that
`declaresGate` is promoted to `Status=waiting`
(`:491-493`). This exists because a gate can be CURRENT state without ever having
been a status transition (migrated campfire items), and blocking is checked FIRST
so it supersedes.

**§9.5 Terminal clears everything.** A terminal item has `WaitingOn`,
`WaitingType`, `WaitingSince`, `GateMsgID` cleared unconditionally
(`pkg/sync/nostrproject.go:494-500`). Note `Gate` itself is NOT cleared here.

**§9.6 Gate field derivation (non-terminal, `declaresGate`).** `WaitingSince`, if
empty, is derived from `UpdatedAt` as RFC3339 UTC
(`pkg/sync/nostrproject.go:512-514`). `GateMsgID` is set to `item.MsgID` — the
**winning card's event id** (§5.1) — if and only if `WaitingType == "gate"`;
otherwise it is cleared (`:515-519`). There is no separate "gate event"; the gate
identity IS the card identity, which is why the id changes on every card
republish.

**§9.7 Gate fields persist under blocking.** When an item both `declaresGate` and
is blocked, §8.4 wins on STATUS (`blocked`) but the gate fields are retained by
`:501-519` — the pending gate is still real. This is the documented parity fix
with `pkg/state.applyBlockStatus`, which likewise never clears them.

**§9.8 No declared gate.** All four fields are cleared
(`pkg/sync/nostrproject.go:520-526`).

**§9.9 Ordering.** §9.4–§9.8 run inside `applyDepAndGateStatus` AFTER the dep pass
(`pkg/sync/nostrproject.go:452`, dep loop `:461-475`, gate loop `:477-527`), and
`applyDepAndGateStatus` itself runs after the whole per-item status pass
(`:435`). An independent client MUST use this ordering: gate promotion reads the
blocked status the dep pass just wrote.

---

## 10. Labels

**§10.1 Nostr labels are FREEFORM.** `Item.Labels` is every `l` tag on the winning
card, in tag order, with **no pattern check and no registry check**
(`pkg/sync/nostrproject.go:583`). The nostr projection has no per-project label
registry; this is stated in the code at `cmd/rd/list.go:199-202` and
`cmd/rd/label.go:84-86`.

**§10.2 No `LabelWarnings` on the nostr fold.** `Item.LabelWarnings`
(`pkg/state/state.go:150`) is never populated by `ProjectItems`. The
drop-with-warning behaviour is campfire-only (`pkg/state/state.go:594-612`,
`:650-659`). See §15.3.

**§10.3 Confidential labels.** On a confidential board with an LTK, the clear `l`
tag value is `hex(HMAC-SHA256(LTK, label))`
(`labelToken`, `pkg/sync/envelope.go:284-288`; emitted at
`pkg/sync/nostrwire.go:296-301`). With NO LTK, a confidential card emits **no** `l`
tag at all rather than leaking a plaintext label
(`pkg/sync/nostrwire.go:302-306`). A granted reader replaces `Item.Labels` with
the plaintext labels from the sealed blob when the blob decrypts AND is non-empty
(`pkg/sync/nostrproject.go:609-611`); a non-member keeps the opaque tokens
(`:617-618`, comment).

**§10.4 Registry is seed-only and advisory.** `state.DeriveAll("", nil)` yields
the built-in seed atoms (`declarations.LoadSeedLabels`,
`pkg/state/state.go:471-479`). `rd label list` renders them
(`cmd/rd/label.go:87`), and `printUnknownLabelHints` uses them for a stderr hint
when a `--label` filter returns nothing (`cmd/rd/list.go:203-214`). Neither path
affects the fold or the filter result.

**§10.5 Query is client-side.** `rd list --label` / `rd ready --label` apply
`views.LabelFilter` over the PROJECTED labels with AND semantics
(`cmd/rd/list.go:93`, `cmd/rd/ready.go:122`). No `#l` filter is pushed to a relay,
which is why tokenization needs no relay-side cooperation (frozen envelope §7).

---

## 11. Confidential envelope — fold rules (promotes frozen §4 and §8 to normative)

**§11.1 Marker tags.** A confidential card or status event carries exactly two new
clear tags: `["enc","1"]` and `["cek_epoch","<int>"]`
(`encMarkerTags`, `pkg/sync/envelope.go:318-323`; constants at `:191-200`).
`isConfidential(e)` is `tagValue(e,"enc") != ""` — ANY version
(`pkg/sync/envelope.go:136-138`).

**§11.2 Well-formedness (structural, key-free).** `encWellFormed`
(`pkg/sync/envelope.go:73-85`) requires ALL of: `enc` tag exactly `"1"` (absent or
unknown version → malformed); `cek_epoch` parses as an int; `Content`
base64-StdEncoding-decodes; decoded length `>= 12 + 16` (nonce + Poly1305 tag).
It deliberately does NOT verify the AEAD — the fold runs without the CEK.

**§11.3 Quarantine (fail-closed).** `shouldQuarantine`
(`pkg/sync/envelope.go:100-116`): if `EncryptedBoards` is nil → false (gate
inert). If `Cutover(boardCoordOf(e))` reports the board is plaintext → false. If
`encWellFormed(e)` → false. Otherwise → **true**: the event is dropped at
§3.9 and can never fold its cleartext into state or history. This covers a
post-cutover plaintext card, a v-shaped card with a malformed/short/unknown
envelope, and a plaintext status event trying to fold a cleartext close reason.

**§11.4 Grandfathering.** The single exception: `e.CreatedAt < cutover AND
!isConfidential(e)` → false (kept) (`pkg/sync/envelope.go:111-114`). This applies
ONLY in the replay loop, which is the full-log fold — never in the live ingest
path. The accepted limit (an admitted member can backdate below the cutover) is
frozen-spec §8 and is unchanged.

**§11.5 Board coordinate of an event.** `boardCoordOf` scans for the `a` tag whose
value starts with `"30301:"` (`pkg/sync/envelope.go:123-131`). This works for both
shapes: a card's sole `a` tag IS the board coordinate
(`cardBoardCoord`, `pkg/sync/nostrwire.go:232-241`), while a status event carries
the board coordinate as a SECOND `a` tag after the card coordinate
(`BuildStatusEventWithIssueRoot`, `pkg/sync/nostrwire.go:443-446`).

**§11.6 CEK resolution.** `cekFor` (`pkg/sync/envelope.go:144-153`) returns
`ok=false` unless a decryptor is present, `enc` is exactly `"1"`, `cek_epoch`
parses, AND the decryptor holds a key for `(boardCoord, epoch)`. Every negative
path is a silent fail-closed, never an error surfaced to the user.

**§11.7 Card placeholder rule.** When `isConfidential(card)`
(`pkg/sync/nostrproject.go:603`): on successful decrypt, `Title`, `Context`,
`Description`, `WaitingOn` come from the sealed `cardPayload`
(`pkg/sync/envelope.go:223-228`), and `Labels` are replaced only if the sealed
list is non-empty (`pkg/sync/nostrproject.go:604-611`). On failure: `Title`,
`Context`, `Description` become `placeholderText` = `"[encrypted]"`
(`pkg/sync/envelope.go:39`) and `WaitingOn` becomes `""` — hidden rather than
shown as a placeholder, because the clear `waiting_type` still renders
(`pkg/sync/nostrproject.go:612-620`). **Every clear routing field (§5.1) renders
normally regardless.** The read path never surfaces raw ciphertext, never panics,
never exits non-zero.

**§11.8 Status placeholder rule.** A confidential status event's `Note` is the
decrypted `{"reason": ...}` on success, else `placeholderText`
(`pkg/sync/nostrproject.go:409-416`; `decryptStatusReason`,
`pkg/sync/envelope.go:175-189`). The rest of the history entry (who / when /
from→to) renders regardless.

**§11.9 Content wire format.** `base64Std(nonce(12) ‖ ChaCha20-Poly1305(CEK, nonce,
plaintext))`, fresh `crypto/rand` nonce per event
(`sealContent`, `pkg/sync/envelope.go:239-253`; `openContent`, `:259-278`). This
restates frozen §3 and MUST NOT drift from it.

**§11.10 Epoch model.** Epochs are integers `>= 1`. Bootstrap mints epoch 1
(`boardConfidentialEnvelope`, `cmd/rd/confidential.go:138-155`) via an
owner-signed self-grant (`publishOwnerCEKSelfGrant`,
`cmd/rd/confidential.go:243-266`) and wraps it to existing members
(`wrapEpochToMembers`, `:194`). A grant whose `cek_epoch < 1` is rejected outright
by keyring derivation (`pkg/sync/keydist.go:164-170`) — it contributes neither a
key nor a cutover.

**§11.11 Rotation on revoke.** `rekeyBoardOnRevoke`
(`cmd/rd/confidential.go:324-353`) mints a fresh CEK at `curEpoch + 1`
(`:342-343`), self-grants it, and re-wraps it — with the **stable LTK** — to every
remaining read-trusted member EXCLUDING the revoked key (`:351-353`). Cards
authored after the revoke are unreadable by that key (forward secrecy); historical
cards stay readable by it (accepted limit, frozen §4). No-op on a plaintext board
or when the signer is not the owner (`:325-327`).

**§11.12 Keyring derivation retains ALL epochs.** `DeriveBoardKeyring`
(`pkg/sync/keydist.go:141-194`) scans EVERY historical grant, not latest-wins, so
a member keeps old-epoch CEKs and historical reads survive (`:136-140`). It
accepts a key only when: kind is 39301 and `Verify()` passes (`:146-151`); the
grant binds to `(boardAuthor, boardD)` (`:156-158`); the grant is signed by the
**board author** — only the owner mints CEKs (`:161-163`); the epoch is `>= 1`
(`:168-170`); the grant's `p` tag names the reader (`:178-180`); AND the NIP-44
wrap actually opens for the reader's key (`:181-186`). The last two together are
the anti-retarget guard.

**§11.13 Cutover derivation.** The board-global cutover is the EARLIEST
`created_at` of any owner-signed CEK-bearing grant, tracked regardless of who it
is addressed to (`pkg/sync/keydist.go:172-175`). `Cutover(coord)` returning
`ok=true` is exactly "this board is confidential"
(`pkg/sync/keydist.go:97-103`).

**§11.14 Current epoch for writes.** `CurrentEpoch` returns the HIGHEST epoch the
reader holds (`pkg/sync/keydist.go:110-124`). A member that missed a rotation
returns a stale epoch; the owner always holds the true current one
(`:105-109`).

**§11.15 Nil-keyring safety.** `boardReadKeyring` may return a nil
`*BoardKeyring` (`cmd/rd/confidential.go:272-275`), which becomes a NON-nil
interface value in `ProjectOptions.{Decryptor,EncryptedBoards}`. Every
`BoardKeyring` method therefore nil-checks its receiver
(`pkg/sync/keydist.go:72-74`, `:87-89`, `:98-100`, `:111-113`), so a nil keyring
behaves as "no keys, all boards plaintext" instead of panicking. An independent
client MUST reproduce the *behaviour* (inert gate), not the Go-specific mechanism.

---

## 12. Role grants (39301): read-trust, levels, until

**§12.1 Parse.** `parseRoleGrant` (`pkg/sync/rolegrant.go:204-251`) requires kind
39301, a non-empty `p` (grantee), a `role` in
`{owner, maintainer, contributor, revoked}` (`:213-217`), and a well-formed
`a` = `30301:<owner>:<d>` (`:218-221`, `parseBoardCoord` at `:263-276`). A `from`
tag must parse as a non-negative int or the whole grant is rejected (`:222-229`).
An unparseable `cek_epoch` coerces to 0 (`:230-235`) — which §11.10 then rejects.

**§12.2 Full-coordinate binding.** Only grants whose `a` names BOTH
`owner == boardAuthor` AND `d == boardD` are replayed
(`pkg/sync/rolegrant.go:441-443`). An empty `boardD` matches no grant
(fail-closed, never every board).

**§12.3 Level mapping.** `owner`/`maintainer` → 2, `contributor` → 1, `revoked` →
0, unknown → 1 (`roleToLevel`, `pkg/sync/rolegrant.go:307-319`). Keys ABSENT from
the map are level 1 by caller convention, NOT level 0
(`pkg/sync/rolegrant.go:352-355`).

**§12.4 Bootstrap.** The board author is seeded at level 2 with
`until = authoritativeForever` (`pkg/sync/rolegrant.go:415-419`). This is what
makes grant-derived trust non-circular: owner-signed grants are always admitted.

**§12.5 Escalation cap.** `signerMayGrant` (`pkg/sync/rolegrant.go:525-549`):
only the board author may grant `maintainer`/`owner` (`:527-529`); the owner may
grant `contributor`/`revoked` to anyone (`:531-533`); a non-owner signer must
itself be level `>= 2` (`:534-536`), may never target the board author
(**owner lockout**, `:537-540`), and may never target a current maintainer
(**peer protection**, `:541-544`). Any other signer grants nothing (`:546-548`).
A cap-violating grant is IGNORED, evaluated against state replayed so far
(`:457-459`).

**§12.6 Single-use claim binding.** A grant carrying a `claim` tag AND signed by
the board author binds that nonce to exactly one grantee, first-cap-valid-wins;
a later owner grant reusing the same claim for a DIFFERENT grantee is skipped
(`pkg/sync/rolegrant.go:478-483`). A `claim` on a non-owner grant is inert — the
grant still applies as an ordinary contributor grant.

**§12.7 `until` derivation.** For each grantee's winning grant: if `role ==
revoked`, `until = from` when `from > 0`, else the grant's `created_at`; otherwise
`until = authoritativeForever` (`pkg/sync/rolegrant.go:489-499`). This is the
value §3.5 gates on.

**§12.8 Read-trust set.** `DeriveReadTrust` (`pkg/sync/rolegrant.go:394-401`) is
the key set of the level map — board author plus every cap-valid grantee,
**including revoked ones** (level 0), so their past events stay admissible. A key
that never received an owner-rooted grant is absent (fail-closed).

**§12.9 One replay, three consumers.** `deriveGrants`
(`pkg/sync/rolegrant.go:410-502`) is the single replay behind `DeriveLevels`
(`:356`), `DeriveReadTrust` (`:394`), `ClaimGrantee` (`:367`) and
`DeriveAllowlist` (`pkg/sync/allowlist.go:44`) — so the graded read-trust set and
the coarse relay allowlist cannot drift.

---

## 13. View predicates — the board's columns (normative)

These predicates ARE the board's columns; an independent client that renders
different membership has diverged even if its item state is identical.

**§13.1 `Filter`** is `func(*state.Item) bool` (`pkg/views/views.go:25`).
**`Apply`** returns the input slice filtered, preserving input order, and returns
a nil slice when nothing matches (`pkg/views/views.go:214-222`).

**§13.2 `Named(viewName, identity)`** dispatches the eight view-name constants —
`ViewReady`, `ViewWork`, `ViewPending`, `ViewOverdue`, `ViewDelegated`,
`ViewMyWork`, `ViewGates`, `ViewFocus` (`pkg/views/views.go:13-22`) — to their
filters, returning nil for an unknown name
(`:30-51`). Note it wires `ViewFocus` to `FocusFilter("")` — the un-parameterized
form (`:47`), so `rd focus <type>` cannot go through `Named` (it calls
`views.FocusFilter(gateType)` directly, `cmd/rd/focus.go:31`).
**`AllNames()`** enumerates the eight view names (`pkg/views/views.go:225-236`).

**§13.3 `ReadyFilter()`** — the default column. An item is READY iff: NOT terminal
(`pkg/views/views.go:61-63`), NOT blocked (`:64-66`), and status is not
`scheduled` (`:67-69`). **ETA is explicitly NOT a filter** — it sorts, it does not
exclude (`:53-59`). Consumed by `rd ready` (`cmd/rd/ready.go:90-95`) and by
`rd status`'s actionable count (`cmd/rd/status.go:187`).

**§13.4 `WorkFilter()`** — `Status == active`, nothing else
(`pkg/views/views.go:76-80`). Consumed by `rd work` when no `--for` is given
(`cmd/rd/work.go:29`).

**§13.5 `PendingFilter()`** — `Status ∈ {waiting, scheduled, blocked}`
(`pkg/views/views.go:83-91`). Consumed by `rd pending` (`cmd/rd/pending.go:25`).
Terminal items cannot match, since none of those three are terminal.

**§13.6 `OverdueFilter()`** — NOT terminal, `ETA` non-empty, `ETA` parses as
RFC3339, and `ETA < now` (`pkg/views/views.go:94-109`). An unparseable ETA is
excluded, never treated as overdue (`:103-106`). **`now` is captured when the
filter is CONSTRUCTED, not when it is applied** (`:95`) — see §15.6.

**§13.7 `DelegatedFilter(identity)` / `DelegatedFilterSet(idset)`.** The set form
is primary (`pkg/views/views.go:130-140`): match iff `idset[For]` AND `By != ""`
AND `!idset[By]` AND `Status == active`. An empty set never matches (`:131-133`).
`DelegatedFilter` is the single-identity wrapper via `identitySet`
(`:113-115`). The set form exists so a person's aliased keys count as ONE party —
work `By` another key of the SAME party is self-work, not a delegation.

**§13.8 `MyWorkFilter(identity)` / `MyWorkFilterSet(idset)`.** Match iff
`idset[By]` AND NOT terminal (`pkg/views/views.go:151-158`); empty set never
matches. `MyWorkFilter` is the single-identity wrapper (`:143-145`). Note this
keys on `By` (the actor), NOT `For`.

**§13.9 `identitySet(identity)`** returns nil for `""` and a one-element set
otherwise (`pkg/views/views.go:164-169`) — this is what makes "empty identity
excludes everything" hold on the raw-string path.

**§13.10 `GatesFilter()`** — `Status == waiting` AND `WaitingType == "gate"` AND
`GateMsgID != ""` (`pkg/views/views.go:175-181`). All three conjuncts matter; §9.6
is what makes the third one true on the nostr path. Consumed by `rd gates`
(`cmd/rd/gates.go:32`).

**§13.11 `FocusFilter(gateType)`** — `ReadyFilter` AND (`gateType == ""` OR
`item.Gate == gateType`) (`pkg/views/views.go:185-196`). Note it filters on
`Gate`, the escalation CATEGORY, on items that are READY — so it does not overlap
`GatesFilter`, whose items are `waiting` and therefore not ready. Consumed by
`rd focus` (`cmd/rd/focus.go:31`).

**§13.12 `LabelFilter(atom)`** — exact match of `atom` against a member of
`Item.Labels`; no substring, no glob (`pkg/views/views.go:202-211`). Multiple
atoms are AND-composed by the CALLER, one `Apply` per atom
(`cmd/rd/ready.go:120-123`, `cmd/rd/list.go:90-94`).

**§13.13 Composition at the CLI (informative, but load-bearing for parity).**
`rd ready` applies, in order: the view filter (`cmd/rd/ready.go:95`); for
non-identity views, a party-scope filter `idset[For] || idset[By]` when `--for` is
non-empty (`:104-115`); a project filter (`:117`); then one `LabelFilter` per
`--label` (`:120-123`). `rd ready --for ""` disables the party scope entirely.

**§13.14 List order is NOT part of the fold.** `ProjectItems` returns a map; the
CLI materializes a slice in Go map-iteration order
(`cmd/rd/nostr.go:912-916`) and then sorts — by priority then ETA for
ready/work/pending/focus/gates (`sortByPriorityETA`, `cmd/rd/ready.go:218-227`),
by priority then ID for `rd list` (`cmd/rd/list.go:103-110`). Only the latter is
a total order. See §15.7.

---

## 14. Deliberately unspecified (and why)

Each entry is a derive branch that exists in the tree but is NOT part of the live
nostr fold. Nothing here was changed; each is listed so a code→clause reviewer
finds no orphan.

**§14.1 The entire `work:*` message-handler family**, all in
`pkg/state/state.go` — `handleWorkCreate` (`pkg/state/state.go:556`),
`handleWorkStatus` (`:692`), `handleWorkClaim` (`:724`), `handleWorkDelegate`
(`:747`), `handleWorkClose` (`:763`), `handleWorkUpdate` (`:806`),
`handleWorkBlock` (`:854`), `handleWorkUnblock` (`:891`), `handleWorkGate`
(`:916`), `handleWorkGateResolve` (`:941`), `handleWorkLabelAdd` (`:633`),
`handleWorkLabelRemove` (`:665`) — plus the dispatcher `DeriveAll`
(`:405-453`) and the deprecated `Derive` (`:399-401`).
**Reason:** campfire-era fold. Live call sites pass `nil` messages (§1.2), so no
handler ever executes in production. Specifying them would spec a dead path and
invite an independent client to implement the wrong fold. The nostr equivalents
are: create/update → §5 (latest-wins card), status/claim/delegate/close → §6,
block/unblock → §8, gate/gate-resolve → §9, label-add/remove → §10.

**§14.2 `buildRoleMap` (`pkg/state/state.go:511-552`), its `roleInfo` value type
(`:315-320`), and `applyStrandedItemReclaim` (`:1000-1012`).** The campfire
`work:role-grant`
replay and the "revoked member's active items flip back to inbox" rule.
**Reason:** the nostr authority model is 39301 grants (§12) and revocation there
is PROSPECTIVE — a revoked key's past events stay authoritative and completed work
does not reopen (§3.5, §6.2). Stranded-item reclaim is a different, incompatible
policy that the nostr fold deliberately does not implement.

**§14.3 `buildLabelRegistry` (`pkg/state/state.go:467-507`), its `LabelDef` entry
type (`:42-47`), and `labelAtomPattern` (`:25`).** **Reason:** the registry
survives only as the
seed-atom source (§10.4); its `work:label-define` overlay
(`:481-504`) has no nostr counterpart, and the atom pattern is not enforced by the
nostr fold (§10.1). Filed as a divergence in §15.3, not as intended nostr
behaviour.

**§14.4 `applyBlockStatus` (`pkg/state/state.go:979-995`).** **Reason:** campfire
counterpart of §8; `applyDepAndGateStatus` is documented as mirroring it exactly
(`pkg/sync/nostrproject.go:439-451`). The nostr rule is normative in §8; this one
is not.

**§14.5 `etaFromPriority` (`pkg/state/state.go:283-296`).** Default-ETA-from-
priority (p0=now, p1=+4h, p2=+24h, p3=+72h). **Reason:** applied only inside
`handleWorkCreate` (`:565-568`). The nostr create path publishes whatever ETA the
item carries; no default is synthesized at fold time. An `eta` tag absent from a
card projects to `""` (§5.2), and `OverdueFilter` excludes empty ETA (§13.6).

**§14.6 Message-decoding scaffolding for §14.1's dead handlers.**
`resolveItemID` (`pkg/state/state.go:300-312`), `clearOrSet` / `ClearSentinel`
(`:1014-1023`), `appendUnique` (`:1026-1033`), `parseTimestamp` /
`parseTimestampValue` (`:324-354`), `hasTag` (`:272-279`), the replay scratch
types `replayState` (`:357-372`), `blockEdge` (`:375-379`) and `blockEdgeKey`
(`:382-385`), and every convention payload struct — `createPayload` (`:174-191`),
`statusPayload` (`:194-200`), `claimPayload` (`:203-206`), `delegatePayload`
(`:209-214`), `closePayload` (`:217-221`), `updatePayload` (`:224-241`),
`blockPayload` (`:244-249`), `unblockPayload` (`:252-255`), `gatePayload`
(`:258-262`), `gateResolvePayload` (`:265-269`), `labelDefinePayload`
(`:456-459`), `labelMutPayload` (`:625-628`). **Reason:** all decode `work:*`
campfire messages. The nostr fold resolves items by the `d` tag / `a` coordinate
instead (`itemIDForEvent`, `pkg/sync/nostrwire.go:585-602`) and its dedup helper
is `appendUniqueStr` (§8.5). Note `state.statusPayload` is unrelated to
`sync.statusPayload` (§11.8), which is the sealed `{"reason": ...}` blob.

**§14.7 `DeriveResult` and its accessors (`pkg/state/state.go:51-75`), including
`Warnings()`.** **Reason:** only `LabelRegistry()` is reached live (§10.4);
`Items()` and `Warnings()` are never consumed on the nostr path — the fold returns
a bare map and produces no warning channel.

**§14.8 `ParseCrossCampfireRef` / `CrossCampfireRef`
(`pkg/state/state.go:1061-1083`).** **Reason:** only `IsCrossCampfireRef`
(`:1050-1059`) is used live, as a write-time rejection predicate (§8.9). Nothing
parses a cross-board ref on the nostr path.

**§14.9 `Item.CampfireID` (`pkg/state/state.go:96`) and
`Item.CrossCampfireWarnings` (`:155`).** **Reason:** never written by the nostr
fold (§5.3, §8.9). Both are `omitempty` so they do not appear in the nostr JSON
surface.

**§14.10 Kind 1633 (`KindStatusDraft`).** **Reason:** never written by rd
(`pkg/sync/nostrwire.go:53-54`), though accepted by `isStatusKind` (§2.3). Its fold
behaviour would be identical to any other status kind (§6) — the kind is not
consulted for status (§6.6) — so there is nothing distinct to specify. Flagged in
§15.4 as a conformance-vector concern.

**§14.11 `pkg/sync` machinery outside the fold** — relay transport
(`nostrinbound.go`, `nostroutbound.go`, `negentropy.go`, `negsync.go`,
`nostrpending.go`, `relayclass.go`, `nostrfilter.go`, `boarddiscovery.go`,
`inviteconsume.go`) and the campfire→nostr migration (`nostrmigrate.go`, except
`CardSpecFromItem`, §5.6). **Reason:** they determine WHICH events reach the local
log, not how a given event set folds. The fold is a pure function of the event set
(§4.3), so an independent client with the same events is conformant regardless of
how it obtained them.

---

## 15. Open questions — where this spec and the code disagree

These are RECORDED, not resolved, and no code was changed for them. Each needs a
ruling before the conformance vector suite (epic ready-9f5) can assert on the
affected behaviour. This section holds the READ-side questions; the write-side
questions are in §27.

**§15.1 `scheduled` is a status no writer produces.** §7.8. `ReadyFilter` excludes
it (`pkg/views/views.go:67-69`) and `PendingFilter` includes it (`:86`), but
nothing in `cmd/rd` ever sets `status=scheduled` — `rd defer` edits ETA only
(`cmd/rd/aliases.go:157-160`). **Question:** is `scheduled` (a) dead and to be
removed from the lattice and both predicates, (b) a status `rd defer` SHOULD set,
or (c) reserved for an external client? A conformance vector cannot cover the
branch until this is answered. Related: `ReadyFilter`'s doc comment says "not
scheduled (pending a future date)" while `rd ready`'s help text still claims
"ETA is within the next 4 hours" (`cmd/rd/ready.go:25`), which §13.3 shows is
false.

**§15.2 Cross-board deps: non-blocking, but silently.** §8.9. The item spec for
this document calls for "cross-board deps NON-BLOCKING **with warnings**." The
nostr fold gives non-blocking WITHOUT warnings — the edge is dropped at
`pkg/sync/nostrproject.go:462-465` with no record, and
`Item.CrossCampfireWarnings` is never populated. Only the campfire fold warns
(`pkg/state/state.go:864-881`). **Question:** should `applyDepAndGateStatus`
populate `CrossCampfireWarnings` for an unresolvable `i` tag that
`IsCrossCampfireRef` matches, restoring parity? Note the write path already
refuses to CREATE such a dep (`cmd/rd/nostrwrite.go:351-353`), so today the case
arises only from a foreign client or a migrated card.

**§15.3 Read-side label validation does not exist on the nostr fold.** §10.1–§10.2.
The campfire fold enforces the atom pattern AND registry membership at derive time
and drops violators into `LabelWarnings`
(`pkg/state/state.go:594-612`). The nostr fold accepts any `l` tag verbatim
(`pkg/sync/nostrproject.go:583`). The code states this is intentional
("card labels are freeform", `cmd/rd/label.go:63`), but the result is that
`Item.LabelWarnings` is dead on the live path while remaining in the shipped JSON
schema. **Question:** delete the read-side registry concept for nostr (and drop
`LabelWarnings` from the surface), or re-introduce a board-scoped registry as
signed events? Until ruled, a conformance vector MUST NOT assert any label
validation.

**§15.4 `isStatusKind` accepts 1633 but rd never writes it.** §2.3, §14.10. A
foreign client's kind-1633 draft event WOULD fold into rd's history as an ordinary
status transition (`pkg/sync/nostrwire.go:534-536`,
`pkg/sync/nostrproject.go:293-295`). **Question:** intended interop, or should
1633 be excluded from `isStatusKind` so a draft cannot mutate rd state?

**§15.5 `Gate` survives on terminal items.** §9.5. The terminal branch clears
`WaitingOn`, `WaitingType`, `WaitingSince` and `GateMsgID` but NOT `Gate`
(`pkg/sync/nostrproject.go:494-500`), so a closed item can still report a gate
category. This is invisible to `GatesFilter` (which requires `waiting`) but
visible in `rd show` / JSON, and `FocusFilter` also cannot see it (terminal items
are not ready). **Question:** is the retained `Gate` deliberate provenance ("this
was closed while gated on design") or a missed clear?

**§15.6 `OverdueFilter` captures `now` at construction time.** §13.6. `now :=
time.Now()` runs when the filter is BUILT (`pkg/views/views.go:95`), not per
item. Harmless for a one-shot CLI invocation, but a long-lived process (or a
browser client that builds the filter once and re-applies it) will silently use a
stale clock. **Question:** move the clock read inside the closure, or document the
construct-per-use contract as normative?

**§15.7 `sortByPriorityETA` is not a total order.** §13.14. `sort.Slice` is
unstable and the comparator ties on equal `(priority, ETA)`
(`cmd/rd/ready.go:218-227`), so two items with the same priority and the same ETA
can render in either order across runs — the input slice already comes from
nondeterministic map iteration (`cmd/rd/nostr.go:912-916`). `rd list` does not have
this problem (it tie-breaks on ID, `cmd/rd/list.go:103-110`). **Question:** add an
ID tie-break to `sortByPriorityETA`? A conformance suite that compares rendered
output MUST NOT assert on `rd ready` ordering until it has one.

**§15.8 The frozen envelope spec's line citations have drifted.** Frozen §1 cites
`BuildCardEvent` at `pkg/sync/nostrwire.go:237-310` and §2 cites
`BuildStatusEvent` at `:319-344`; in the current tree they are at `:246-353` and
`:362-387`, and every per-tag line number in those two tables is off by roughly
nine lines. The tag→disposition CONTENT is still correct, and this document does
not modify the frozen doc. **Question:** does the freeze permit a
citation-only refresh, or should the frozen doc be left byte-stable and this
clause serve as the standing errata? This document's own citations are anchored
to the commit that introduced it and carry the same drift risk, which is an
argument for the conformance suite asserting on behaviour rather than on line
numbers.

**§15.9 `Description` is a permanent alias of `Context`.** §5.1. Both fields are
set from the card's `Content` (`pkg/sync/nostrproject.go:573-574`) and both are
overwritten together on confidential decrypt (`:606-607`, `:614-615`). The
campfire fold keeps them in sync too (`pkg/state/state.go:822`). They can never
diverge, so the nostr JSON surface ships the same string twice. **Question:**
retire `Description` (it is documented as "alias for context, for bd
compatibility", `pkg/state/state.go:100`), or is an external consumer still
reading it?

---

# Part II — the WRITE surface (normative)

Part I (§1–§15) specifies the READ projection: which events fold, in what order,
into what item state. Part II specifies the inverse: for every mutation a client
(including a browser board) can perform, exactly which signed events it MUST
produce, with which tags, in which order, stamped with which `created_at`.

Why this exists: the read projection is forgiving — an unknown tag is ignored, a
missing tag defaults to the zero value (§5.2). The write surface is not. An
independent client that guesses an event shape writes events rd folds
differently, or drops entirely (§3), and those events are **signed and
permanent**: there is no delete, no edit-in-place, and a wrong `created_at` can
beat a correct later write forever (§17.5).

Same discipline as Part I: every clause cites `path:line` of the code that
implements it **today**, and describes what that code actually does. Where the
writer and the read projection disagree, the clause states the actual behaviour
and files an entry in §27 — no bug is specced as intent, and no writer code was
changed for this document.

---

## 16. The write model

**§16.1 One durable act, two phases.** Every mutation builds one or more signed
events, appends them to the local append-only log (`Log.Append`, phase 1), then
best-effort publishes them to the write relays (phase 2)
(`Publisher.publishEvents`, `pkg/sync/nostroutbound.go:418-432`). **The log
append is the mutation.** A phase-1 failure aborts with an error
(`:425-427`); a phase-2 failure never fails the mutation — the event is buffered
for retry (`relayPublish`, `:437`) and the local log remains authoritative. An
independent client MUST treat its own durable store as the commit point and the
relay as replication, not as the writer of record.

**§16.2 Every event is signed by the actor key.** `Publisher.Key` is the
secp256k1 key for the current durable actor (`nostrKey`, `cmd/rd/nostr.go:147-159`;
actor selection `rdActor`, `:135-140`). Signing happens inside each builder
(`e.Sign(k)` — e.g. `pkg/sync/nostrwire.go:349-351`), so `pubkey`, `id` and `sig`
are derived, never caller-supplied. Any tag mutation after signing REQUIRES a
re-sign, because the id is the content hash of the canonical form
(`BuildStatusEventWithIssueRoot`, `pkg/sync/nostrwire.go:447-454`).

**§16.3 Read-modify-write, always from the projection.** Every mutation body
first resolves the item through `nostrResolveItem`
(`cmd/rd/nostrwrite.go:110-131`), which runs the **full fold** of Part I and
resolves by exact id, else by unique prefix; an unknown id or an ambiguous prefix
is a hard error (`:122`, `:128`). It then edits the resulting `*state.Item`
in memory and republishes the WHOLE item (§18.2). There is no partial-update
event: a client that writes a card carrying only the field it changed erases
every field it omitted.

**§16.4 Write path preconditions.** A mutation runs only on a nostr-native
project: a `.ready/` directory whose config pins a board coordinate
(`nostrNativeProject`, `cmd/rd/nostrwrite.go:63-72`; pin resolution
`nostrPinnedBoard`, `cmd/rd/nostr.go:281-290` — committed `.ready/board.json`
first, machine-local `config.json` second). No pin ⇒ every write command refuses
(`errNotNostrProject`, `cmd/rd/nostrwrite.go:47-56`). `nostrWriteActive`
(`:77-83`) is the publish-hook guard; when false the publish helpers return nil
and the mutation silently emits nothing (`cmd/rd/nostr.go:354-356`, `:390-392`).

**§16.5 Board author vs signer.** The board-membership coordinate a write
addresses is derived from the PINNED coordinate's owner, not from the signer:
`nostrBoardAuthor` (`cmd/rd/nostr.go:306-320`) parses `30301:<owner>:<boardD>`
and returns `<owner>`; with no pin it falls back to the signer's own pubkey
(`:319`). A present-but-unparseable pin is a HARD ERROR, never a fallback
(`:314-316`). This is what lets an agent key sign a card that still belongs to
the owner's board and therefore survives the read-side pin gate
(`pkg/sync/nostrproject.go:271-273`).

**§16.6 The board event (30301) is written only by its owner.** `PublishItem` is
passed a non-nil `*BoardSpec` only when `signer == boardAuthor`
(`cmd/rd/nostrwrite.go:178-180`; same test on the manual republish path,
`cmd/rd/nostr.go:539-542`). The spec is `{BoardD: <project prefix>, Title:
<project prefix>, Maintainers: [boardAuthor]}` (`boardSpecForProject`,
`cmd/rd/nostr.go:294-297`) and it emits tags `d`, `title`, one `p` per maintainer
(`BuildBoardEvent`, `pkg/sync/nostrwire.go:201-221`). Because board maintainers
are read latest-wins from the WINNING board only (§4.5, §6.1), republishing a
board is a **maintainer-set rewrite**: any maintainer absent from the new `p`
tags is revoked. An agent MUST NOT publish a board event for a board it does not
author.

**§16.7 Confidential sealing is decided per write, from the log.** Immediately
before building the card, every live write calls `setCardEnvelope`
(`cmd/rd/confidential.go:230-237`) → `boardConfidentialEnvelope` (`:84-156`),
which returns: `nil` (plaintext) when the project config opts out
(`boardIsConfidential`, `:55-61`, default **confidential**) or no board is pinned
(`:92-94`); the current-epoch `Envelope` when the writer's keyring yields one
(`envelopeFromKeyring`, `:65-76`); a freshly minted epoch-1 CEK+LTK plus an
owner self-grant when the owner writes first (`:138-155`,
`publishOwnerCEKSelfGrant`, `:243-266`); or a hard error when the board is
confidential and the writer holds no key (`:126-132`). A non-owner writing to a
not-yet-bootstrapped board writes plaintext (`:133-136`). Consequence for an
independent client: **you cannot write to a confidential board without a
grant-borne CEK.** Publishing a plaintext card there is not a degraded write, it
is a quarantined one (§3, `pkg/sync/envelope.go:100-116`).

**§16.8 The reserved-board guard.** Any `Publisher` not marked `Production` is
refused the instant a built event addresses board d-tag `"ready"` — the board
event itself, or any `a` tag whose d-component matches
(`hitsReservedBoard`, `pkg/sync/nostroutbound.go:85-99`; `guardReservedBoard`,
`:110-120`, called from `publishEvents` `:419` and `PublishEventsUnique` `:381`).
The real CLI publisher sets `Production: true` (`cmd/rd/nostr.go:263`). This is a
repo-local test-safety rail, not a protocol rule, but a conformance harness that
constructs a `Publisher` will hit it.

---

## 17. `created_at` selection and monotonicity

**§17.1 Unit.** `created_at` is unix **seconds** (NIP-01). Every builder takes it
as an explicit argument so ids are deterministic and testable
(`BuildCardEvent`, `pkg/sync/nostrwire.go:246`; `BuildStatusEvent`, `:362`;
`BuildBoardEvent`, `:201`; `BuildRoleGrantEvent`, `pkg/sync/rolegrant.go:112`).
No builder calls `time.Now()`.

**§17.2 The rule.** A live mutation stamps
`created_at = max(now, newestInScope + 1)` (`nostrNextCreatedAt`,
`cmd/rd/nostr.go:222-241`; the max scan is `:228-236`, the bump `:237-239`).
`newestInScope` is the greatest `created_at` among events in the local log whose
`DriftScope` equals the target scope (`:230-232`).

**§17.3 Scope.** `DriftScope` (`pkg/sync/nostrwire.go:549-569`) is the event's
**causal chain**: `item:<itemID>` for a card, status or issue event (`:565-568`,
via `itemIDForEvent` `:585-602`), `grant:<boardD>:<grantee>` for a 39301
(`:553-558`), `board:<d>` for a 30301 (`:559-564`), `""` for anything else (which
therefore matches no scope). Callers name the scope explicitly:
`ItemDriftScope(item.ID)` for every item mutation
(`pkg/sync/nostrwire.go:574`; used at `cmd/rd/nostrwrite.go:181`,
`cmd/rd/nostr.go:373`, `:409`) and `GrantDriftScope(boardD, grantee)` for grants
(`pkg/sync/nostrwire.go:580`; used at `cmd/rd/confidential.go:216`).

**§17.4 Why scoped and not log-wide.** A log-wide max let an unrelated burst
(`rd engage` over N items) inflate the NEXT write to ANY item by one second per
burst event, drifting it into the future where it beat a genuinely later
cross-machine edit. Scoping bounds a single chain's future-drift to the count of
same-second writes to that chain (`cmd/rd/nostr.go:210-221`).

**§17.5 What this guarantees, and what it does not.** Within one chain on one
machine, two writes in the same wall-clock second cannot collide: the second is
stamped at least one second later, so §4.1's `(created_at, lowest event id)`
tiebreak is never reached and **intent order wins**. Across machines it
guarantees nothing: two genuinely concurrent same-second writes to the same item
still resolve by lowest event id (§4.1, `newerThan`,
`pkg/sync/nostrproject.go:552-557`), which is content-hash order — i.e. a lost
update. An independent client MUST implement §17.2 (a client that stamps plain
`time.Now()` will lose its own second write to its own first write whenever the
first has the lower id).

**§17.6 One `created_at` per publish call, shared by every event in it.** A
create publishes board + card + issue + status all stamped with the same value
(`PublishItemWithReason`, `pkg/sync/nostroutbound.go:183`, `:189`, `:195`,
`:203` — all passed the same `createdAt`); a status change publishes card + status
(and possibly the issue event) at one value
(`PublishStatusChange`, `:223`, `:228`, `:233`). Because the events land in the
log before the next mutation reads it, a subsequent mutation to the same item is
stamped strictly later (§17.2).

**§17.7 Multi-publish commands stamp per publish, not per command.**
`runUpdateNostr` can issue up to three separate publishes in one invocation — a
card edit (`cmd/rd/nostrwrite.go:462`), a status change (`:475`), a claim status
change (`:483`) — and each calls `nostrNextCreatedAt` independently after the
previous publish has already appended. So a single `rd update --title X --status
active` produces events at `T` and `T+1`, in that order, deterministically.

**§17.8 The cutover stamp is the one log-wide exception.** The owner CEK
self-grant that makes a board confidential is stamped strictly after EVERY event
in the log, not merely its own scope (`cutoverCreatedAt`,
`cmd/rd/confidential.go:32-47`), so every pre-existing plaintext card is genuinely
pre-cutover and is grandfathered by the fold gate (§11,
`pkg/sync/envelope.go:111-114`).

**§17.9 Two republish paths do NOT follow §17.2.** `rd log publish <item>`
(`cmd/rd/nostr.go:549`) and `rd log put` (`:726`, `:728`) stamp
`time.Now().Unix()`. Neither is reachable from a board UI; both are recorded in
§27.3.

---

## 18. Card events (kind 30302) — create and edit

**§18.1 Identity and coordinates.** A card's `d` tag is the rd item id
(`pkg/sync/nostrwire.go:251`), making the card addressable at
`30302:<signerPubkey>:<itemID>` (`CardCoord`, `:196`). Its `a` tag is the
BOARD-membership coordinate `30301:<boardAuthor>:<boardD>`
(`cardBoardCoord`, `:232-241`, emitted at `:258-260`) where `boardAuthor` is
`CardSpec.BoardAuthor` when set and the signer otherwise (`:236-239`). The `a`
tag is **omitted entirely** when `BoardD` is empty (`:233-235`). The read side
compares this exact string against the pinned coordinate and drops any card that
differs (`pkg/sync/nostrproject.go:271-273`), so the `a` tag MUST be the owner's
board coordinate, byte-for-byte.

**§18.2 A card carries the WHOLE item.** Every live write builds its `CardSpec`
through the single helper `CardSpecFromItem`
(`pkg/sync/nostrmigrate.go:106-127`) and then sets `BoardAuthor`
(`cmd/rd/nostrwrite.go:168-169`, `cmd/rd/nostr.go:367-368`, `:403-404`). Because
the card is a latest-wins projection (§4.1) with no merge, **an omitted field is
a deleted field**. Note `CardSpec.Assignee ← item.By`
(`pkg/sync/nostrmigrate.go:112`) → the `p` tag; `For` is a separate tag (§18.3).
`CardSpecFromItem` does NOT carry `WaitingSince`, `GateMsgID`, `Blocks`,
`History`, `MsgID` or `Project` — all four of the first are re-derived by the
fold (§9.6, §8.5), and `Project` has no card tag at all (§27.6).

**§18.3 The card tag table.** `BuildCardEvent`
(`pkg/sync/nostrwire.go:246-353`) emits exactly these tags, **in this order**,
each only when its source field is non-empty:

| # | Tag | Value | Emitted when | Cite | Folds to (Part I) |
|---|---|---|---|---|---|
| 1 | `d` | item id | always (empty id is an error) | `:248-251` | `ID` (§5.1) |
| 2 | `title` | title | **plaintext mode only** | `:255-257` | `Title` (§5.1) |
| 3 | `a` | `30301:<boardAuthor>:<boardD>` | `BoardD != ""` | `:258-260` | board pin gate (§3) |
| 4 | `s` | exact rd status | `Status != ""` | `:261-263` | `Status` (§5.1) |
| 5 | `rank` | priority | `Priority != ""` | `:264-266` | `Priority` fallback (§5.1) |
| 6 | `priority` | priority | `Priority != ""` | `:267` | `Priority` (§5.1) |
| 7 | `itype` | item type | `Type != ""` | `:269-271` | `Type` (§5.1) |
| 8 | `p` | assignee pubkey (`item.By`) | `Assignee != ""` | `:272-274` | `By` (§5.1) |
| 9 | `i` | one per blocker item id | per non-empty dep | `:275-279` | raw `BlockedBy` (§8.1) |
| 10 | `gate` | escalation category | `Gate != ""` | `:280-282` | `Gate` (§5.1) |
| 11 | `waiting_type` | waiting type | `WaitingType != ""` | `:283-285` | `WaitingType` (§5.1) |
| 12 | `waiting_on` | free text | `WaitingOn != ""` AND **plaintext mode** | `:288-290` | `WaitingOn` (§5.1) |
| 13 | `l` | label atom, or its HMAC token | per label, see §23.3 | `:291-311` | `Labels` (§5.1, §10) |
| 14 | `eta` | RFC3339 | `ETA != ""` | `:312-314` | `ETA` (§5.1) |
| 15 | `level` | humanness level | `Level != ""` | `:319-321` | `Level` (§5.1) |
| 16 | `for` | assignment scope | `For != ""` | `:322-324` | `For` (§5.1) |
| 17 | `parent` | parent item id | `ParentID != ""` | `:325-327` | `ParentID` (§5.1) |
| 18 | `due` | RFC3339 | `Due != ""` | `:328-330` | `Due` (§5.1) |
| 19 | `enc` | `"1"` | confidential mode | `pkg/sync/nostrwire.go:341`; `pkg/sync/envelope.go:318-323` | §11.1 |
| 20 | `cek_epoch` | epoch integer | confidential mode | `pkg/sync/nostrwire.go:341`; `pkg/sync/envelope.go:318-323` | §11.1 |

Tag ORDER is load-bearing in exactly one place: the fold reads the FIRST `a` tag
(`tagValue`, `pkg/sync/nostrwire.go:512-519`) to resolve the item's
status-authority set (§6.4, `pkg/sync/nostrproject.go:337`). A card has only one
`a` tag, so any order preserves that; a client that adds a second `a` tag ahead
of the board coordinate breaks authority resolution.

**§18.4 Content — plaintext mode.** `event.Content` is the item's
context/description verbatim (`pkg/sync/nostrwire.go:334`), which the fold
assigns to BOTH `Context` and `Description` (§5.1, §15.9).

**§18.5 Content — confidential mode: what is sealed.** When `CardSpec.Enc` is
non-nil, Content is replaced by the sealed blob (`pkg/sync/nostrwire.go:336-340`) and the
two marker tags are appended (`:341`). The sealed plaintext is the JSON object
`cardPayload` (`pkg/sync/envelope.go:221-228`) with exactly four members:

| JSON key | Source | omitempty |
|---|---|---|
| `title` | `spec.Title` | no |
| `context` | `spec.Context` | yes |
| `waiting_on` | `spec.WaitingOn` | yes |
| `labels` | `spec.Labels` | yes |

built by `sealCardPayload` (`pkg/sync/envelope.go:293-305`). Write and read MUST
agree byte-for-byte on this struct — the read side unmarshals the same type
(`decryptCardPayload`, `:157-171`). Everything NOT in this table stays a clear
tag (§18.3): status, priority, type, assignee, deps, gate category, waiting type,
eta, level, for, parent, due. **Sealing is free-text-only; routing is public.**

**§18.6 Content wire format.** `event.Content =
base64Std( nonce(12) ‖ ChaCha20-Poly1305(CEK, nonce, plaintext) )` with a fresh
`crypto/rand` nonce per call (`sealContent`, `pkg/sync/envelope.go:239-253`).
This is the FROZEN envelope §3; this clause restates it, it does not amend it.

**§18.7 Marker tags.** Exactly two, both always clear: `["enc","1"]` and
`["cek_epoch","<int>"]` (`encMarkerTags`, `pkg/sync/envelope.go:318-323`;
constants `:191-200`). `enc` MUST be the literal `"1"` — the read-side
well-formedness gate rejects any other version, absent value, unparseable epoch,
or a body shorter than nonce+tag, and quarantines the event
(`encWellFormed`, `pkg/sync/envelope.go:73-85`; `shouldQuarantine`, `:100-116`).
There is NEVER a content-hash tag (frozen envelope §6, restated at
`pkg/sync/nostrwire.go:333`).

**§18.8 Card create.** `runCreateNostr` (`cmd/rd/nostrwrite.go:504-551`) derives
the id (generated from the project prefix, collision-checked against the whole
projection, `:516-529`), defaults `For` to the signer unless `--for` was given
(`:509-514`), builds the item with `Status: state.StatusInbox` (`:541`), and calls
`publishItemFullCreateNostr` (`:547`). That publishes, in ONE call at ONE
`created_at` (§17.6): the board event **iff** the signer is the board author
(§16.6), the card, the kind-1621 issue event iff none exists yet and the board is
plaintext (§19.6), and a kind-1630 status event carrying `status=inbox`
(`PublishItemWithReason`, `pkg/sync/nostroutbound.go:177-210`). A fresh item is
therefore ALWAYS created with at least one authoritative status event, so §6.11
(card `s` tag stands alone) is unreachable for rd-created items.

**§18.9 Card edit.** A pure field edit publishes ONLY a refreshed card, with no
status event (`PublishCardEdit`, `pkg/sync/nostroutbound.go:285-293`, via
`publishItemCardEditNostr`, `cmd/rd/nostr.go:389-419`). This is the hybrid
model's invariant: **a card edit can neither add to nor erase history** (§5.5,
§6.5) — history lives only in the status chain. An independent client that wants
a transition recorded MUST publish a status event; rewriting the card's `s` tag
alone is invisible to history and, for any item that has an authoritative status
event, is overwritten on read by §6.10.

**§18.10 Card edits re-seal.** `publishItemCardEditNostr` calls `setCardEnvelope`
(`cmd/rd/nostr.go:406`) exactly as the create path does, so an edit on a
confidential board re-seals under the CURRENT epoch with a FRESH nonce. Two
byte-identical logical cards therefore have different `Content` and different
event ids; there is no content-based dedup, only id dedup (§3.2).

---

## 19. Status events (kinds 1630 / 1631 / 1632)

**§19.1 Kind selection.** The kind is a pure function of the rd status:
`done → 1631`, `cancelled|failed → 1632`, everything else (including any unknown
string) `→ 1630` (`statusKindFor`, `pkg/sync/nostrwire.go:72-82`). The mapping is
lossy on purpose. `1633` is reserved and rd never writes it (§2.3, §14.10).

**§19.2 The status tag is authoritative, the kind is not.** The EXACT rd status
rides the `status` tag (`pkg/sync/nostrwire.go:371`) and that is what the fold
reads (§6.5–§6.6); the kind is never consulted as a fallback. A client that emits
kind 1631 with `status=active` produces an item that is `active`. Emit both
consistently anyway: generic NIP-34 clients read only the kind.

**§19.3 Base tag list.** `BuildStatusEvent` (`pkg/sync/nostrwire.go:362-387`)
emits, in this order:

| # | Tag | Value | Emitted when | Cite |
|---|---|---|---|---|
| 1 | `a` | `30302:<signerPubkey>:<itemID>` — the CARD coordinate | always | `:370` |
| 2 | `d` | item id | always | `:371` |
| 3 | `status` | exact rd status | always (empty is an error, `:367`) | `:372` |
| 4 | `e` | the concrete card event id | `cardEventID != ""` | `:374-376` |

`Content` is the close/change reason, verbatim in plaintext mode (`:381`). Both
an empty item id (`:363-365`) and an empty status (`:366-368`) are hard errors.

**§19.4 Additive anchors — the live path.** The live path never calls
`BuildStatusEvent` directly; it calls `BuildStatusEventWithIssueRoot`
(`pkg/sync/nostrwire.go:416-455`), which appends, in this order, to the base list:

- the sealed Content + `enc`/`cek_epoch` markers when an envelope is supplied,
  REPLACING the plaintext reason before signing so the cleartext reason is never
  signed or published (`:427-435`; `sealStatusPayload` seals the JSON object
  `{"reason": "<text>"}`, `pkg/sync/envelope.go:230-234`, `:308-314`);
- `["e", <issueEventID>, "", "root"]` — the NIP-10 marked anchor to the item's
  kind-1621 issue event, when one exists (`pkg/sync/nostrwire.go:437-442`);
- a SECOND `a` tag carrying the BOARD coordinate `30301:<owner>:<boardD>`
  (`pkg/sync/nostrwire.go:443-446`).

Any of these changes the tag set, so the event is re-signed (`pkg/sync/nostrwire.go:447-454`).

**§19.5 Tag ORDER on a status event is normative.** rd's own fold reads only the
FIRST `a` (the card coordinate) and the FIRST `e`
(`tagValue`, `pkg/sync/nostrwire.go:512-519`), while the board-scoped sync filter
and the confidential fold gate scan ALL `a` tags for the one with the `30301:`
prefix (`boardCoordOf`, `pkg/sync/envelope.go:123-131`; rationale at
`pkg/sync/nostrwire.go:402-415`). A client that puts the board coordinate FIRST
therefore still syncs and still passes the fold gate, but presents the board
coordinate where rd expects a card coordinate. **Emit card-coordinate `a` first,
board-coordinate `a` second.**

**§19.6 The issue event (kind 1621) is written at most once per item.**
`ensureIssueEvent` (`pkg/sync/nostroutbound.go:252-275`) scans the local log for
an existing 1621 whose `d` tag matches the item (`FindIssueEventID`,
`pkg/sync/nostrwire.go:502-509`) and builds a new one only on a miss (`pkg/sync/nostroutbound.go:270-274`).
It emits tags `d`=item id and `subject`=title, with the description as Content
(`BuildIssueEvent`, `pkg/sync/nostrwire.go:477-495`). **On a confidential board
the issue event is suppressed entirely** (`pkg/sync/nostroutbound.go:260-262`) —
it would leak the two most sensitive free-text fields in the clear — and the
status event then carries no issue-root anchor. The issue event never folds
(§2.4), so a client MAY omit it; if it emits one it MUST emit at most one per
item, or generic clients will see duplicate issues.

**§19.7 The `by` tag rule.** A live write MUST NOT emit a `by` tag. No live
writer does: only the campfire→nostr migration replay adds one
(`BuildHistoricalStatusEvent`, `pkg/sync/nostrmigrate.go:61-63`), to preserve the
original actor for history it did not sign. The read side honours a `by` tag ONLY
when the SIGNER is a board maintainer, and otherwise falls back to the signer
pubkey (`pkg/sync/nostrproject.go:401-404`). So for a browser client the rule is
simple and absolute: **omit `by`; attribution is the signing key.** A contributor
who emits `by` gets it silently ignored; a maintainer who emits it rewrites
provenance, which is a migration capability, not an editing one.

**§19.8 Authority.** A status event only counts if its author is the winning
card's author or a maintainer of the board named by that card's first `a` tag
(§6.4). A non-authoritative status event contributes NEITHER state NOR history —
it is not "weaker", it is invisible. An independent client whose key is a
contributor (level 1) is authoritative for the items whose cards it authored, and
for nothing else; to transition someone else's item it needs maintainer level
(§6.2, `pkg/sync/nostrproject.go:316-322`).

**§19.9 Every status event rides with a refreshed card.** No live path publishes
a bare status event. `PublishStatusChange`
(`pkg/sync/nostroutbound.go:219-243`) always builds the card FIRST (`:223`), uses
its event id as the status event's `e` anchor (`:233`), and publishes
`[card, (issue), status]` in that order (`:237-241`). A client MUST publish the
card first for the same reason: the `e` tag names a concrete event that must
exist.

---

## 20. Status-carrying mutations

All four commands below funnel into `publishItemStatusChangeNostr`
(`cmd/rd/nostr.go:353-383`) → `PublishStatusChange` (§19.9), so they share §18's
card shape, §19's status shape, and §17's stamping. They differ only in the
precondition, the in-memory field edits, and the reason string.

**§20.1 Claim.** `runClaimNostr` (`cmd/rd/nostrwrite.go:212-230`): refuses a
terminal item (`:221-223`), sets `Status=active` and `By=<signer pubkey>`
(`:224-225`, signer from `nostrSelfPubkey`, `:98-104`), publishes with the
optional `--reason` as the status-event reason (`:226`). Wire result: a card with
`s=active` + `p=<signer>`, and a kind-1630 status event with `status=active`.

**§20.2 Delegate.** `runDelegateNostr` (`cmd/rd/nostrwrite.go:255-269`): refuses a
terminal item (`:260-262`), sets `By=<to>` and changes NOTHING else — including
not changing the status (`:263`) — then publishes a status event that re-affirms
the current status so the reassignment lands in history (`:264`). Wire result: a
card with a new `p` tag, and a status event whose `status` equals the item's
UNCHANGED status.

**§20.3 Close / fail / cancel.** `runCloseNostr`
(`cmd/rd/nostrwrite.go:234-250`): refuses an already-terminal item (`:239-241`),
snapshots `item.Blocks` BEFORE the transition (`:242`), maps the resolution
through `closeResolutionToStatus` — `cancelled → cancelled`, `failed → failed`,
anything else → `done` (`cmd/rd/nostr.go:334-343`) — publishes (`cmd/rd/nostrwrite.go:244`), then
runs the implicit unblock (§21.4, `cmd/rd/nostrwrite.go:247`). Wire result: a card with `s=done` (or
`cancelled`/`failed`) and a kind-**1631** (done) or kind-**1632**
(cancelled/failed) status event, plus one card-only republish per item this item
was blocking. `rd done` and `rd complete` route here with resolution `"done"`
(`cmd/rd/close.go:35`, `cmd/rd/complete.go:56`); `rd cancel`/`rd kill` with
`"cancelled"` (`cmd/rd/aliases.go:107`, `:119`); the shared alias factory covers
`done`/`fail`/`cancel` (`cmd/rd/aliases.go:222`).

**§20.4 Explicit status set.** `runUpdateNostr`'s status block
(`cmd/rd/nostrwrite.go:467-478`) assigns `Status=<statusTo>` and, when supplied,
`WaitingOn` / `WaitingType` (`:468-474`), then publishes with `--note` as the
reason (`:475`). The CLI auto-sets `statusTo=waiting` when `--waiting-on` is given
without `--status` (`cmd/rd/update.go:52-55`) and resolves bd-style status
aliases before dispatch (`:66-72`). Note the terminal guard here is
**field-conditional**: `runUpdateNostr` refuses a terminal item only when a FIELD
update is also present (`cmd/rd/nostrwrite.go:439-441`) — a status-only update
can reopen a terminal item. See §27.2.

**§20.5 A close does NOT clear the gate category.** `runCloseNostr` sets only
`Status`; `Gate`, `WaitingType` and `WaitingOn` are still carried onto the
republished card by `CardSpecFromItem`. The fold then clears `WaitingOn`,
`WaitingType`, `WaitingSince` and `GateMsgID` on terminal items but NOT `Gate`
(§9.5, `pkg/sync/nostrproject.go:494-500`). The writer and the reader agree; the
retained `Gate` is already filed as §15.5.

---

## 21. Dependency edges

**§21.1 An edge is a card tag, never its own event.** There is no dep event kind.
`rd dep add` appends the blocker id to the BLOCKED item's `BlockedBy` and
republishes THAT item's card only (`runDepAddNostr`,
`cmd/rd/nostrwrite.go:350-368`; append via `strSliceAppendUnique`,
`cmd/rd/nostr.go:422-429`; publish `cmd/rd/nostrwrite.go:363`). `rd dep remove` strips it and
republishes the same single card (`runDepRemoveNostr`, `:373-388`; removal via
`strSliceRemove`, `cmd/rd/nostr.go:432-444`; publish `cmd/rd/nostrwrite.go:383`). The BLOCKER's card is
never touched — `Item.Blocks` is derived by the fold (§8.5), never written.

**§21.2 Both endpoints must already exist.** Both ids are resolved through the
projection before anything is published (`cmd/rd/nostrwrite.go:354-361`, `:374-381`), so a dep to an
unknown item fails at write time rather than producing an `i` tag the fold would
silently drop (§8.2).

**§21.3 Cross-board deps are refused at write time.** `runDepAddNostr` rejects a
blocked-item argument that looks like a cross-campfire reference
(`cmd/rd/nostrwrite.go:351-353`, via `state.IsCrossCampfireRef`). Note the guard
tests only the BLOCKED argument, not the BLOCKER (§27.4).

**§21.4 Implicit unblock is a write-side courtesy, not a fold rule.** On close,
rd republishes the card of every item the closing item was blocking
(`publishImplicitUnblockNostrNative`, `cmd/rd/nostrwrite.go:635-649`), each as a
plain card edit (`:645`), each failure warned and skipped rather than fatal
(`:642`, `:646`). The fold does not need this — §8.4 already ignores terminal
blockers on the next replay — so an independent client MAY omit it. What it MUST
NOT do is write `s=blocked` to express a dependency: `blocked` is derived (§7.6,
§8.4) and any authored value is overwritten on the next replay.

**§21.5 Status is never written for a dep change.** Both dep commands use
`publishItemCardEditNostr`, so a dep change adds no history entry (§18.9). An
independent client that wants a dependency change in the audit trail cannot get
one today.

---

## 22. Gate open → resolve (write side)

**§22.1 Open.** `runGateNostr` (`cmd/rd/nostrwrite.go:273-296`) refuses a terminal
item (`:278-280`), then sets FOUR fields — `Status=waiting`, `Gate=<type>`,
`WaitingType="gate"`, `WaitingOn=<description>` (`:281-284`) — and publishes a
status change whose reason IS the description (`:285`). Wire result: a card
carrying `s=waiting` + `gate=<type>` + `waiting_type=gate` + `waiting_on=<desc>`
(the last omitted and sealed in confidential mode, §18.3 row 12) and a kind-1630
status event with `status=waiting`. It then RE-RESOLVES the item purely to report
the projection-derived `GateMsgID` (`:288-293`) — there is no gate event, and even
the writer learns the gate id from the fold (§9.6: `GateMsgID` IS the winning
card's event id).

**§22.2 Approve.** `runApproveNostr` (`cmd/rd/nostrwrite.go:299-321`) requires a
pending gate — `GateMsgID != "" OR Gate != "" OR WaitingType == "gate"`
(`:304-306`) — AND `Status == waiting` (`:307-309`). It sets `Status=active` and
CLEARS all five gate fields: `Gate`, `WaitingType`, `WaitingOn`, `WaitingSince`,
`GateMsgID` (`:310-315`), then publishes (`:316`). Because the republished card
omits `gate` / `waiting_type` / `waiting_on`, the fold's card-declared gate
promotion (§9.4) finds nothing to promote and the item stays `active`. A client
that clears only `Status` and leaves the gate tags on the card will see the item
snap back to `waiting` on the next read.

**§22.3 Reject.** `runRejectNostr` (`cmd/rd/nostrwrite.go:327-345`) applies the
SAME two preconditions (`:332-337`) and then changes NO field: it republishes the
card unchanged and emits a kind-1630 status event that re-affirms `status=waiting`
with the rejection reason as Content (`:340`). The gate stays open; the ruling
survives as a history entry (§6.5). Rejecting is therefore not a state transition
at all — it is a durable note attached to the still-open gate.

**§22.4 Approve and reject are indistinguishable on the wire except by
reason.** Both are kind-1630 status events on a `waiting` item; approve differs
only in that its status tag is `active` and its card drops the gate tags. There
is no `resolution` tag — the JSON `{"resolution":"approved"|"rejected"}` is CLI
output only (`cmd/rd/nostrwrite.go:319-320`, `:343-344`), never an event field. An independent client
MUST NOT invent one; a reviewer reading history distinguishes them by the
`waiting → active` transition versus a `waiting → waiting` self-transition.

**§22.5 `WaitingSince` and `GateMsgID` are never written.** Neither is in
`CardSpec` (§18.2); both are derived at fold time (§9.6). Clearing them in memory
(`cmd/rd/nostrwrite.go:314-315`) affects only the in-process value.

---

## 23. Labels

**§23.1 Add / remove are card-only edits.** `runLabelAddNostr`
(`cmd/rd/nostrwrite.go:391-402`) appends the atom if absent (`:396`) and
republishes the card (`:397`); `runLabelRemoveNostr` (`:406-417`) removes every
occurrence (`:411`) and republishes (`:412`). Removing an absent label is
idempotent — no error. Neither publishes a status event, so a label change leaves
no history entry (§18.9). Both are reachable only on a nostr-native project
(`cmd/rd/label.go:314-315`, `:339-340`).

**§23.2 No validation happens on the write path.** Neither command checks the
atom against a pattern or a registry — the label string is written verbatim.
This contradicts `rd label add`'s own help text ("The label must be registered in
the project label registry", `cmd/rd/label.go:301`) and matches the read side,
which also validates nothing (§10.1). Filed as §27.5.

**§23.3 The three label emission modes.** `BuildCardEvent`
(`pkg/sync/nostrwire.go:291-311`) branches per label:

1. **Plaintext board** (`Enc == nil`): `["l", "<atom>"]` verbatim (`pkg/sync/nostrwire.go:307-309`).
2. **Confidential board WITH an LTK** (`Enc.LTK != nil`):
   `["l", hex(HMAC-SHA256(LTK, atom))]` — lowercase hex, no prefix
   (`labelToken`, `pkg/sync/envelope.go:284-288`; emitted `pkg/sync/nostrwire.go:296-301`). The
   plaintext atom ALSO rides inside the sealed `cardPayload.labels` for
   member-side rendering (§18.5).
3. **Confidential board with NO LTK**: **no `l` tag at all** (`pkg/sync/nostrwire.go:302-306`). Not a
   plaintext fallback — emitting the atom would leak it. The label survives only
   inside the sealed blob, and rd filters labels client-side anyway (§10.5), so
   nothing is lost but relay-side `#l` filtering.

An empty atom is skipped in every mode (`pkg/sync/nostrwire.go:292-294`).

**§23.4 Tokenization is equality-preserving and board-scoped.** Same atom + same
LTK ⇒ same token, so a relay can exact-match `#l` without seeing plaintext; a
different board's LTK yields a different token
(`pkg/sync/envelope.go:280-283`). The LTK is stable ACROSS CEK epochs
(`pkg/sync/envelope.go:216-218`), so a CEK rotation does not invalidate previously
written label tokens. It is distributed in the same owner-signed grant as the CEK
(`RoleGrantSpec.WrappedLTK`, `pkg/sync/rolegrant.go`; wrapped at
`cmd/rd/confidential.go:207`, `:249`).

**§23.5 A member who cannot decrypt MUST NOT write.** On the read side an
undecryptable confidential card projects `Labels` as the opaque tokens
(`pkg/sync/nostrproject.go:617-619` comment) and `Title`/`Context` as
`placeholderText` (`:612-620`). Round-tripping that item through any card edit
would re-seal the placeholder over the real title and re-tokenize already-tokenized
labels. The write path guards the no-key case by erroring
(`cmd/rd/confidential.go:126-132`), but NOT the holds-a-newer-epoch-only case —
see §27.1.

---

## 24. Field edits: title, priority, context, ETA, due, level

**§24.1 All six are card rewrites, not separate events.** `runUpdateNostr`'s field
block (`cmd/rd/nostrwrite.go:443-465`) assigns `Title`, `Context`, `Priority`,
`ETA`, `Due`, `Level` — each only when the corresponding flag was non-empty
(`:444-461`) — and publishes ONE card edit (`:462`). There is no title event, no
priority event, no `rank`-only event. A retitle and a re-prioritise are the same
kind of act: republish the whole card (§18.2) with one tag different.

**§24.2 Empty string means "unchanged", not "clear".** Every field assignment is
guarded by `!= ""` (`cmd/rd/nostrwrite.go:444`, `:447`, `:450`, `:453`, `:456`, `:459`), so no `rd
update` flag can CLEAR a field. Combined with §18.2 (an omitted field is a deleted
field) this gives an asymmetry an independent client must understand: rd's own CLI
cannot empty a field, but any client that builds a `CardSpec` directly and leaves a
field empty WILL empty it on the winning card.

**§24.3 Priority emits two tags.** `Priority` produces BOTH `rank` and `priority`
with the same value (`pkg/sync/nostrwire.go:264-268`); the fold prefers
`priority` and falls back to `rank` (§5.1,
`pkg/sync/nostrproject.go:571`). A client MUST write both, or interop with
NIP-100 clients that order by `rank` breaks.

**§24.4 Title on a confidential board is not a tag.** In confidential mode the
clear `title` tag is DROPPED and the title moves into the sealed blob
(`pkg/sync/nostrwire.go:255-257`, `pkg/sync/envelope.go:294`). So on a
confidential board a retitle changes no visible tag at all — only `Content` and
therefore the event id.

**§24.5 Field edit + status change in one command = two publishes.** When both
`hasFieldUpdate` and `hasStatusUpdate` are set, the card edit is published FIRST
(`cmd/rd/nostrwrite.go:462`), then the status change publishes ANOTHER card plus
the status event (`:475`) at `created_at + 1` (§17.7). The intermediate card is a
real, permanent event; there is no transactional batching.

**§24.6 `rd defer` and `rd progress` are field edits.** `rd defer` normalises the
ETA expression and routes to a card-only `eta` edit
(`cmd/rd/aliases.go:151-160`) — it does NOT set `status=scheduled` (§7.8, §15.1).
`rd progress` reads the current context, appends `"\n\n[<UTC minute>] <notes>"`
(`cmd/rd/aliases.go:197-203`), and routes to a card-only `context` edit (`:204`);
it refuses a terminal item first (`:192-194`). Progress notes are therefore
**context text, not history entries** — they do not appear in `History` (§18.9).

**§24.7 `--claim` inside `rd update`.** The claim block sets `Status=active` and
`By=<signer>` and publishes a status change with an EMPTY reason
(`cmd/rd/nostrwrite.go:480-486`). Because the CLI also sets `statusTo=active`
when `--claim` is passed without `--status` (`cmd/rd/update.go:47-50`), a bare
`rd update --claim` satisfies BOTH the status block and the claim block and
publishes TWO status changes. See §27.2.

---

## 25. What a write MUST NOT do

**§25.1 No new kinds.** The only kinds that fold are 30301, 30302, 1630–1633 and
39301 (§2). Any other kind is dropped by the `itemIDForEvent == ""` guard
(`pkg/sync/nostrproject.go:261-264`). Writing item state in a kind rd does not
fold produces an event that is signed, replicated, permanent — and invisible.

**§25.2 No new envelope versions.** `enc` MUST be exactly `"1"`. A card or status
event carrying any other value fails `encWellFormed`
(`pkg/sync/envelope.go:74`) and is QUARANTINED on a confidential board
(`shouldQuarantine`, `:100-116`) — the event does not degrade to plaintext, it
disappears from the projection and from history. The same holds for an
unparseable `cek_epoch` (`:77-79`) or a Content body shorter than
nonce+AEAD-overhead (`:80-84`).

**§25.3 No plaintext writes to a confidential board.** A post-cutover event with
no `enc` marker is quarantined (`pkg/sync/envelope.go:111-115`); only a genuine PRE-cutover plaintext
event is grandfathered (`pkg/sync/envelope.go:111-114`), and that grandfathering
happens only inside the full-log replay — never in the live ingest path
(`pkg/sync/envelope.go:94-96`, comment). This is why §16.7's write path hard-errors instead of
downgrading.

**§25.4 No writing to a board coordinate you have no grant for.** The card's `a`
tag MUST equal the reader's pinned coordinate or the card is dropped
(`pkg/sync/nostrproject.go:271-273`), and the author MUST be in the read-trust set
— self, config-trusted, or a cap-valid 39301 grantee for that board
(`:235-237`, `grantTrusts`) — with a revoked key's post-revocation events dropped
by the point-in-time gate (`:246-248`). **Note this is enforced entirely on the
READ side.** The writer performs no self-authorization check: rd will happily
sign and publish a card for a board it has no grant on, and the event simply
never folds for anyone else. An independent client MUST therefore treat "do I
hold a grant" as its own precondition. The one write-side refusal that does exist
is the reserved-board guard (§16.8).

**§25.5 No `by` tag from a non-migration writer.** §19.7.

**§25.6 No authored `blocked` status, no authored `WaitingSince`/`GateMsgID`,
no authored `Blocks`.** All are derived (§7.6, §8.4, §8.5, §9.6). Writing them is
at best ignored and at worst persists a stale derived value into the card's `s`
tag (§27.7).

**§25.7 No second `a` tag ahead of the primary one.** §18.3, §19.5.

**§25.8 No content-hash tag, ever.** Frozen envelope §6; restated at
`pkg/sync/nostrwire.go:333`.

**§25.9 No bare status event and no bare `created_at = now`.** §19.9, §17.2.

---

## 26. Mutation ↔ clause coverage (both directions)

**§26.1 Board mutation → clause.** Every mutation the approved board design can
perform maps to at least one clause:

| Board mutation | Command path | Events emitted | Clause |
|---|---|---|---|
| move column: → active | `rd claim` (`cmd/rd/claim.go:25`) | card + 1630 | §20.1 |
| move column: → waiting | `rd update --status waiting` / `rd gate` | card + 1630 | §20.4, §22.1 |
| move column: → done | `rd done` / `rd complete` | card + **1631** | §20.3 |
| move column: → cancelled/failed | `rd cancel` / `rd kill` / `rd fail` | card + **1632** | §20.3 |
| move column: → blocked | *not writable* — derived from `i` tags | card only | §21.4, §25.6 |
| claim | `rd claim` | card (`p`) + 1630 | §20.1 |
| reassign | `rd delegate` (`cmd/rd/delegate.go:36`) | card (`p`) + status re-affirm | §20.2 |
| close (with reason) | `rd done/fail/cancel --reason` | card + 1631/1632, reason in Content | §20.3, §19.3 |
| retitle | `rd update --title` | card only | §24.1, §24.4 |
| re-prioritise | `rd update --priority` | card only (`rank` + `priority`) | §24.1, §24.3 |
| add label | `rd label add` (`cmd/rd/label.go:315`) | card only (`l`) | §23.1, §23.3 |
| remove label | `rd label remove` (`:340`) | card only | §23.1 |
| open gate | `rd gate` (`cmd/rd/gate.go:38`) | card + 1630 | §22.1 |
| approve gate | `rd approve` (`cmd/rd/approve.go:26`) | card (gate tags dropped) + 1630 | §22.2 |
| reject gate | `rd reject` (`cmd/rd/reject.go:28`) | card + 1630 (`waiting → waiting`) | §22.3 |
| create item | `rd create` (`cmd/rd/create.go:179`) | board? + card + 1621? + 1630 | §18.8 |
| edit description | `rd update --context` / `rd progress` | card only | §24.1, §24.6 |
| set ETA / due / level | `rd update` / `rd defer` | card only | §24.1, §24.6 |
| add dep | `rd dep add` (`cmd/rd/dep.go:44`) | card only (`i`) | §21.1 |
| remove dep | `rd dep remove` (`cmd/rd/dep.go:65`) | card only | §21.1 |

**§26.2 Writer code → clause (no orphans).** Citation shorthand for this clause
only: a bare `:N` means `cmd/rd/nostrwrite.go:N`. Every mutation body in that
file, and every publish helper it uses:
`publishItemFullCreateNostr` (`:155`) §18.8; `runClaimNostr` (`:212`) §20.1;
`runCloseNostr` (`:234`) §20.3; `runDelegateNostr` (`:255`) §20.2;
`runGateNostr` (`:273`) §22.1; `runApproveNostr` (`:299`) §22.2;
`runRejectNostr` (`:327`) §22.3; `runDepAddNostr` (`:350`) §21.1;
`runDepRemoveNostr` (`:373`) §21.1; `runLabelAddNostr` (`:391`) §23.1;
`runLabelRemoveNostr` (`:406`) §23.1; `runUpdateNostr` (`:430`) §20.4 + §24.1 +
§24.7; `runCreateNostr` (`:504`) §18.8; `runEngageNostr` (`:560`) /
`publishEngagedItemsNostr` (`:602`) §26.3; `publishImplicitUnblockNostrNative`
(`:635`) §21.4; `publishItemStatusChangeNostr` (`cmd/rd/nostr.go:353`) §19.9;
`publishItemCardEditNostr` (`cmd/rd/nostr.go:389`) §18.9; `setCardEnvelope`
(`cmd/rd/confidential.go:230`) §16.7. The remaining non-mutating helpers in the
same file are dispositioned too, so nothing in it is an orphan:
`errNotNostrProject` (`:47`), `nostrNativeProject` (`:63`) and `nostrWriteActive`
(`:77`) → §16.4; `nostrResolveItem` (`:110`) → §16.3; `nostrSelfPubkey` (`:98`) →
§20.1; `nostrExistingIDs` (`:135`) → §18.8; `nostrReadActive` (`:88`) is a READ
gate (Part I, §1.1); `emitMutationResult` (`:197`) formats CLI output only and
writes no event (§22.4).

**§26.3 `rd engage` is N creates.** `publishEngagedItemsNostr`
(`cmd/rd/nostrwrite.go:602-628`) loops the expanded playbook items, building each
with `Status: state.StatusInbox` (`:618`) and `BlockedBy: ei.Deps` (`:620`), and
calls `publishItemFullCreateNostr` per item (`:622`). Deps between siblings work
regardless of publish order because the ids are pre-generated and the fold
resolves `i` tags after all items are known (§8.2). Nothing here is a new event
shape — §18.8 covers it entirely.

**§26.4 Not browser-reachable, dispositioned here so no writer is an orphan.**
`rd log publish` (`cmd/rd/nostr.go:488-569`) and `rd log publish --board`
(`runPublishBoard`, `:600-633`) are operator republish tools — the first
re-materializes one item's current state (re-deriving its reason from history,
`lastStatusReason`, `:578-585`), the second re-sends already-durable log events
verbatim without re-signing. `rd log put` (`:689-749`) is a demo/diagnostic
primitive that builds a `CardSpec` by hand. `rd grant`/`rd revoke`/`rd kill`
publish kind-39301 role grants (`cmd/rd/authz_nostr.go:45-76`) — authorization,
not item state, and specified as a READ input in §12. `rd sync` / `rd relay
flush` / `rd log merge-log` move existing events between stores and mint nothing
(§14.11).

---

## 27. Open questions — write side

Recorded, not resolved. No writer code was changed for any of these. Each needs a
ruling before the conformance vector suite can assert on the affected behaviour.
(§15 holds the read-side open questions.)

**§27.1 A member holding only a NEWER CEK epoch can silently overwrite free text
with the placeholder.** The read path renders `Title`/`Context` as
`placeholderText` when a confidential card cannot be decrypted
(`pkg/sync/nostrproject.go:612-620`), and `Labels` stay opaque tokens. The write
path builds its envelope from `CurrentEpoch` only
(`envelopeFromKeyring`, `cmd/rd/confidential.go:65-76`) and errors ONLY when the
writer holds no key at all (`:126-132`) — it never checks that the card it is
about to re-seal was actually decryptable. `DeriveBoardKeyring` accumulates epochs
from the grants present in the LOCAL LOG (`pkg/sync/keydist.go:145-186`), and
39301 grants are addressable per `(boardD, grantee)` slot
(`roleGrantD`), so a machine that populated its log from a relay after an epoch
rotation may hold only the newest grant. Such a member reads a pre-rotation card
as `[encrypted]` and, on ANY card edit (label add, dep add, retitle, close),
re-seals `title: "[encrypted]"` over the real title — signed, permanent,
latest-wins. **Question:** should the write path refuse a card edit whose current
projection contains `placeholderText`, or should grants carry every historical
epoch, or should `boardConfidentialEnvelope` verify decryptability of the card
being replaced? The comment at `cmd/rd/confidential.go:82-83` asserts "a
placeholder is never re-sealed over real content", which holds only for the
no-key-at-all case.

**§27.2 `rd update` has two status anomalies.** (a) A bare `rd update --claim`
publishes TWO status changes: the CLI sets `statusTo=active`
(`cmd/rd/update.go:47-50`), satisfying the status block
(`cmd/rd/nostrwrite.go:467-478`), and the claim block then publishes again
(`:480-486`) — two cards, two 1630 events, one at `T` and one at `T+1` (§17.7),
the second with an empty reason. (b) The terminal guard is field-conditional
(`:439-441`): `rd update --status active` on a `done` item is ACCEPTED and reopens
it, while `rd claim` (`:221-223`), `rd done` (`:239-241`), `rd gate` (`:278-280`)
and `rd progress` (`cmd/rd/aliases.go:192-194`) all refuse terminal items.
**Question:** is reopening via `--status` intended (an escape hatch), and should
`--claim` short-circuit the status block?

**§27.3 Two republish paths bypass the monotonic stamp.** `rd log publish
<item>` stamps `time.Now().Unix()` (`cmd/rd/nostr.go:549`) and `rd log put`
stamps it twice (`:726`, `:728`), instead of `nostrNextCreatedAt` (§17.2). A
republish issued in the same second as the live write it follows collides on
`created_at`, and §4.1's lowest-event-id tiebreak decides — so a manual republish
can silently LOSE to the state it was meant to refresh. `rd log put` additionally
builds its `CardSpec` with no `BoardAuthor` (`:714-722`, so the `a` tag is the
SIGNER's board, failing the pin gate for an agent key) and no envelope (so it
writes plaintext to a confidential board, which §25.3 quarantines). **Question:**
route both through `nostrNextCreatedAt` + `CardSpecFromItem` + `setCardEnvelope`,
or mark `rd log put` explicitly as an unsafe diagnostic?

**§27.4 The cross-board dep guard checks only one endpoint.**
`runDepAddNostr` tests `IsCrossCampfireRef` on the BLOCKED argument
(`cmd/rd/nostrwrite.go:351-353`) but never on the BLOCKER, and `runDepRemoveNostr`
tests neither. In practice `nostrResolveItem` fails first for an id not in this
projection, so the hole is not reachable today — but the guard does not say what
it appears to say. **Question:** extend the check to both arguments, or delete it
as redundant with resolution?

**§27.5 `rd label add` claims a registry it does not enforce.** The command help
says "The label must be registered in the project label registry"
(`cmd/rd/label.go:301`) but `runLabelAddNostr` (`cmd/rd/nostrwrite.go:391-402`)
validates nothing — no atom pattern, no registry membership — matching the read
side, which also validates nothing (§10.1, §15.3). **Question:** this is the
write-side half of §15.3; a ruling there must also fix or delete this help text.
Until ruled, a conformance vector MUST NOT assert that any label is rejected.

**§27.6 `Item.Project` is set at create time and never written to the wire.**
`runCreateNostr` populates `item.Project` (`cmd/rd/nostrwrite.go:537`) and
`publishEngagedItemsNostr` sets it from the project prefix (`:615`), but
`CardSpecFromItem` (`pkg/sync/nostrmigrate.go:106-127`) has no `Project` field and
`BuildCardEvent` emits no such tag — so the value is dropped on the first
publish and the fold always projects `Project=""`. The `--project` filter
(`cmd/rd/ready.go:117`) therefore matches nothing on a nostr project.
**Question:** add a `project` card tag, or delete the flag and the field from the
nostr surface? (This is distinct from §5.3's `CampfireID`, which is deliberately
never set.)

**§27.7 Card edits persist DERIVED status back into the `s` tag.** `blocked`
(§8.4) and the promoted `waiting` (§9.4) are computed by the fold and written
into `item.Status`; the next card-only edit copies that value verbatim into the
card's `s` tag via `CardSpecFromItem` (`pkg/sync/nostrmigrate.go:110`). It is
harmless while the item has an authoritative status event (§6.10 overrides it) and
while the derivation still holds — but it means the wire carries a value no
writer intended, and a client reading the card alone (a generic NIP-100 kanban
client, which knows nothing of §8) sees a status rd would recompute differently.
**Question:** should `CardSpecFromItem` write the last AUTHORED status instead of
the derived one? §7.6 already records the read-side half of this.

**§27.8 The status event's first `a` tag names the SIGNER's card coordinate.**
`BuildStatusEvent` builds `CardCoord(k.PubKeyHex(), itemID)`
(`pkg/sync/nostrwire.go:370`) — the SIGNER's, not the winning card author's.
Today the two always agree, because every live status event is published together
with a card signed by the same key (§19.9). They diverge for any client that
publishes a status event without republishing the card, and for a status event
authored by a maintainer about another author's item (§19.8), where the
coordinate points at a 30302 event that does not exist. The fold does not
currently follow that coordinate for authority (it uses the winning CARD's first
`a`, `pkg/sync/nostrproject.go:337`) and falls back to the `d` tag for item
resolution (`itemIDForEvent`, `pkg/sync/nostrwire.go:589-600`), so nothing breaks
today. **Question:** should the card coordinate be built from the winning card's
author, or should the clause simply forbid publishing a status event without its
card (§19.9)?
