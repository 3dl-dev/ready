---
title: Relay write-admission — closing the grant→relay forward path (and reconciling the cosmos relay + vibrant open+PoW)
status: DRAFT (design, needs Baron ruling on §6)
epic: ready-a14 (nostr identity & trust)
builds-on: docs/design/nostr-identity-model.md, docs/design/identity-reconciliation-ready-vs-dontguess.md (GAP-1), docs/design/confidential-boards-envelope.md
date: 2026-08-19
---

# Relay write-admission — closing the grant→relay forward path

**Audience:** ready maintainers + the nostr-relay owner (enforcement code lives in `~/projects/nostr-relay`).
**One-line problem:** a new ready writer identity has **no working forward path to relay write access** today — not because the model is undesigned, but because the designed grant→allowlist bridge points at a file the current relay ignores.

This doc does **not** relitigate ready's decided identity model. `nostr-identity-model.md` already ruled owner-rooted bounded delegation via kind-39301 grants, generate-then-authorize, the invite→join→grant lifecycle, and "derive the allowlist from one signed source." This doc closes the **relay side of GAP-1** (`identity-reconciliation-ready-vs-dontguess.md`: "read-trust set is fed by hand-maintained config, not derived from grants") and reconciles it with two things that shipped *after* those docs: the cosmos/khatru relay (nostr-relay `nostrrelay-8d2`) and the vibrant open+PoW policy (nostr-relay `nostrrelay-164`, 2026-08-19).

---

## 1. The break, precisely

The designed forward path (`nostr-identity-model.md` §2, invite lifecycle):

1. Owner `rd invite` → mints a claim token (no secret, TTL-bounded).
2. Recipient `rd join` → self-mints an **inert** key, imports the board read-only.
3. Recipient sends owner their `pubkey` + `claim`.
4. Owner `rd grant <pubkey> contributor --claim <claim>` → publishes an owner-signed **kind-39301** role-grant.
5. Owner `rd relay sync-allowlist --apply` → regenerates the relay write-allowlist from `{pubkeys with a non-revoked grant}`.

Step 5 is where it dead-ends. `rd relay sync-allowlist` writes **`scripts/relay-policy/write-allowlist.json`** (`pkg/sync/allowlist.go:21`; test `pkg/sync/allowlist_test.go:53`). That file is a **strfry-era artifact**. The relay that actually serves `wss://relay.3dl.network` today is the cosmos/khatru relay, and it **does not read that file at all** — it admits writers only from an Azure Cosmos `NostrRegistry` container, populated **exclusively** through the NIP-86 `allowpubkey` management API, which is gated to `NOSTR_RELAY_ADMIN_PUBKEYS` (nostr-relay `internal/cosmosstore/{writegate,registry,nip86admin}.go`).

Consequence: a grant-derived writer key lands in a JSON file that nothing consumes. The **only** thing that actually admits a key is an operator manually running NIP-86 `allowpubkey` with an admin secret in hand (nostr-relay `cmd/admitkey`). That is a human bottleneck per identity — not a forward path. The two ready keys currently in that JSON (`780bf45d…`, `a9f766ae…`) are also in the cosmos registry only because they were manually admitted once; the sync never put them there.

**So the model isn't missing — the wire between ready's grants and the relay's admission surface is severed.**

## 2. What already IS decided (do not rebuild)

- **Delegation carrier:** owner-signed addressable **kind-39301 role-grant**, latest-wins, revocable, graded 0/1/2 (revoked/contributor/maintainer). Owner = the 30301 board author, the pinned trust root (`nostr-identity-model.md` §3).
- **Generate-then-authorize:** a minted key is inert until an owner grant names it. Sybil payoff is structurally zero.
- **Single signed source:** the allowlist is a **projection of non-revoked grants**, never a hand-maintained list.
- **Confidentiality is already solved and is orthogonal to admission:** `confidential-boards-envelope.md` (FROZEN) encrypts only `Content`; routing tags stay clear/tokenized. So **reads can be open** — a third party reading the relay sees ciphertext for confidential boards. Admission is a *write* concern; confidentiality is an *encryption* concern. They do not need to be coupled. (This retires the worry that opening the relay re-exposes the 734-card leak: the leak was plaintext content; the envelope spec is the fix, not relay read-ACLs.)

## 3. The mixed / third-party answer

Baron's ruling this session: ready writers include third parties, not just the operator's fleet. The decided model already handles this — a third party is exactly a `rd join` recipient who receives an owner grant. Their forward path is the invite lifecycle above. **No new trust model is required; the relay just has to honor the grants.**

## 4. Target model — one write gate, capability-sourced

The relay admits a write iff the writer presents a valid capability, resolved in order:

1. **author == board owner** — a self-authored, author-scoped addressable event; always allowed (sigs make cross-author overwrite impossible).
2. **admin/root** — `NOSTR_RELAY_ADMIN_PUBKEYS`; operator bootstrap only (today's manual allowlist, demoted to this).
3. **valid owner grant** — a non-revoked kind-39301 grant, signed by the pinned owner of the board this write targets, naming the writer's pubkey. This is the team/third-party path.
4. **public PoW** — vibrant open boards: PoW + kind + size (nostr-relay `nostrrelay-164`, already shipped). PoW is "the public grant."

This unifies every existing write model as one of four capability sources, and — critically — makes the relay finally *use* the 39301 grants ready already emits.

## 5. Two ways to wire step (3) — the real decision

**Option A — sync pushes NIP-86 (bridge the existing job).** `rd relay sync-allowlist --apply` reconciles the relay's Cosmos registry to the grant-derived set via NIP-86 `allowpubkey`/`banpubkey` (the surface `cmd/admitkey` already speaks), instead of writing the dead JSON. 
- *Pros:* smallest change; relay stays a flat allowlist; reuses shipped NIP-86 + admitkey; no write-path grant verification.
- *Cons:* needs an admin secret wherever the owner runs sync; the allowlist is still a **cache** of grants that drifts until the next sync; revocation latency = sync cadence; two sources of truth (grants + registry).

**Option B — relay derives admission from grants (single source of truth).** The relay's write gate itself verifies a non-revoked 39301 grant (rooted at the board owner) at write time. No sync job; the signed grants on the relay *are* the allowlist.
- *Pros:* one source of truth; instant revocation; the invite→grant flow completes with zero operator/relay-ops involvement; retires the sync job, the JSON, and the manual admitkey path for team writers.
- *Cons:* stateful authz on the write path (resolve board owner from the 30301, load owner's grant events, check sig + grantee + level + not-revoked + point-in-time). Latency and a grant index. Revocation and grant-caching semantics are the sharp edges — must mirror `pkg/sync/rolegrant.go`'s latest-wins + prospective-revocation exactly so relay and client agree.

**Recommendation:** **B is the end state** — it's the only option that makes the relay's admission and ready's trust projection the *same* signed source, which is the whole point of GAP-1. **A is a legitimate interim** if we want the forward path working this week without building write-path grant verification: flip sync to NIP-86, keep the flat registry as a grant cache. A and B are compatible — A now, B later, same grant source.

## 6. Open decisions (Baron)

1. **A-interim vs straight-to-B.** Ship the NIP-86 sync bridge now (forward path works, allowlist is a cache), or invest directly in relay-side grant verification (single source of truth)?
2. **Grant verification locus for B.** Relay reads 39301s from its own store on the write path (self-contained, but the relay must learn ready's owner-rooted grant semantics), vs. a relay-adjacent projector that maintains the registry from grants (keeps grant semantics out of the relay, but is Option A wearing a hat).
3. **Multi-tenant owner roots.** The relay is one process over many boards/tenants (ready, dontguess, vibrant). For B it must resolve "who is the authoritative owner of board X" per write. Pin owners per board-coordinate from the 30301 author, or maintain a per-tenant root config?

Non-decisions (mine once §6 is ruled): grant index shape, verification algorithm, hook wiring in `relay.go`, sync reconciliation diff.

## 7. Decomposition sketch (post-ruling)

- **Interim (A):** repoint `rd relay sync-allowlist --apply` from `write-allowlist.json` to NIP-86 reconcile (allowpubkey add / banpubkey remove to match the non-revoked grant set); delete the orphaned JSON + its dead references in nostr-relay; make `rd invite`→`rd grant`→`rd relay sync-allowlist` an end-to-end tested path against the live relay.
- **End state (B):** relay-side `RejectVibrantWrite`-sibling `RejectUnlessGranted` hook that verifies a 39301 grant rooted at the board owner; a grant index + revocation cache in `internal/cosmosstore`; retire the sync job for team writers; conformance test proving invite→grant→write with no operator step; reconcile with the capability-gate ordering in §4 so vibrant PoW and owner-grants coexist.

---

**Cross-repo note:** design + tracking live here in `ready` (this is a ready trust-model concern). Enforcement code lands in `~/projects/nostr-relay` (`internal/cosmosstore`). The vibrant open+PoW gate (nostr-relay `nostrrelay-164`, shipped 2026-08-19) is already the §4-case-4 implementation and is forward-compatible with every option here.
