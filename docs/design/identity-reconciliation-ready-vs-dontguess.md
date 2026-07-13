---
title: Trust-model reconciliation — ready ↔ dontguess (owner/agent identity)
status: DECIDED (Opus adversarial design pass, security-laden)
epic: ready-a14
tracking: ready-434
design-workflow: wf_c5ffa62e-c4d
date: 2026-07-09
---

# Trust-Model Reconciliation: dontguess ↔ ready

**Audience:** ready maintainers
**Author:** Architect (synthesis of three grounding analyses: dontguess identity/trust model, ready trust-seam audit, threat-model reconciliation)
**Status:** Formal ruling. Decisive.

---

## 1. SUMMARY

- **ready does not need dontguess's anti-sybil machinery, and this is structural, not a gap.** dontguess counts *independent identities* as a trust signal ("3+ independent agents converged," `convergence-sybil-defense.md:16,18`); ready **never counts keys as trust**. In ready a minted key is inert until owner-granted (`nostr-identity-model.md:39`), so the entire convergence-sybil attack surface is absent by construction.
- **The two systems share one load-bearing spine:** trust is an **application-computed projection of signed events, enforced at the promotion/projection seam** — the relay write-allowlist is only a coarse anti-spam gate, and signature-validity is never admission. Both arrived at this independently (dontguess `nostr-admission-scrip-rehome-3b8.md:28,43`; ready `nostrproject.go:144`).
- **What transfers, directly:** the multi-seam `TrustChecker` discipline (dontguess's four engine seams ↔ ready's projection/status-authority/board-pin seams), and the "derive the allowlist from one signed source" rule.
- **What does NOT transfer:** reputation floor, proof-of-cost sat-burn, scrip, and all global-tier weighted-convergence math (K_eff, clique-recurrence, PAC). Each is scoped to dontguess's *global/permissionless* tier or its *marketplace* economics — neither of which ready has or will have.
- **ready is permanently and only the "team" tier** — the tier dontguess itself declares *already solved by allowlisted identities* (`convergence-sybil-defense.md:8,93–95`). This tier fact is the single most important import: it scopes out most of dontguess's design.
- **ready's owner-rooted graded delegation is NOT web-of-trust and is NOT a mistake.** dontguess rejected CAG because a permissionless mesh's root set is self-appointed; ready's root is a single pinned unforgeable owner key with depth-capped, self-escalation-blocked delegation. dontguess's rejection *supports* ready's design rather than contradicting it. Stop calling it "web-of-trust"; call it **owner-rooted bounded delegation**.
- **The seam audit is clean on security.** 14 of 14 authoritative seams enforce signature-verify + read-trust membership (+ board-pin/point-in-time where applicable). No hostile-admission bypass, and critically **no dontguess-style in-memory-reset bug** (ready's trust is recomputed per projection, never a persisted "validated" flag).
- **Two real deviations found — both design-fidelity, both fail-closed (safe to ship):** GAP-1 (read-trust set is fed by hand-maintained config, not derived from grants) and GAP-2 (grants bind by board *owner* only, not the full board coordinate). Neither is externally exploitable; both mean design claims are only partially realized in code.

**Bottom line: ready's model is already sound. The transfer is mostly discipline and documentation, plus closing two fail-closed fidelity gaps.**

---

## 2. THE TWO MODELS SIDE-BY-SIDE

| Dimension | dontguess | ready |
|---|---|---|
| **Key structure** | Three roles (operator / fleet-member / anonymous buyer), all secp256k1/schnorr. Per-agent keys are the **measurement substrate** — K distinct signers = convergence count (`exchange-per-agent-identity-decision.md:13-23`) | Per-**durable-actor** secp256k1 keys under `$RD_HOME` (`key.go:352,408`). Keys are **attribution + revocation handles**, never a counted signal; sub-actor detail by annotation, not a key (`nostr-identity-model.md:19,34–35`) |
| **Trust source** | npub allowlist (`Membership` iface) + operator write-authority + reputation floor, composed in one `TrustChecker` (`trust.go:324`) | Projection of signed kind-39301 grants: `DeriveLevels` (`rolegrant.go:297`), owner-rooted. Read-*membership* fed by `Config.TrustedPubkeys` (`config.go:65`) — see GAP-1 |
| **Enforcement locus** | Client-side; **auto-accept promotion gate is the choke** (`engine_pricing.go:251-267`), + 3 defense-in-depth seams (dispatch, de-allowlist, reload) | Client-side projection: read-trust at `nostrproject.go:191`, status-authority `:260-288`, board-pin `:227`, point-in-time `:202` |
| **Graded levels** | 3 tiers: `TrustAnonymous(0)/TrustAllowlisted(1)/TrustOperator(2)` (`trust.go:50-58`) | 3 levels: 0 / contributor(1) / maintainer(2), owner=2 bootstrap (`rolegrant.go:297`) |
| **Revocation** | Runtime `DeAllowlistSeller` → `RemoveMember` + flag-for-revalidation + rebuild index (`engine_core.go:596-607`); reload re-gate (`engine_index.go:25-28`) | Prospective via `authoritative-until` (`rolegrant.go:359-370`); `until` gate keeps past events, drops future (`nostrproject.go:202`). Allowlist removal needs an explicit `role=revoked` (`allowlist.go:105`) |
| **Sybil resistance** | Flat allowlist = sole anti-poisoning primitive at team tier; burn/K_eff/PAC ratified-but-**unbuilt** for global tier (`convergence-sybil-defense.md`) | **N/A by construction** — keys inert until granted; sybil payoff is structurally zero (`nostr-identity-model.md:39`) |
| **Tiers** | Individual / Team / Enterprise / Global; enforcement model ships at **Team** (`nostr-first-rebuild-decision.md`) | **Team only, permanently.** No global tier exists or is planned |

---

## 3. JUSTIFIED DIVERGENCES

Three places where ready deliberately diverges from dontguess. Each is ruled explicitly.

### 3a. ready uses owner-rooted graded delegation; dontguess rejects web-of-trust (CAG). **RULING: ready is CORRECT.**

dontguess rejected CAG because on a *permissionless* mesh the root set is **self-appointed** — three sybils cross-vouching mint their own root for ~$30-65, a "trusted intermediary reintroduced one layer down" (`convergence-sybil-defense.md:56-61,91`; codified `trust.go:4-6`). That rejection is specific to three properties of CAG-on-open-mesh, and ready differs on all three:

- **Root:** CAG = self-appointed root on open mesh. ready = a **single pinned offline unforgeable owner key**; root *fabrication* is impossible (`nostr-identity-model.md:33,77`, foreign-`a` cards rejected `nostrproject.go:227`).
- **Propagation:** CAG = transitive `0.5^hops` vouch decay. ready = **depth-capped, self-escalation-blocked** delegation — a maintainer may grant only contributor/revoked, never another maintainer; violating grants ignored at derivation (`nostr-identity-model.md:64–68`, `signerMayGrant` `rolegrant.go:381`). No transitive privilege inflation.
- **Tier:** CAG's break is global-tier-specific; ready is closed team-tier where "allowlisted identities make independence real" (`convergence-sybil-defense.md:95`).

The thing dontguess rejected (fabricable, self-rooted, transitively-propagating vouch graph) is **not the thing ready built** (unforgeable-rooted, depth-capped, revocable delegation tree in a closed graph). ready independently rejected the recipient-controlled/transitive variants too — NIP-58 badges and NIP-26 delegated-signing (`nostr-identity-model.md:20`). **dontguess's CAG rejection supports ready's design — both refuse self-appointed roots.** Maintainers must stop labeling this "web-of-trust"; the correct name is **owner-rooted bounded delegation**, the right enrichment of a team-tier flat allowlist for a multi-actor work-graph.

### 3b. ready keeps a relay write-allowlist; dontguess rejects relay-allowlist-as-gate. **RULING: SHARED, not a divergence.**

Both treat the relay write-allowlist as a **coarse anti-spam admission, never the trust authority**. dontguess: "NIP-42 secures the pipe, not the operation… re-home the full trust model into the engine, NOT delegate to the relay write model" (`nostr-first-rebuild-decision.md:181`; `nostr-admission-scrip-rehome-3b8.md:14,28`). ready: relay write-allowlist "stays binary/coarse (spam gate)"; real graded trust is a client-side projection at the app seam (`nostr-identity-model.md:96,13`). ready's `write-allowlist.py` is explicitly documented as defense-in-depth beneath the client gate, fail-closed on empty/malformed. **This is verbatim-in-spirit agreement, not a divergence.**

### 3c. ready adds graded levels on top of a flat allowlist; dontguess's team tier is flat (weight ≡ 1.0). **RULING: ready's enrichment is CORRECT and tier-appropriate.**

dontguess's marketplace needs only admitted/not-admitted for sellers. A work-graph needs differentiated write scopes: a contributor writes item state; a maintainer authors status on *others'* items and rewrites `by`-provenance (`nostr-identity-model.md:21`). A flat "admitted/not" cannot express that; the graded 0/1/2 level is a correct addition dontguess's domain didn't require. Both still **derive** the list from one signed source (`sync-allowlist` ↔ `Config.FleetAllowlist`), which is the shared discipline.

---

## 4. TRUST-SEAM COMPLETENESS AUDIT (ready)

**Threat:** a permissive/hostile relay or foreign committed log serves validly-signed events from a **non-admitted key**, attempting to poison projected work-item state or the relay allowlist. Three gates must stand where an event becomes authoritative: **(V)** schnorr `Event.Verify`; **(d53)** read-trust membership; **(BP-3)** board-pin + point-in-time + status-authority.

| # | Seam | Verdict | Evidence (file:line) |
|---|---|---|---|
| 1 | Ingestion — relay reconcile | **COVERED** | V `nostrinbound.go:109`, d53 `:117`; prod callers pass non-nil trust set (`nostr.go:384,404`) |
| 2 | Ingestion — negentropy download | **COVERED** | V `negentropy.go:212`, d53 `:215`; NIP-77 authors filter re-checked client-side `:200-204` |
| 3 | Ingestion — degrade-floor merge | **COVERED** | V `nostrlog.go:238`, d53 `:243`; closes "foreign committed log" vector. `AppendUnique` = single choke for all 3 inbound paths |
| 4 | Projection — card latest-wins membership | **COVERED** | V `nostrproject.go:183`, d53 `:191`, dedup `:180` |
| 5 | Projection — point-in-time / prospective revocation | **COVERED** | `nostrproject.go:202`; no-grant keys unbounded (correct); revoked key's future events dropped, past survive (A1) |
| 6 | Projection — board pinning | **COVERED** | `nostrproject.go:227`; foreign-board cards rejected (kills parallel-board self-escalation A5); prod pins at every call site (`nostr.go:411,597,884`) |
| 7 | Projection — status-authority + grant fold | **COVERED** | `nostrproject.go:280-288,260-266`; A4 "union never revokes" fixed via `winningBoard` `:212,246-251`; status double-gated (passes V+d53+until before `statusEvents`) |
| 8 | Projection — `by` provenance rewrite | **COVERED** | `nostrproject.go:346`; `by` honored only when `maintainerSigners[s.PubKey]`; falls back to signer |
| 9 | Derivation — `DeriveLevels` board binding | **COVERED (forgery)** / **GAP-2** | V `rolegrant.go:327`, cap `:352`, owner-binding `:336` — but binds by owner only |
| 10 | Allowlist regeneration — `sync-allowlist` | **COVERED** | V + cap on `log.ReadAll()` input (`nostr_grant.go:348`); no-lockout invariant `allowlist.go:105`; hostile event can't add self nor evict live key |
| 11 | Relay write-allowlist plugin | **COVERED** | strfry verifies BIP-340 first (`write-allowlist.py:9-17`); fail-closed on empty/malformed `:38-41,80-94`; mtime-reload |
| 12 | Migration | **COVERED** | Source is campfire/JSONL never nostr (`nostr.go:951`, non-circular); projected-back events pass same gates |
| 13 | Dual-read (`RD_NOSTR_READ`) | **COVERED** | Routes through `ProjectItems` with Trusted+PinnedBoard (`nostr.go:884`) — identical gate stack |
| 14 | Readiness — `rd ready` | **COVERED** | `ReconcileBoard` + `ProjectItems` both gated (`nostr.go:579,595-597`) |
| 15 | In-memory trust-cache reset (dontguess `NeedsRevalidation` analog) | **CONFIRMED ABSENT — no bug** | `DeriveLevels` pure, re-run per `ProjectItems` (`nostrproject.go:126-130`); config reloaded per command (`config.go:84`). No persisted "validated" flag |

### Defects (both fail-closed — safe to ship, but they are real design-fidelity deviations)

**GAP-1 — Read-trust set is NOT derived from grants. Severity: LOW (fail-closed) / design-fidelity HIGH.** Design §4/A3 mandates `Trusted = {level≥1} ∪ self` from "one signed source." In code, the membership gate (`opts.trusts` `nostrproject.go:191`; `reconcile:117`; `admitDownloaded:215`; `MergeFrom:243`) is fed *only* by hand-maintained `Config.TrustedPubkeys` (`config.go:65`, `nostr.go:93`). `DeriveLevels`/`DeriveAllowlist` feed the *relay* file and the status-authority fold but **never the read-trust set** (grep-confirmed). Consequences: (a) a freshly-granted contributor absent from `rd.json` has its events **dropped at ingestion** — grant alone is insufficient for read admission; (b) the "two lists derived from one source" claim is half-true — relay file derives from grants, client read-trust does not, so they can still drift by hand. Direction is safe (strict allowlist), so not exploitable. **Fix: wire `DeriveLevels(level≥1)` into `Trusted`, OR explicitly document `TrustedPubkeys` as the retained separate read-membership feed.**

**GAP-2 — `DeriveLevels` binds grants by board OWNER only, not full boardD. Severity: LOW.** `deriveGrants` filters on `g.BoardOwner != boardAuthor` (`rolegrant.go:336`); `ProjectItems` calls `DeriveLevels(events, owner)` discarding the pinned boardD (`nostrproject.go:127-128`). A grant on `30301:<owner>:<any-other-boardD>` is honored on the pinned board — cross-board grant bleed, contradicting design §3 "bound per board coordinate `30301:<author>:<d>`." The code comment admits it (`rolegrant.go:278-279`). Not an external escalation (still needs an owner-signed grant; owner sigs unforgeable). **Fix: pass boardD into `DeriveLevels`, match the full coordinate.**

### Notes (operator-dependent, not code gaps)

- **NOTE-A — Prospective-revocation footgun.** A1's "completed items don't reopen" holds only while a revoked key **stays** in `TrustedPubkeys`. If an operator also strips it from `rd.json`, the membership gate drops *all* its events, reopening completed items. Design says "don't un-list, publish `role=revoked`," but nothing enforces it. Interacts with GAP-1 — correct revocation currently requires touching two things.
- **NOTE-B — Same-second grant ordering.** `signerMayGrant` evaluates against levels replayed so far (`rolegrant.go:286-287,352`); a same-second maintainer-grant + downstream grant can silently no-op via id tie-break. Deterministic, fail-closed. Document or seed cap-evaluation with the final level map.

---

## 5. TRANSFERABLE LESSONS

ready already embodies most of dontguess's hard-won discipline. Three concrete adoptions remain:

1. **Client-enforced trust as the boundary — codify it as an explicit invariant.** Both systems learned that **signature-validity is never admission** and the relay allowlist is a coarse spam gate only (dontguess `nostr-admission-scrip-rehome-3b8.md:14-16,28,43`; ready `nostrproject.go:144`). ready enforces this in code but the doctrine is scattered. Adopt the shared one-liner verbatim into `nostr-identity-model.md`: *"Trust is an application-computed projection of signed events, enforced at the projection seam. The relay write-allowlist is only a coarse anti-spam admission — never the trust authority, and signature-validity is never admission."*

2. **Multi-seam enforcement discipline — name ready's seams explicitly, as dontguess names its four.** dontguess documents WHY it needs a *promotion-gate choke* + 3 defense-in-depth seams: the fold applies state before any gate runs, so gating the general path is not enough (`nostr-admission-scrip-rehome-3b8.md:32-43`). ready's equivalent seams (ingestion `AppendUnique`, projection latest-wins, status-authority fold, board-pin, point-in-time) are all COVERED but not enumerated as a named contract. Write ready's seam list into the design doc so a future refactor cannot silently drop one — the audit above (§4) is the source.

3. **Closed-tier documentation — state the tier fact and its consequences.** dontguess's single most valuable export is the *tier scoping ruling*: "team solved by allowlisted identities; global machinery is YAGNI" (`convergence-sybil-defense.md:5,8,93-95`). ready is **permanently and only team tier**. Document this explicitly, plus its consequences: no reputation floor, no proof-of-cost burn, no scrip, no weighted convergence — **because keys are inert until granted, sybil payoff is structurally zero** (`nostr-identity-model.md:39`). This tells future maintainers *what not to build* and pre-empts "should we add sybil defense?" churn.

---

## 6. SHARED-RELAY RECONCILIATION (ready-677 context)

Two projects with **divergent trust philosophies** may sit on shared relays. The reconciliation is clean because both already agree the **relay is a dumb pipe**:

- **No shared-relay coordination is required for correctness.** Both systems do **client-side re-verification of every read** (dontguess `nostr-first-rebuild-decision.md:178-181`; ready seams §4). A relay that carries both projects' events cannot leak trust between them: ready drops any author absent from its own `TrustedPubkeys` at ingestion (`nostrinbound.go:117`); dontguess drops any non-admitted seller at its promotion gate. Neither trusts the relay's write-allowlist as authority. Cross-project event bleed is filtered at each reader's projection seam, not at the wire.
- **The one thing to coordinate is the relay write-allowlist union.** If a single strfry instance serves both, its write-allowlist must contain **both projects' admitted keys** (ready's grant-derived allowlist ∪ dontguess's `FleetAllowlist`). This is a coarse spam gate only; over-admitting there is harmless because both re-verify client-side. Ensure neither project's `sync-allowlist` regeneration **overwrites** the other's keys — the file must be a union, or the two projects must use separate write-allowlist files / separate relays. This is the single operational hazard.
- **Kind separation is already clean.** ready uses kinds 30301 (board)/card/39301 (grant); dontguess uses its own operator/put/match kinds. No kind collision, so a shared relay's event stream is trivially partitionable by each reader.

**Recommendation for ready-677:** prefer **separate write-allowlist files per project** on a shared relay (simplest, zero cross-contamination risk). If a single union file is unavoidable, add a regeneration guard so `rd relay sync-allowlist` merges rather than replaces. No trust-model coordination beyond this — the shared spine (client-side re-verification) makes the projects safely co-tenant.

---

## 7. WORK ITEMS

The audit found **two fail-closed design-fidelity gaps** (not security holes) plus documentation debt. Ordered by priority.

1. **[P1] Wire grant-derived trust into the read-trust set (GAP-1).**
   *Done:* a contributor granted via signed kind-39301 (level≥1) but absent from `rd.json` has its events **admitted** at ingestion and projection; `Trusted` set = `DeriveLevels(level≥1) ∪ self`. OR, if the separate feed is retained by decision, `nostr-identity-model.md` explicitly documents `TrustedPubkeys` as the intentional separate read-membership source and the A3 "one signed source" claim is amended. (`nostrproject.go:191`, `config.go:65`)

2. **[P2] Bind grant derivation to the full board coordinate (GAP-2).**
   *Done:* `DeriveLevels` receives boardD and rejects grants whose coordinate ≠ `30301:<owner>:<pinnedD>`; a grant on a different boardD is NOT honored on the pinned board; test proves cross-board grant bleed is closed. (`rolegrant.go:336`, `nostrproject.go:127-128`)

3. **[P2] Enforce or document the revocation-via-config-strip footgun (NOTE-A).**
   *Done:* either a lint/guard warns when an operator removes a key from `TrustedPubkeys` that still has live projected items, OR `nostr-identity-model.md` §6 documents that correct revocation = publish `role=revoked` grant AND leave config untouched. Resolves the interaction with GAP-1. (`nostrproject.go:202`)

4. **[P3] Document ready as permanently team-tier and enumerate what is deliberately absent.**
   *Done:* `nostr-identity-model.md` states ready is closed team-tier only, and explicitly records that reputation floor, proof-of-cost burn, scrip, and weighted-convergence math are **out of scope by construction** (keys inert until granted → sybil payoff zero). Cites `convergence-sybil-defense.md:8,93-95`.

5. **[P3] Codify the shared trust-doctrine one-liner and the named seam contract (Lessons 1 & 2).**
   *Done:* `nostr-identity-model.md` contains the "trust is a client-side projection… signature-validity is never admission" invariant and the enumerated authoritative-seam list (from §4) as a maintained contract.

6. **[P3] Rename "web-of-trust" to "owner-rooted bounded delegation" in ready docs (§3a).**
   *Done:* no ready design doc or comment describes the delegation model as "web-of-trust"; the model is described as owner-rooted, depth-capped, revocable delegation, with a note that dontguess's CAG rejection supports (not contradicts) it.

7. **[P2] Shared-relay write-allowlist coordination (ready-677).**
   *Done:* on any relay shared with dontguess, ready's `sync-allowlist` either writes a project-scoped file or merges (never overwrites) the union of admitted keys; a test/guard proves regeneration does not evict the other project's keys. (§6)

8. **[P4] Document same-second grant-ordering behavior (NOTE-B).**
   *Done:* `rolegrant.go` comment or design doc notes that a same-second maintainer-grant + downstream grant can no-op via id tie-break, and either seeds cap-evaluation with the final level map or documents the fail-closed behavior as acceptable. (`rolegrant.go:286-287,352`)

**Honest assessment:** ready's trust model is **already sound and secure** — 14/14 seams covered, no exploitable bypass, and notably free of the in-memory-reset class of bug that dontguess had to engineer around (Seam D). The remaining work is **discipline and documentation plus two fail-closed fidelity fixes**, not a security remediation. Items 1 and 2 close real code-vs-design gaps; items 3-8 are doc/coordination hardening.
<!-- Full raw seam audit + dontguess model available in workflow wf_c5ffa62e-c4d journal -->
