package foldvectors

// cases_grantauthority.go — the GRANT AUTHORITY half of the suite (ready-ce8).
//
// WHY THIS FILE EXISTS. The adversarial audit ready-3759 ran 34 mutations of the
// live fold against the committed corpus. Four did NOT turn a single vector red,
// which means the corpus was reporting GREEN on security properties that were
// switched OFF:
//
//	 (1) §12.5  signerMayGrant -> `return true` (the WHOLE escalation cap gone:
//	            owner-only maintainer grants, the non-owner level>=2 floor, owner
//	            lockout, peer-maintainer protection) — 33/33 vectors green.
//	 (2) §3.5   the revocation boundary weakened from `e.CreatedAt >= u` to `> u`
//	            — green, because the one grant vector revoked at t+100 and its
//	            dropped events sat at t+200/t+300, so NO fixture event ever landed
//	            exactly ON the revoke instant. The off-by-one was unfalsifiable.
//	 (3) §6.2   deleting the fold of grant-derived level>=2 keys into the pinned
//	            coordinate's status-authority maintainer set — green, because the
//	            only grant in the corpus issued role=contributor (level 1), so
//	            §6.2 had no discriminating fixture at all.
//	 (4) §12.3  remapping RoleContributor from level 1 to level 2 in roleToLevel
//	            — green, because no projection observed a role's NUMERIC level.
//
// The vectors below are built so that RE-APPLYING each of those mutations turns a
// NAMED vector red. Each one was proven by applying the mutation, running the
// suite, watching it fail, restoring, and re-confirming green — see the
// per-vector "PROVEN RED UNDER" notes. None of them asserts what the fold happens
// to do today: every expectation is derived from the cited clause.
//
// SHAPE OF EVERY VECTOR HERE. A cap-violating grant is IGNORED (§12.5), which
// means its grantee never enters `levels` at all, which means §12.8/§3.4 never
// admits it (`grantTrusts` is key-presence in `levels`, so an ignored grant is
// indistinguishable from no grant). The observable consequence is therefore
// ITEM ABSENCE: the escalated key's card produces NO item. Turning the cap off
// makes that item APPEAR — a full authorship takeover, since a card's author is
// also its own status authority (§6.4).
//
// Every vector pairs the rejection with a POSITIVE CONTROL inside the SAME
// fixture — a key admitted by a cap-VALID grant on the same board, whose card
// does fold. Without that control an "absent item" expectation would be
// satisfiable by any unrelated reason (board pinning, config trust, a malformed
// event) and would prove nothing about the cap.

import (
	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
	rdsync "github.com/3dl-dev/ready/pkg/sync"
)

// grant is a terse cap-relevant 39301 builder: `signer` publishes `role` for
// `grantee` on the OWNER's board coordinate. Binding every grant to the owner's
// coordinate (never the signer's own) is what makes these vectors about the
// escalation cap rather than about §12.2 full-coordinate binding.
func (b *builder) grant(signer *nostr.Key, grantee, role, label string, createdAt int64) (*nostr.Event, error) {
	return rdsync.BuildRoleGrantEvent(signer, rdsync.RoleGrantSpec{
		BoardD: boardD, BoardAuthor: b.ownerPub, Grantee: grantee, Role: role, Label: label,
	}, createdAt)
}

// vGrantCapOnlyOwnerGrantsMaintainer pins the FIRST branch of §12.5: `case
// RoleMaintainer, RoleOwner: return isBoardAuthor`. A level-2 maintainer — the
// highest authority below the owner — publishes role=maintainer for a third key.
// §12.5 says only the board author may, so that grant is ignored and the third
// key is never admitted to read-trust (§12.8, §3.4).
//
// PROVEN RED UNDER: signerMayGrant -> `return true`  (audit mutation 1)
// PROVEN RED UNDER: `case RoleMaintainer, RoleOwner: return isBoardAuthor` ->
// `return levels[signer] >= LevelMaintainer` (the narrow "a maintainer may
// appoint maintainers" regression this branch exists to forbid).
func (b *builder) vGrantCapOnlyOwnerGrantsMaintainer() error {
	// Cap-VALID: the board author appoints a maintainer (§12.5 first branch, §12.3).
	gMaint, err := b.grant(b.owner, b.maintPub, rdsync.RoleMaintainer, "owner appoints the maintainer", t0-200)
	if err != nil {
		return err
	}
	// Cap-VIOLATING: that maintainer tries to appoint another maintainer. Ignored.
	gEscalate, err := b.grant(b.maint, b.agentPub, rdsync.RoleMaintainer, "maintainer tries to appoint a peer", t0-100)
	if err != nil {
		return err
	}
	// POSITIVE CONTROL: the validly-appointed maintainer's own card DOES fold, so
	// the agent's absence below is about the cap, not about trust wiring.
	cMaint, err := b.card(b.maint, rdsync.CardSpec{
		ItemID: "ready-v34", Title: "authored by the owner-appointed maintainer", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	// The escalated key's card: NO item at all.
	cAgent, err := b.card(b.agent, rdsync.CardSpec{
		ItemID: "ready-v35", Title: "authored by the never-validly-appointed key", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0+100)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v34", MsgID: cMaint.ID, Title: "authored by the owner-appointed maintainer",
		Type: "task", Priority: "p1", Status: state.StatusInbox,
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "grant_cap_only_owner_grants_maintainer",
		SpecClauses: []string{"12.5", "12.3", "12.4", "12.8", "3.4"},
		Note: "Only the board author may grant maintainer/owner (§12.5). The owner-appointed maintainer " +
			"(level 2) publishes role=maintainer for a third key; that grant is IGNORED, so the third key " +
			"never enters the level map and is never admitted by §12.8/§3.4 — ready-v35 produces NO item, " +
			"not merely a dropped field. ready-v34, authored by the validly appointed maintainer, DOES fold: " +
			"the rejection is about the escalation cap, not about read-trust wiring. With the cap off, a " +
			"contributor self-promotes to maintainer and then authors authoritative status events and `by` " +
			"rewrites, which is why this is the highest-severity hole the audit found.",
		Options: Options{Trusted: trust(b.ownerPub), PinnedBoard: b.boardCoord},
		Events:  []*nostr.Event{gMaint, gEscalate, cMaint, cAgent},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"ready": {"ready-v34"}, "focus": {"ready-v34"}}),
		},
	})
}

// vGrantCapContributorMayNotDelegate pins the level floor in §12.5's
// contributor/revoked branch (`levels[signer] < LevelMaintainer -> false`) AND,
// as a direct consequence, §12.3's `contributor -> 1` mapping: a contributor's
// own grant is only refused BECAUSE its numeric level is below 2. This is the
// vector that makes the role->level table observable in a projection.
//
// PROVEN RED UNDER: signerMayGrant -> `return true`               (audit mutation 1)
// PROVEN RED UNDER: roleToLevel RoleContributor -> LevelMaintainer (audit mutation 4)
func (b *builder) vGrantCapContributorMayNotDelegate() error {
	// Cap-VALID: the owner admits a contributor (level 1, §12.3).
	gAgent, err := b.grant(b.owner, b.agentPub, rdsync.RoleContributor, "owner admits a contributor", t0-200)
	if err != nil {
		return err
	}
	// Cap-VIOLATING: a level-1 contributor tries to admit anyone at all. Ignored.
	gDelegated, err := b.grant(b.agent, b.outsiderPub, rdsync.RoleContributor, "contributor tries to admit a friend", t0-100)
	if err != nil {
		return err
	}
	// POSITIVE CONTROL: the contributor itself IS admitted, so the outsider's
	// absence is about the SIGNER's level, not about the grant's coordinate.
	cAgent, err := b.card(b.agent, rdsync.CardSpec{
		ItemID: "ready-v36", Title: "authored by the owner-admitted contributor", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	cOutsider, err := b.card(b.outsider, rdsync.CardSpec{
		ItemID: "ready-v37", Title: "authored by the contributor-delegated key", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0+100)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v36", MsgID: cAgent.ID, Title: "authored by the owner-admitted contributor",
		Type: "task", Priority: "p1", Status: state.StatusInbox,
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "grant_cap_contributor_may_not_delegate",
		SpecClauses: []string{"12.5", "12.3", "12.8", "3.4"},
		Note: "A non-owner signer must itself be level >= 2 to grant contributor/revoked (§12.5). The " +
			"owner-admitted CONTRIBUTOR is level 1 (§12.3), so its grant for the outsider is ignored and " +
			"ready-v37 produces NO item, while the contributor's own ready-v36 folds. This is the vector " +
			"that makes §12.3's role->level table falsifiable: remap `contributor` to level 2 and the " +
			"contributor's delegation becomes cap-valid, admitting the outsider — trust would spread " +
			"transitively from every contributor, which §12.5 exists to prevent.",
		Options: Options{Trusted: trust(b.ownerPub), PinnedBoard: b.boardCoord},
		Events:  []*nostr.Event{gAgent, gDelegated, cAgent, cOutsider},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"ready": {"ready-v36"}, "focus": {"ready-v36"}}),
		},
	})
}

// vGrantCapOwnerIsIrrevocable pins §12.5's OWNER LOCKOUT (`grantee ==
// boardAuthor -> false`): the board author is the self-certifying trust root and
// no non-owner signer may revoke or demote it. Without this branch a maintainer
// the owner appointed publishes role=revoked TARGETING the owner, §12.7 bounds
// the owner's `until`, and §3.5 then discards the owner's OWN later events — the
// trust anchor becomes revocable by a key it appointed.
//
// The same fixture carries the CONVERSE, so the lockout expectation is not
// vacuous: the very same maintainer's revoke of a level-1 CONTRIBUTOR does take
// effect (ready-v40 is dropped). A maintainer's revoke power is live; it just
// cannot reach the owner.
//
// WHY THIS FIXTURE DEMOTES THE OWNER FIRST (a finding, not decoration). Deleting
// the owner-lockout conjunct ALONE, on a fixture where the owner sits at its
// bootstrap level 2, leaves the suite GREEN — measured, not assumed: peer
// protection (`levels[grantee] >= LevelMaintainer -> false`) catches the very
// same grant one line later, so the two guards are indistinguishable for every
// state reachable without an owner self-demotion. The spec presents them as two
// independent protections (§12.5); on its own, the lockout branch is only
// load-bearing once `levels[boardAuthor]` has dropped BELOW 2, and the one
// cap-valid way to reach that state is an owner-signed grant naming the owner
// itself at role=contributor (`isBoardAuthor` short-circuits the cap, §12.5
// second branch, so the owner may always demote itself). This fixture therefore
// includes that self-demotion, which is what makes the lockout branch — and not
// its neighbour — the thing under test. Note the owner's GRANT power is
// unaffected by its own level (`isBoardAuthor` is an identity test, §12.4), so
// the self-demotion does not weaken the rest of the fixture.
//
// PROVEN RED UNDER: signerMayGrant -> `return true`                      (audit mutation 1)
// PROVEN RED UNDER: deleting only `if grantee == boardAuthor { return false }`
// PROVEN GREEN (before the self-demotion leg was added) under that same narrow
//
//	deletion — which is the whole reason the leg exists.
func (b *builder) vGrantCapOwnerIsIrrevocable() error {
	gMaint, err := b.grant(b.owner, b.maintPub, rdsync.RoleMaintainer, "owner appoints the maintainer", t0-400)
	if err != nil {
		return err
	}
	gAgent, err := b.grant(b.owner, b.agentPub, rdsync.RoleContributor, "owner admits a contributor", t0-390)
	if err != nil {
		return err
	}
	// Cap-VALID and load-bearing (see the doc comment): the owner demotes ITSELF
	// to contributor. `isBoardAuthor` short-circuits the cap, so this applies and
	// levels[owner] becomes 1 — below the peer-protection threshold, leaving the
	// owner-lockout conjunct as the ONLY thing standing between the maintainer's
	// next event and a revoked trust root. Note it does NOT bound the owner's
	// `until` (§12.7: only role=revoked does), and it does NOT cost the owner its
	// grant power (§12.4 / §12.5 test signer IDENTITY, not level).
	gSelfDemote, err := b.grant(b.owner, b.ownerPub, rdsync.RoleContributor, "owner demotes itself to contributor", t0-380)
	if err != nil {
		return err
	}
	// Cap-VIOLATING: the maintainer revokes the BOARD AUTHOR. Ignored (§12.5).
	rOwner, err := b.grant(b.maint, b.ownerPub, rdsync.RoleRevoked, "maintainer tries to revoke the owner", t0-200)
	if err != nil {
		return err
	}
	// Cap-VALID: the same maintainer revokes a level-1 contributor. Applies, so
	// until[agent] = t0-100 (§12.7, no `from` tag).
	rAgent, err := b.grant(b.maint, b.agentPub, rdsync.RoleRevoked, "maintainer revokes a contributor", t0-100)
	if err != nil {
		return err
	}
	// The contributor's PRE-revoke card survives (§3.5 is prospective).
	cAgentBefore, err := b.card(b.agent, rdsync.CardSpec{
		ItemID: "ready-v38", Title: "contributor card before its revoke", Status: state.StatusInbox, Priority: "p2", Type: "task",
	}, t0-150)
	if err != nil {
		return err
	}
	// The OWNER's card, authored AFTER the attempted revoke of the owner. It must
	// fold: the owner is irrevocable, so it has no bounded `until` at all.
	cOwner, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v39", Title: "owner card after the attempted owner-revoke", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	// The contributor's POST-revoke card is dropped — proof the maintainer's
	// revoke authority is real and this vector's owner-lockout half is not
	// standing in for "revokes never work here".
	cAgentAfter, err := b.card(b.agent, rdsync.CardSpec{
		ItemID: "ready-v40", Title: "contributor card after its revoke", Status: state.StatusInbox, Priority: "p2", Type: "task",
	}, t0+100)
	if err != nil {
		return err
	}
	items, err := itemsJSON(
		&state.Item{
			ID: "ready-v38", MsgID: cAgentBefore.ID, Title: "contributor card before its revoke",
			Type: "task", Priority: "p2", Status: state.StatusInbox,
			CreatedAt: nanos(t0 - 150), UpdatedAt: nanos(t0 - 150),
		},
		&state.Item{
			ID: "ready-v39", MsgID: cOwner.ID, Title: "owner card after the attempted owner-revoke",
			Type: "task", Priority: "p1", Status: state.StatusInbox,
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
	)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "grant_cap_owner_is_irrevocable",
		SpecClauses: []string{"12.5", "12.4", "12.7", "3.5", "12.8", "3.4"},
		Note: "OWNER LOCKOUT (§12.5): a non-owner signer may never target the board author. The " +
			"owner-appointed maintainer's role=revoked for the OWNER is ignored, so the owner keeps " +
			"until = authoritativeForever (§12.4/§12.7) and ready-v39 — authored 200s AFTER that attempted " +
			"revoke — still folds. The converse in the same fixture keeps the claim honest: the SAME " +
			"maintainer's revoke of a level-1 contributor DOES apply, so ready-v38 (before t0-100) folds " +
			"and ready-v40 (after) is absent. Drop the lockout branch and the owner's own card vanishes " +
			"from the projection: the trust root becomes revocable by a key it appointed. The owner " +
			"SELF-DEMOTES to contributor at t0-380 on purpose — with the owner at its bootstrap level 2, " +
			"peer protection catches the same grant one line later and the lockout branch is untestable " +
			"in isolation (it was measured green under a lockout-only deletion before this leg existed). " +
			"An owner's grant power is an identity test, not a level test (§12.4), so the demotion costs " +
			"it nothing else.",
		Options: Options{Trusted: trust(b.ownerPub), PinnedBoard: b.boardCoord},
		Events:  []*nostr.Event{gMaint, gAgent, gSelfDemote, rOwner, rAgent, cAgentBefore, cOwner, cAgentAfter},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v38", "ready-v39"},
				"focus": {"ready-v38", "ready-v39"},
			}),
		},
	})
}

// vGrantCapPeerMaintainerProtected pins §12.5's PEER PROTECTION (`levels[grantee]
// >= LevelMaintainer -> false`): only the owner may revoke or demote a CURRENT
// maintainer, so the maintainer set changes only by the owner's hand.
//
// As in the lockout vector, the converse rides along: the same maintainer's
// revoke of a level-1 key DOES apply, so "the revoke was ignored" cannot be
// explained by revokes being inert in this fixture.
//
// PROVEN RED UNDER: signerMayGrant -> `return true`                              (audit mutation 1)
// PROVEN RED UNDER: deleting only `if levels[grantee] >= LevelMaintainer { return false }`
func (b *builder) vGrantCapPeerMaintainerProtected() error {
	gMaint, err := b.grant(b.owner, b.maintPub, rdsync.RoleMaintainer, "owner appoints the first maintainer", t0-300)
	if err != nil {
		return err
	}
	gPeer, err := b.grant(b.owner, b.agentPub, rdsync.RoleMaintainer, "owner appoints the second maintainer", t0-290)
	if err != nil {
		return err
	}
	gOutsider, err := b.grant(b.owner, b.outsiderPub, rdsync.RoleContributor, "owner admits a contributor", t0-280)
	if err != nil {
		return err
	}
	// Cap-VIOLATING: maintainer revokes a PEER maintainer. Ignored (§12.5).
	rPeer, err := b.grant(b.maint, b.agentPub, rdsync.RoleRevoked, "maintainer tries to unseat a peer", t0-200)
	if err != nil {
		return err
	}
	// Cap-VALID: the same maintainer revokes the level-1 contributor.
	rOutsider, err := b.grant(b.maint, b.outsiderPub, rdsync.RoleRevoked, "maintainer revokes a contributor", t0-100)
	if err != nil {
		return err
	}
	cOutsiderBefore, err := b.card(b.outsider, rdsync.CardSpec{
		ItemID: "ready-v41", Title: "contributor card before its revoke", Status: state.StatusInbox, Priority: "p2", Type: "task",
	}, t0-150)
	if err != nil {
		return err
	}
	// The protected peer maintainer's card, authored AFTER the attempted revoke.
	cPeer, err := b.card(b.agent, rdsync.CardSpec{
		ItemID: "ready-v42", Title: "peer maintainer card after the attempted peer-revoke", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	// The revoked contributor's later REWRITE of ready-v41. Dropped by §3.5, so
	// the pre-revoke title/priority stand — a newer card that does NOT win.
	cOutsiderAfter, err := b.card(b.outsider, rdsync.CardSpec{
		ItemID: "ready-v41", Title: "post-revoke rewrite that must not land", Status: state.StatusDone, Priority: "p0", Type: "bug",
	}, t0+100)
	if err != nil {
		return err
	}
	items, err := itemsJSON(
		&state.Item{
			ID: "ready-v41", MsgID: cOutsiderBefore.ID, Title: "contributor card before its revoke",
			Type: "task", Priority: "p2", Status: state.StatusInbox,
			CreatedAt: nanos(t0 - 150), UpdatedAt: nanos(t0 - 150),
		},
		&state.Item{
			ID: "ready-v42", MsgID: cPeer.ID, Title: "peer maintainer card after the attempted peer-revoke",
			Type: "task", Priority: "p1", Status: state.StatusInbox,
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
	)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "grant_cap_peer_maintainer_protected",
		SpecClauses: []string{"12.5", "12.3", "12.7", "3.5", "4.1"},
		Note: "PEER PROTECTION (§12.5): a non-owner signer may not target a key that is CURRENTLY level " +
			">= 2. One owner-appointed maintainer's role=revoked for the other is ignored, so ready-v42 — " +
			"authored 200s later by the targeted peer — still folds. The converse rides along: the same " +
			"maintainer's revoke of the level-1 contributor DOES apply, so the contributor's later REWRITE " +
			"of ready-v41 (newer card, §4.1 latest-wins) is dropped and the pre-revoke title and p2 " +
			"priority stand. Drop peer protection and one maintainer can unseat every other, collapsing " +
			"the maintainer set to whoever revoked last.",
		Options: Options{Trusted: trust(b.ownerPub), PinnedBoard: b.boardCoord},
		Events: []*nostr.Event{
			gMaint, gPeer, gOutsider, rPeer, rOutsider, cOutsiderBefore, cPeer, cOutsiderAfter,
		},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v41", "ready-v42"},
				"focus": {"ready-v41", "ready-v42"},
			}),
		},
	})
}

// vRevokeBoundaryAtTheInstant pins the COMPARISON in §3.5 — `e.CreatedAt >=
// until[e.PubKey]`, not `>` — by putting fixture events on BOTH sides of the
// boundary and, critically, exactly ON it.
//
// The pre-existing grant vector could not do this: it revoked at t0+100 and
// placed its dropped events at t0+200 / t0+300, so weakening `>=` to `>` changed
// nothing and the suite stayed green. The revoke instant itself is the security-
// relevant edge — an event stamped with exactly the revoke's created_at is the
// one a revoked key racing its own revocation would publish — so it must be a
// fixture point, not an interpolation.
//
// PROVEN RED UNDER: `e.CreatedAt >= u` -> `e.CreatedAt > u`  (audit mutation 2)
func (b *builder) vRevokeBoundaryAtTheInstant() error {
	gAgent, err := b.grant(b.owner, b.agentPub, rdsync.RoleContributor, "owner admits the agent machine", t0-100)
	if err != nil {
		return err
	}
	cAgent, err := b.card(b.agent, rdsync.CardSpec{
		ItemID: "ready-v43", Title: "authored while granted", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	// ONE SECOND BEFORE the revoke instant: admitted under `>=` AND under `>`.
	// This is the lower bracket — it proves the boundary is a boundary and not a
	// blanket rejection of everything near the revoke.
	sJustBefore, err := b.status(b.agent, "ready-v43", state.StatusActive, "claimed one second before the revoke", t0+99, nil)
	if err != nil {
		return err
	}
	revoke, err := b.grant(b.owner, b.agentPub, rdsync.RoleRevoked, "machine decommissioned", t0+100)
	if err != nil {
		return err
	}
	// EXACTLY ON the revoke instant: dropped under `>=`, ADMITTED under `>`. Two
	// events, so the mutation shows up in three independent places — the winning
	// card (title/priority/type/status), the history chain, and updated_at.
	cAtInstant, err := b.card(b.agent, rdsync.CardSpec{
		ItemID: "ready-v43", Title: "rewrite stamped exactly at the revoke instant", Status: state.StatusDone, Priority: "p0", Type: "bug",
	}, t0+100)
	if err != nil {
		return err
	}
	sAtInstant, err := b.status(b.agent, "ready-v43", state.StatusDone, "closed exactly at the revoke instant", t0+100, nil)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v43", MsgID: cAgent.ID, Title: "authored while granted",
		Type: "task", Priority: "p1", Status: state.StatusActive,
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0 + 99),
		History: []state.HistoryEntry{
			{Timestamp: rfc(t0 + 99), FromStatus: "", ToStatus: state.StatusActive, ChangedBy: b.agentPub, Note: "claimed one second before the revoke"},
		},
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "revoke_boundary_excludes_the_revoke_instant",
		SpecClauses: []string{"3.5", "12.7", "12.8", "4.1", "6.5"},
		Note: "§3.5 drops an event when created_at >= until, and §12.7 sets until to the revoke's own " +
			"created_at when no `from` tag is present. So the revoke instant t0+100 is EXCLUSIVE: the " +
			"status event at t0+99 is authoritative, and BOTH the card rewrite and the close stamped at " +
			"exactly t0+100 are dropped. The item therefore keeps its original title, p1 priority, task " +
			"type, status=active and a single history row. This is the vector the corpus was missing: " +
			"weaken the comparison to `>` and a revoked key racing its own revocation lands a rewrite and " +
			"a close on the same second the owner revoked it.",
		Options: Options{Trusted: trust(b.ownerPub), PinnedBoard: b.boardCoord},
		Events:  []*nostr.Event{gAgent, cAgent, sJustBefore, revoke, cAtInstant, sAtInstant},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v43"}, "work": {"ready-v43"}, "focus": {"ready-v43"},
			}),
		},
	})
}

// vGrantLevelConfersStatusAuthority pins §6.2 — the fold of grant-derived level
// >= 2 keys into the PINNED coordinate's status-authority maintainer set — and,
// in the same fixture, §12.3's `contributor -> 1` on the receiving side of that
// threshold.
//
// This fixture carries NO 30301 board event and passes NO opts.Maintainers, so
// §6.1 and §6.3 contribute nothing: the ONLY possible source of status authority
// for a non-author key here is §6.2. That is deliberate — it is what makes the
// clause falsifiable in isolation.
//
// The two status events straddle the level threshold:
//
//	maintainer (level 2) -> authoritative -> the item goes active, one history row
//	contributor (level 1) -> ignored entirely -> the later `done` never lands
//
// PROVEN RED UNDER: deleting the `levels >= LevelMaintainer -> addBoardMaintainer`
//
//	loop in ProjectItems (audit mutation 3): the maintainer's
//	transition is dropped too, so the item stays inbox with an
//	empty history.
//
// PROVEN RED UNDER: roleToLevel RoleContributor -> LevelMaintainer (audit
//
//	mutation 4): the contributor crosses the threshold, its
//	`done` becomes authoritative and CLOSES someone else's item.
func (b *builder) vGrantLevelConfersStatusAuthority() error {
	gMaint, err := b.grant(b.owner, b.maintPub, rdsync.RoleMaintainer, "owner appoints the maintainer", t0-200)
	if err != nil {
		return err
	}
	gAgent, err := b.grant(b.owner, b.agentPub, rdsync.RoleContributor, "owner admits a contributor", t0-100)
	if err != nil {
		return err
	}
	// The card is authored by the OWNER, so neither status signer below is the
	// item author: §6.4's author branch cannot explain either outcome.
	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v44", Title: "owner-authored item", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	sMaint, err := b.status(b.maint, "ready-v44", state.StatusActive, "picked up by the grant-derived maintainer", t0+50, nil)
	if err != nil {
		return err
	}
	// LATER than the maintainer's transition, and terminal: if the contributor
	// were a status authority it would win CURRENT state, not merely add a row.
	sAgent, err := b.status(b.agent, "ready-v44", state.StatusDone, "contributor tries to close someone else's item", t0+100, nil)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v44", MsgID: c.ID, Title: "owner-authored item",
		Type: "task", Priority: "p1", Status: state.StatusActive,
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0 + 50),
		History: []state.HistoryEntry{
			{Timestamp: rfc(t0 + 50), FromStatus: "", ToStatus: state.StatusActive, ChangedBy: b.maintPub, Note: "picked up by the grant-derived maintainer"},
		},
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "grant_level_two_confers_status_authority",
		SpecClauses: []string{"6.2", "6.4", "12.3", "12.5", "6.11"},
		Note: "There is NO 30301 board event and no opts.Maintainers in this fixture, so §6.1 and §6.3 " +
			"contribute nothing and §6.2 is the only possible source of status authority for a non-author " +
			"key: a role=maintainer grantee (level 2) is a maintainer of the PINNED coordinate. Its " +
			"transition is authoritative on an item the OWNER authored. The role=contributor grantee is " +
			"level 1 (§12.3), below the threshold, so its LATER terminal `done` is excluded entirely — no " +
			"status change and no history row (§6.4). Delete the §6.2 fold and the maintainer's transition " +
			"disappears too; remap contributor to level 2 and a contributor closes an item it does not own.",
		Options: Options{Trusted: trust(b.ownerPub), PinnedBoard: b.boardCoord},
		Events:  []*nostr.Event{gMaint, gAgent, c, sMaint, sAgent},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v44"}, "work": {"ready-v44"}, "focus": {"ready-v44"},
			}),
		},
	})
}
