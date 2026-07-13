---
title: rd nostr identity & trust model
status: DECIDED (adversarial synthesis; reconciled against dontguess trust model per docs/design/identity-reconciliation-ready-vs-dontguess.md, ready-434)
blocks: ready-f94
epic: ready-a14
design-workflow: wf_ef458857-18b
date: 2026-07-09
---

# DESIGN — rd nostr identity & trust model (pre-ready-f94)

**Status:** DECIDED. Author: Architect (adversarial synthesis). Blocks: ready-f94 (nostr-only flip).
**Governing invariant (epic ready-a14):** events are the source of truth; trust is a client-side projection of signed events, never a config flag or a flat allowlist.

**Trust invariant (adopted verbatim from the dontguess reconciliation, ready-434 §5.1):**
> Trust is an application-computed projection of signed events, enforced at the projection seam. The relay write-allowlist is only a coarse anti-spam admission — never the trust authority, and signature-validity is never admission.

This is the one-liner both ready and dontguess independently arrived at. See `docs/design/identity-reconciliation-ready-vs-dontguess.md` §3b, §5.1 for the joint derivation.

---

## 1. DECISION SUMMARY

- **Per-actor keys: YES, bounded to *durable* actors.** One owner key (the human trust root = the 30301 board author) plus one key per *named, long-lived* agent identity (`ceo-automaton`, a worker-pool identity), each at `$RD_HOME/keys/<actor>.json`. **Not** per-host (can't attribute "which agent"), and **not** per-ephemeral-process (that is the allowlist sprawl the ADVERSARY correctly fears). Sub-actor granularity finer than a durable key is carried by annotation, not a key. *Rationale: the owner/agent axis is structurally unrepresentable under one-key-per-host (§2); bounding keys to durable identities keeps both allowlists ~machine-sized.*
- **Delegation mechanism: a custom addressable `rd role-grant` event (kind 39301), owner-signed, latest-wins, revocable.** A direct port of `provenance/checker.go` onto nostr. **Rejected:** NIP-58 badges (recipient-controlled, no unilateral revoke — wrong control direction) and NIP-26 delegated-signing (per-event overhead, time-bound not revocable). The 30301 board `p`-list is **kept only as the level-2 bootstrap seed**, not the grant carrier. *Rationale: reuses `checker.go`'s proven 0/1/2 + latest-wins + creator-bootstrap semantics verbatim; only the transport changes.*
- **Operator level GATES writes (graded); humanness level ANNOTATES only.** Two orthogonal axes, never conflated. Operator level (actor's trust, 0/1/2) gates: revoked(0) → dropped at ingestion **and** removed from the relay allowlist; contributor(1) → may write item state; maintainer(2) → additionally authors status on others' items and rewrites `by`. Humanness/item level (`level` tag, ready-187) stays pure provenance annotation, untouched. *Rationale: this is exactly `checker.go`'s shape; binary trust is the regression we are undoing.*
- **Key + config storage: `$RD_HOME` (default `~/.config/rd`, XDG), git-ignored, 0600/0700.** Kills the `.cf` dependency outright. The `requireUnderCFHome` lexical `.cf`-name sniff is replaced by a resolved-path-under-`$RD_HOME` check plus `git check-ignore` defense-in-depth. *Rationale: post-flip `.cf` is a phantom directory, not a security boundary.*
- **Reconciliation: one signed source (role-grants) FEEDS all four built pieces.** ready-d53 `TrustSet` and the ready-266 relay `write-allowlist.json` are both **derived/regenerated** from `{pubkeys with non-revoked grant}` — ending the hand-maintained two-list drift. ready-b57 status-authority keeps its per-board-coordinate binding but its maintainer set becomes the derived level-2 grant set. The `by` tag is **demoted to migration-backfill only**. ready-187's `level` tag is unchanged.
- **Revocation is POINT-IN-TIME (prospective) by default, with an owner-signed retroactive "repudiate-from-T" escape hatch for compromise.** This resolves the ADVERSARY's #1 risk: a current-snapshot revoke would erase a departed key's past authoritative events and **reopen completed items**. Prospective revocation preserves the audit trail; repudiation-from-T contains a stolen key.
- **The authoritative board coordinate is PINNED in `.ready/config.json`; projection rejects cards whose `a` coordinate isn't the pinned board.** Closes the parallel-board self-escalation path (any relay-admitted key otherwise forks its own board and self-grants maintainer).
- **One correctness fix ships with this regardless of anything else:** the board-maintainer derivation at `nostrproject.go:151-159` is a monotonic union across all historical board events with **no latest-wins** — a maintainer once listed is a maintainer forever. This defeats "must be REVOCABLE" in live code today and must be replaced by latest-grant-per-(board,pubkey).

---

## 2. IDENTITY MODEL

**Actors and keys.**
- **Owner key** — the human root of trust. It is the author of the 30301 board; its board signature *is* its identity (self-certifying, `checker.go:98-105` creator-bootstrap ported: board author = implicit level 2, the trust anchor). Lives **offline only** (1Password per the OS pattern), never on an ephemeral host. It is the one backed-up secret; losing it means no admit/revoke/recovery, so it is not kept on cattle.
- **Agent keys** — one per *durable* agent identity (bounded, ~machine-count), each a fresh `GenerateKey()` (`key.go:24`) persisted via the existing O_CREATE|O_EXCL race-safe path (`key.go:170-210`) at `$RD_HOME/keys/<actor>.json`. `DefaultKeyPath`/`LoadOrCreatePortfolioKey` are parameterized by an actor id; `$RD_ACTOR` selects it, defaulting to `"owner"`.
- **Sub-actor attribution** finer than a durable key (which specific ephemeral worker) is carried by the `by` tag + an `actor-kind` annotation (human vs autonomous-agent), **zero allowlist cost** — because revocation granularity, not attribution granularity, is what a key must buy.

**Why not one-key-per-host (the ADVERSARY's counter-proposal):** correct that it bounds the allowlist, but it makes `ChangedBy = card.PubKey` (`nostrproject.go:178,253`) identical for every agent on the box — the owner/agent question the epic exists to answer is then structurally unanswerable. We take the bound-the-allowlist win a different way: keys are per *durable* actor (small, bounded like hosts) **and** both allowlists are *derived* from grants (§4), so sprawl cannot accrue by hand.

**Provisioning — generate-then-authorize.** An agent key is generated locally on first `rd` use (same bootstrap as today) but is **inert**: it can sign, but nothing it signs is honored until (a) the owner publishes a role-grant naming its pubkey and (b) `rd relay sync-allowlist` (§4) adds it to the relay write-allowlist. Two admission steps collapse into one signed act plus one mechanical regeneration.

**Rotation.** No special rotation event: publish `role=revoked` for the old pubkey (prospective — its past work stays valid), generate a new key, grant it the same role. Same primitive as revocation.

---

## 3. DELEGATION & ROLES

**Terminology: call this "owner-rooted bounded delegation," not "web-of-trust."** dontguess (`convergence-sybil-defense.md`) rejected CAG/web-of-trust for its *permissionless* mesh because the root set there is self-appointed — sybils can mint their own root and cross-vouch. ready's model differs on every property that rejection depended on: a single pinned unforgeable owner key (not self-appointed), depth-capped and self-escalation-blocked propagation (not transitive vouch decay), in a closed team-tier graph (not an open mesh). dontguess's rejection of web-of-trust therefore **supports** ready's design rather than contradicting it — the two are not the same shape. See `docs/design/identity-reconciliation-ready-vs-dontguess.md` §3a for the full ruling. Use "owner-rooted bounded delegation" (or just "delegation") in all ready docs and code comments going forward.

**Event: `rd role-grant`, kind `39301` (addressable / parameterized-replaceable, deliberately away from NIP-100's 30301/30302 to avoid collision).**

| Field | Value |
|---|---|
| `kind` | `39301` |
| `d` | `"<boardD>:<granteePubkeyHex>"` — one addressable slot per (board, grantee) ⇒ latest-wins per grantee for free |
| `p` | `<granteePubkeyHex>` — the subject of the grant |
| `a` | `30301:<ownerPubkey>:<boardD>` — binds the grant into the pinned board's authority chain |
| `role` | `owner` \| `maintainer` \| `contributor` \| `revoked` |
| `from` | *(optional)* effective-from unix seconds — **absent = prospective** (effective at `created_at`); present = retroactive repudiation from T (compromise case) |
| `content` | optional human label — replaces `write-allowlist.json`'s hand-kept `pubkey→label` map |
| signature | the **granting** key (owner, or a maintainer within the escalation cap) |

**Level mapping — `checker.go:34-46` verbatim:** `maintainer→2`, `contributor→1`, `revoked→0`, no-grant→1. "Owner" is **not** a 4th numeric level — it is the identity of the board author (the bootstrap level-2 trust root, `checker.go:102-104`). This keeps the port minimal while still letting the escalation cap distinguish "who may mint a maintainer."

**Granting.** The owner signs a 39301 event assigning a role to an agent pubkey. Latest grant per `(board, grantee)` wins via the **existing** `newerThan` tie-break (`nostrproject.go:392-397`) — no new convergence logic.

**Escalation cap (non-self-escalation — the twin of "must be REVOCABLE"):**
- Only the **board author (owner)** may sign a grant of `role=maintainer` (or `owner`).
- A **maintainer** (level 2, not the board author) may sign only `contributor` or `revoked`.
- A grant violating the cap is **ignored at derivation** — a signer can never grant above its own tier.
*Without this, one compromised maintainer key mints unlimited new maintainers — strictly worse than today's hand-edited flat list, which a key at least cannot self-expand.*

**Revocation and the ADVERSARY's revocation-race, addressed explicitly.**

Revocation = publish a newer `role=revoked` grant. Enforcement is at projection, and the semantics are **point-in-time, not current-snapshot** — this is the crux the ADVERSARY raised and it is ruled here:

- **Default (prospective / clean offboarding):** each pubkey derives an `authoritative-until` = `+∞` if not revoked, else the revoking grant's effective timestamp (`from`, else its `created_at`). An item event `E` authored by that key is honored iff `E.created_at < authoritative-until`. **A departed key's past authoritative events are NOT erased** — completed items do not reopen, history does not vanish. (Current code's `opts.trusts(e.PubKey)` at `:144` is a snapshot check; revoking there would erase all of the key's past events — the live data-integrity bug we are ruling out.)
- **Compromise (retroactive):** the owner publishes `role=revoked` with `from=T` chosen before the suspected compromise. All events from that key with `created_at ≥ T` are dropped, and `sync-allowlist` removes the key from the relay so it can publish nothing new. In the limit `T` = the grant's original time ⇒ full erasure (containment).
- **Back-dating residual (PERMANENT CONSTRAINT):** `created_at` is self-asserted, so a compromised key can back-date an event below `T`. The containment boundary is therefore **operator-chosen** (set `T` conservatively before any exposure; full repudiation = erase-all). This matches campfire's model — a compromised creator key was equally unrecoverable — and is documented, not silently assumed.
- **Race:** an attacker cannot forge a *newer* owner-signed grant (needs the owner key) and cannot self-escalate on its own board (maintainer authority is bound per board coordinate `30301:<author>:<d>` at `:104-106`, and the board is pinned — §4). So the only residual is back-dated replay of already-published events, bounded by `T`.
- **Same-second grant ordering (acceptable, documented):** the escalation cap is evaluated against levels replayed so far, in `(created_at, id)` order. A same-second maintainer-grant followed by a grant it authorizes can no-op via the `id` tie-break if the tie-break happens to order the dependent grant first. This is deterministic and fail-closed (the grant is simply not honored that replay, not honored-wrongly) — it is documented here as acceptable behavior, not a bug to fix. See reconciliation NOTE-B (`docs/design/identity-reconciliation-ready-vs-dontguess.md` §4).

---

## 4. TRUST DERIVATION

Trust stops being a stored list and becomes a **pure function of signed events**, computed each run.

**`DeriveLevels(events, boardAuthor) → (map[pubkey]int, map[pubkey]until)`** (new, ~80 LOC, the nostr port of `NewStoreChecker`):
1. Bootstrap: `boardAuthor` (owner) = level 2.
2. Replay all trusted+verified 39301 events for the **pinned** board coordinate in `(created_at, id)` order; apply the escalation cap per grant; latest per grantee wins.
3. Output the graded level map and each key's `authoritative-until` timestamp.

**Gate A — read-trust (ready-d53), was flat binary, becomes derived.** `ProjectOptions.Trusted` (`nostrproject.go:73`) is populated from `{ pubkey : level ≥ 1 }` ∪ self. The `Trusted[pubkey]` check at `:144` gains the point-in-time comparison (`E.created_at < until[pubkey]`). `rdconfig.Config.TrustedPubkeys` (`config.go:52`) is **demoted to a bootstrap/offline cache** — still parses (old `rd.json` must load), reconciled-from-events every run, never trusted over an event.

**Gate B — status-authority (ready-b57), keeps per-coordinate binding, source changes.** `maintainerSigners` (`nostrproject.go:188-196`) = `{ pubkey : level ≥ 2 }` for the card's board coordinate, unioned with `opts.Maintainers`. This also **fixes the union-never-revokes bug**: replace the monotonic union of historical board `p`-tags (`:151-159`) with latest-grant-per-(board,pubkey); keep the 30301 board `p`-tags only as the level-2 **bootstrap seed**.

**Gate C — humanness level (ready-187): unchanged, annotate-only.** The `level` card tag (`nostrwire.go:245`, `itemFromCard` `:428`) stays item provenance. Do not conflate with operator level — different tag, different axis.

**Relay write-allowlist (ready-266) stays binary/coarse, but its FEED is derived.** `write-allowlist.py`'s contract (JSON `{pubkey:label}`, mtime-reload, fail-closed) needs **zero code change** — it is levels-agnostic and is the coarse spam/DoS gate ("may this key write at all"). A new **`rd relay sync-allowlist`** command regenerates `write-allowlist.json` from `{ pubkeys with level ≥ 1 (non-revoked) }` (label from the grant's `content`) and prunes revoked keys. `rd.json` (as the derived trust cache) and the relay file now share **one source** — the drift the runbook warns about (`write-allowlist.py:33-36`, `relay-runbook.md:157-162`) is closed structurally, not by discipline.

**Board pinning.** The authoritative board coordinate is pinned in `.ready/config.json` (`SyncConfig`). Projection rejects any card whose `a` coordinate ≠ the pinned board — killing the parallel-board self-grant path.

**Named seam contract (maintained list — do not let a refactor silently drop one).** The reconciliation audit (`docs/design/identity-reconciliation-ready-vs-dontguess.md` §4) enumerates every point where an event becomes authoritative. Each MUST enforce signature-verify (V) + read-trust membership (d53), and the seams marked `(+BP)` additionally enforce board-pin and/or point-in-time revocation. This list is the contract; a future refactor that touches trust must re-derive it against this table, not silently narrow it:

1. Ingestion — relay reconcile (V + d53)
2. Ingestion — negentropy download (V + d53)
3. Ingestion — degrade-floor merge (V + d53)
4. Projection — card latest-wins membership (V + d53)
5. Projection — point-in-time / prospective revocation (+BP)
6. Projection — board pinning (+BP)
7. Projection — status-authority + grant fold (V + d53 + BP)
8. Projection — `by` provenance rewrite (gated on derived maintainer set)
9. Derivation — `DeriveLevels` board binding (V + escalation cap)
10. Allowlist regeneration — `sync-allowlist` (V + cap, no-lockout invariant)
11. Relay write-allowlist plugin (signature-verify first, fail-closed)
12. Migration (source is campfire/JSONL, never nostr — non-circular; projected-back events pass the same gates)
13. Dual-read (`RD_NOSTR_READ`) — same gate stack as primary read path
14. Readiness (`rd ready`) — both `ReconcileBoard` and `ProjectItems` gated

**Read-trust is now grant-derived.** Gate A above (ready-434 / GAP-1) closes the fidelity gap the reconciliation found: `Trusted` was fed only by hand-maintained `Config.TrustedPubkeys`, not by `DeriveLevels`. It is now wired as `DeriveLevels(level ≥ 1) ∪ self`, so a freshly-granted contributor's events are admitted at ingestion without a manual `rd.json` edit. Any change to a seam in the list above must preserve this: read admission is always grant-derived, never hand-maintained-only.

---

## 5. KEY & CONFIG STORAGE

**`$RD_HOME` resolution** (new `RDHome()`, mirroring `CFHome()`'s cascade at `root.go:162-191` but rd-native): `--rd-home` flag → `$RD_HOME` env → walk-up for a repo-local `.rd/` marker (preserves the per-worktree identity isolation `cfHomeWalkUp` gives today) → default `~/.config/rd` (XDG). `.rd/` is added to `.gitignore`.

Repointed call sites (both already take a home-dir string — low blast radius): `nostr.DefaultKeyPath(RDHome())` and `rdconfig.Path(RDHome())`.

**Guard change.** Replace the lexical `.cf`-ancestor sniff (`requireUnderCFHome`, `key.go:94-110`) with:
1. resolved-path check: `filepath.Clean(abs)` must be under `filepath.Clean(RDHome())` — one canonical root, which kills both the symlink-name TOCTOU (`key.go:90-93`) and the ADVERSARY's foreign-repo `.cf` leak (a `.cf` dir in a repo that doesn't ignore it currently passes the guard and commits the secret);
2. defense-in-depth: refuse a path inside a git work tree unless `git check-ignore` confirms it is ignored.

**Migration — identity-preserving COPY, never regenerate** (resolving the ADVERSARY's #2 catastrophic-silent-failure risk; overriding the PURIST's "move" for rollback safety):
- On first post-flip run, if `$RD_HOME/nostr-identity.json` is absent but `~/.cf/nostr-identity.json` (or the legacy `~/.campfire/…`, `root.go:187`) exists, **copy** the secret + `rd.json` into `$RD_HOME` (0600), using the existing O_EXCL pattern so concurrent first-runs converge. **Never call `GenerateKey` on the migration path** — a regenerated pubkey is absent from the relay allowlist (every write rejected), absent from every peer's trust set (events dropped), and is not the author of any existing event (self-authorship + status-authority break), all silently.
- Leave the `~/.cf` originals in place for one deprecation window (rollback), write only to `$RD_HOME` thereafter. Campfire's dead ed25519 `identity.json` is left behind (not migrated).
- **Startup assertion:** after load, verify the loaded pubkey is in the local derived trust set / allowlist; if not, warn loudly (this is the tripwire for a botched or regenerated identity).
- `rd migrate-home --dry-run` for explicit operator control. Keys are per-machine, so each host migrates independently — no cross-machine coordination.

---

## 6. RECONCILIATION

| Built piece | Before | After |
|---|---|---|
| **ready-266** relay write-allowlist (`write-allowlist.py`) | hand-edited `{pubkey:label}`, kept consistent with `rd.json` **by hand** | **plugin unchanged**; file **regenerated** by `rd relay sync-allowlist` from `{level ≥ 1, non-revoked}`. Stays binary/coarse (spam gate), fail-closed intact. |
| **ready-b57** board maintainers / status-authority (`nostrproject.go:151-196`) | monotonic union of **all historical** board `p`-tags — **never revocable** (the live bug); per-coordinate binding correct | maintainer set = **latest-grant-per-(board,pubkey) at level 2**; per-coordinate binding **kept**; board `p`-tags demoted to bootstrap seed. Revocation now works. |
| **ready-d53** `TrustSet` (`config.go:65-76`) | flat binary union of `TrustedPubkeys` + self | **derived** = `{level ≥ 1}` ∪ self, with point-in-time `authoritative-until`. `TrustedPubkeys` demoted to bootstrap cache (still parses). |
| **ready-187** `level` tag (`nostrwire.go:245`, `itemFromCard:428`) | per-item humanness annotation | **unchanged.** Explicitly kept separate from operator level. |
| **`by` provenance tag** (`nostrproject.go:253-256`) | rewrites `ChangedBy`, honored from board maintainers — the compensator for one-key-per-host | **demoted to migration-backfill only.** Live self-writes make `ChangedBy = s.PubKey` cryptographically true (per-actor keys). `by` retained for keyless historical actors; ignored on new events. The gate that honors it repoints onto the derived (revocable) maintainer set. |

**Backward compatibility:** kind 39301 is purely additive. With zero grants present, `DeriveLevels` falls back to the bootstrap rule (board author = level 2), so the already-published 1565 migrated items need **no re-migration** and the parity demo (`docs/nostr-migration.md`, matched=1565) re-passes unchanged.

---

## 7. ADVERSARY ATTACKS

| # | Attack / risk | Disposition |
|---|---|---|
| A1 | **Retroactive-revocation erases past authoritative events** → completed items reopen (`opts.trusts` is a snapshot, `:144`) | **RESOLVED** — revocation ruled **point-in-time/prospective** (§3): event honored iff `created_at < authoritative-until`; past work preserved. Compromise handled by owner-signed `from=T` repudiation. |
| A2 | **Silent migration key-regeneration** breaks relay allowlist + self-authorship, portfolio-wide, no error | **RESOLVED** — migration is identity-preserving **copy**, never `GenerateKey`; originals kept for rollback; startup assertion that loaded pubkey ∈ trust set (§5). |
| A3 | **Two-allowlist drift** (`rd.json` vs `write-allowlist.json`) kept consistent by hand | **RESOLVED** — both derived from one signed source; `rd relay sync-allowlist` regenerates the relay file (§4). |
| A4 | **Board-maintainer union never revokes** (`:151-159`) — maintainer listed once is maintainer forever | **RESOLVED** — replaced by latest-grant-per-(board,pubkey); board `p`-tags become bootstrap seed only (§4, §6). |
| A5 | **Unpinned board self-escalation** — any relay-admitted key forks its own 30301, self-grants maintainer, publishes cards under its own `a` | **RESOLVED** — board coordinate **pinned** in `.ready/config.json`; projection rejects foreign-`a` cards (§4). |
| A6 | **Lexical `.cf`-name guard is false safety** — a `.cf` dir in a repo that doesn't ignore it commits the secret; symlink-name TOCTOU | **RESOLVED** — guard replaced by resolved-path-under-`$RD_HOME` + `git check-ignore` (§5). |
| A7 | **`cfHomeWalkUp` breaks post-flip** — walk-up keys on campfire's `identity.json`, which stops being written ⇒ worktrees collapse to global home | **RESOLVED** — `RDHome()` walk-up keys on a `.rd/` marker instead (§5). |
| A8 | **Per-actor key sprawl** — N ephemeral agents ⇒ N relay + N trust entries, dead keys accumulate, chicken/egg provisioning | **RESOLVED (scoped)** — keys are per **durable** actor (bounded ~host-count), not per-process; sub-actor detail via `by`/actor-kind annotation; both lists **derived** so growth is mechanical + prunable (§2, §4). |
| A9 | **Maintainer self-escalation** — a compromised maintainer mints new maintainers | **RESOLVED** — escalation cap: only the board author grants maintainer; maintainers grant only contributor/revoked; violating grants ignored at derivation (§3). |
| A10 | **Owner key single point of failure** — lose it → no recovery; compromise → attacker regrants everything | **PERMANENT CONSTRAINT** — inherent to any rooted delegation chain. Mitigated: owner key offline-only (1Password), never on cattle, the one backed-up secret (§2). Documented, not eliminated. |
| A11 | **Back-dated event replay** under point-in-time revocation (`created_at` self-asserted) | **PERMANENT CONSTRAINT** — containment boundary is operator-chosen via `from=T`; full repudiation = erase-all. Same bound campfire had. Documented (§3). |

---

## 8. BUILD PLAN

Five outcome-scoped items, all landing **before ready-f94**. Minimum viable: port `checker.go`'s graded model onto nostr; reuse `newerThan`, `GenerateKey`, O_EXCL, and the `checker.go` level map verbatim. No campfire machinery rebuilt.

**BP-1 — `$RD_HOME` replaces `.cf` for identity + config; existing installs migrate identity-preservingly.**
*Done:* `rd` reads/writes its key and `rd.json` under `$RD_HOME` (default `~/.config/rd`) with **no `.cf` dependency**; the guard rejects any secret path not under `$RD_HOME` (verified by a test pointing at a foreign `.cf`); an existing `~/.cf` install auto-**copies** its key+config forward on first run with the **same pubkey** (asserted in-test), originals left intact; `rd migrate-home --dry-run` prints the plan.
*Touches:* `pkg/nostr/key.go` (`requireUnderCFHome`→resolved-path guard, `DefaultKeyPath`), `pkg/rdconfig/config.go` (`Path`), new `RDHome()` in `cmd/rd/root.go`, `.gitignore`. Independent of BP-2..5.
*Deps:* none. **Blocks ready-f94 regardless of the rest.**

**BP-2 — Graded levels are derived from signed role-grant events (pure function, unit-tested).**
*Done:* `DeriveLevels(events, boardAuthor)` returns the correct `{pubkey→level}` + `authoritative-until` map for a set of 39301 events, matching `checker.go` semantics (latest-wins, revoked=0, bootstrap owner=2), **enforcing the escalation cap** (a maintainer-signed `maintainer` grant is ignored) and **prospective revocation** (a revoked key's `authoritative-until` = revoke time; `from=T` overrides). Wire build/parse for kind 39301 exists. No CLI/relay wiring yet.
*Touches:* new `pkg/sync/rolegrant.go` (build/parse, reuse `newerThan`), new `DeriveLevels` (port of `pkg/provenance/checker.go`). Pure functions + tests.
*Deps:* none (parallel with BP-1).

**BP-3 — Revocation actually takes effect at projection, and no completed item reopens when a past author is later revoked.**
*Done:* replaying a board republished without a maintainer `p`-tag **drops** that maintainer's status-authority; a fresh `role=revoked` grant removes a key from read-trust for events after the revoke; a **completed item does NOT reopen** when its past author is revoked (prospective); the board-maintainer union bug at `nostrproject.go:151-159` is gone. Proven by replay tests.
*Touches:* `pkg/sync/nostrproject.go` (`:144` point-in-time gate via BP-2's `until`; `:151-196` maintainer set from `DeriveLevels`; foreign-`a` card rejection against the pinned board), `.ready/config.json` board-coordinate pin.
*Deps:* BP-2.

**BP-4 — Owner and a named agent on one host sign with distinct keys and are attributed distinctly.**
*Done:* `$RD_ACTOR=agent:pm rd …` signs with `$RD_HOME/keys/agent-pm.json`, distinct from `owner`; `rd show` attributes each transition to the acting key; an existing single-key install's key becomes the `owner` actor with **zero migration**; board author = owner bootstrap holds.
*Touches:* `pkg/nostr/key.go` (parameterize `DefaultKeyPath`/`LoadOrCreatePortfolioKey` by actor id), `cmd/rd` (`$RD_ACTOR` selection, default `owner`).
*Deps:* BP-1.

**BP-5 — One signed act admits/revokes an actor across BOTH the client trust set and the relay allowlist.**
*Done:* `rd grant <pubkey> <role>` / `rd revoke <pubkey>` publishes a 39301 grant; `rd relay sync-allowlist` regenerates `write-allowlist.json` from `{level ≥ 1, non-revoked}` (pruning revoked keys), and the derived `TrustSet` matches — verified by a demo that grants an agent, watches its first write land, revokes it, and watches the next write get rejected at the relay. Foreign-board cards are rejected (from BP-3).
*Touches:* new `cmd/rd` grant/revoke/sync-allowlist commands, `scripts/relay-policy/` regeneration hook, `docs/relay-runbook.md` (replace the manual-sync section).
*Deps:* BP-2, BP-3, BP-4.

**Order:** BP-1 ∥ BP-2 → BP-3, BP-4 → BP-5 → **ready-f94**. Re-run the 1565-item parity demo after BP-3 to prove no regression before the flip.

---

## 9. TIER SCOPING (permanent — read before proposing sybil/economic defenses)

**ready is permanently and only a closed team-tier system.** There is no global/permissionless tier, no marketplace, and none is planned. This is not an oversight to fix later — it is a structural property of the model in §2-§4: a minted key is **inert until the owner grants it a role**. Nothing a key signs is honored, admitted to the relay, or counted for anything until an owner-signed (or capped-maintainer-signed) 39301 grant names it. Because ready never counts *keys* as a trust signal — it counts only owner-rooted grants — the entire "N independent identities converge" attack surface that a permissionless/marketplace tier must defend against **does not exist here**.

Consequently, the following are **out of scope by construction** and MUST NOT be built for ready:

- **Reputation floor** — there is nothing to float a reputation score against; trust is binary-gated by grant, not accumulated by behavior.
- **Proof-of-cost / sat-burn** — burn-to-mint-identity defenses exist to make sybil identity minting expensive on a *permissionless* mesh. ready's mesh is not permissionless; minting a key is free and harmless because the key is inert until granted.
- **Scrip / marketplace economics** — ready has no marketplace, no sellers, no auto-accept promotion gate to protect with an economic cost.
- **Weighted-convergence sybil math** (K_eff, clique-recurrence detection, PAC bounds) — this machinery defends against sybils *counting* toward a convergence threshold. ready has no convergence threshold; a hundred sybil keys contribute exactly zero trust until the owner grants one of them a role, at which point it is one identity the owner chose to trust, not a convergence signal.

**Authority for this scoping:** dontguess's own design (`convergence-sybil-defense.md`, per the reconciliation `docs/design/identity-reconciliation-ready-vs-dontguess.md` §1, §5 item 3) declares the flat-allowlist team tier **already solved by allowlisted identities** — the sybil/economic machinery in that document is explicitly scoped to dontguess's *global* and *marketplace* tiers, neither of which ready has. ready's sybil payoff is structurally zero because keys are inert until owner-granted (§2 above); the convergence/economic apparatus dontguess built for its open tiers has no problem to solve in ready's closed one.

**What this tells future maintainers:** if a proposal surfaces to add reputation scoring, proof-of-work/burn admission, an internal economy, or convergence-counting to ready's trust model, the default answer is **no** — re-derive from this section and the reconciliation doc before building it. If ready ever grows a genuinely open/permissionless tier (not currently planned), that would be a new architecture decision requiring its own design doc, not an incremental addition here.