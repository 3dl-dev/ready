// Deterministic, non-vacuous unit tests for ready-8ce / BP-2: graded operator
// levels derived from signed kind-39301 role-grant events (the nostr port of
// pkg/provenance/checker.go).
//
// Every event here is REAL: built + schnorr-signed via BuildRoleGrantEvent and
// re-verified inside DeriveLevels. The assertions check the derived LEVEL and
// AUTHORITATIVE-UNTIL values, the escalation cap (violating grants ignored), and
// prospective revocation — not err==nil.
package sync

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
)

const testBoardD = "ready"

// grant is a tiny helper: build+sign a 39301 event for boardAuthor's board.
func grant(t *testing.T, signer *nostr.Key, boardAuthor, grantee, role string, from, createdAt int64) *nostr.Event {
	t.Helper()
	e, err := BuildRoleGrantEvent(signer, RoleGrantSpec{
		BoardD:      testBoardD,
		BoardAuthor: boardAuthor,
		Grantee:     grantee,
		Role:        role,
		From:        from,
		Label:       role + "-label",
	}, createdAt)
	if err != nil {
		t.Fatalf("BuildRoleGrantEvent(%s): %v", role, err)
	}
	return e
}

// TestDeriveLevels_MatchesCheckerSemantics proves the level map matches
// checker.go: bootstrap owner=2, maintainer grant=2, contributor grant=1, revoked
// grant=0, and a key with no grant is ABSENT (caller defaults it to 1).
func TestDeriveLevels_MatchesCheckerSemantics(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	maint := testKey(t)
	contrib := testKey(t)
	revoked := testKey(t)
	nogrant := testKey(t)

	events := []*nostr.Event{
		grant(t, owner, ba, maint.PubKeyHex(), RoleMaintainer, 0, 1000),
		grant(t, owner, ba, contrib.PubKeyHex(), RoleContributor, 0, 1001),
		grant(t, owner, ba, revoked.PubKeyHex(), RoleRevoked, 0, 1002),
	}

	levels, until := DeriveLevels(events, ba, testBoardD)

	if got := levels[ba]; got != LevelMaintainer {
		t.Errorf("board author level = %d, want %d (bootstrap)", got, LevelMaintainer)
	}
	if got := levels[maint.PubKeyHex()]; got != LevelMaintainer {
		t.Errorf("maintainer level = %d, want %d", got, LevelMaintainer)
	}
	if got := levels[contrib.PubKeyHex()]; got != LevelContributor {
		t.Errorf("contributor level = %d, want %d", got, LevelContributor)
	}
	if got := levels[revoked.PubKeyHex()]; got != LevelRevoked {
		t.Errorf("revoked level = %d, want %d", got, LevelRevoked)
	}
	if _, ok := levels[nogrant.PubKeyHex()]; ok {
		t.Errorf("no-grant key should be ABSENT from level map (caller defaults to 1)")
	}
	// Non-revoked keys authoritative forever; revoked key bounded at revoke time.
	if until[ba] != authoritativeForever {
		t.Errorf("board author until = %d, want +inf", until[ba])
	}
	if until[maint.PubKeyHex()] != authoritativeForever {
		t.Errorf("maintainer until = %d, want +inf", until[maint.PubKeyHex()])
	}
	if got := until[revoked.PubKeyHex()]; got != 1002 {
		t.Errorf("revoked until = %d, want 1002 (revoke created_at)", got)
	}
}

// TestDeriveLevels_LatestGrantPerGranteeWins proves the addressable latest-wins
// rule: for one grantee, the newest grant (higher created_at) determines the level,
// regardless of the order the events are supplied in.
func TestDeriveLevels_LatestGrantPerGranteeWins(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	subject := testKey(t)

	older := grant(t, owner, ba, subject.PubKeyHex(), RoleMaintainer, 0, 1000)
	newer := grant(t, owner, ba, subject.PubKeyHex(), RoleContributor, 0, 2000)

	// Supply newest-first to prove ordering is by (created_at,id), not slice order.
	levels, _ := DeriveLevels([]*nostr.Event{newer, older}, ba, testBoardD)
	if got := levels[subject.PubKeyHex()]; got != LevelContributor {
		t.Errorf("latest grant (contributor@2000) should win: level = %d, want %d", got, LevelContributor)
	}

	// And the reverse supply order gives the identical winner (determinism).
	levels2, _ := DeriveLevels([]*nostr.Event{older, newer}, ba, testBoardD)
	if got := levels2[subject.PubKeyHex()]; got != LevelContributor {
		t.Errorf("supply-order independence broken: level = %d, want %d", got, LevelContributor)
	}
}

// TestDeriveLevels_LatestWinsTieBreakLowestID proves the created_at TIE break
// matches newerThan: the LOWEST-id grant is canonical and wins.
func TestDeriveLevels_LatestWinsTieBreakLowestID(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	subject := testKey(t)

	a := grant(t, owner, ba, subject.PubKeyHex(), RoleMaintainer, 0, 5000)
	b := grant(t, owner, ba, subject.PubKeyHex(), RoleContributor, 0, 5000)
	if a.ID == b.ID {
		t.Fatal("tie-break test needs distinct ids")
	}
	// Determine which id is lower; that grant's role must win.
	wantLevel := LevelMaintainer // role of a
	if b.ID < a.ID {
		wantLevel = LevelContributor // role of b
	}
	levels, _ := DeriveLevels([]*nostr.Event{a, b}, ba, testBoardD)
	if got := levels[subject.PubKeyHex()]; got != wantLevel {
		t.Errorf("tie-break: level = %d, want %d (lowest-id grant wins)", got, wantLevel)
	}
}

// TestDeriveLevels_EscalationCap_MaintainerCannotMintMaintainer is the security
// crux: a level-2 maintainer (not the board author) signing a role=maintainer
// grant is IGNORED, but the SAME maintainer signing contributor/revoked is honored.
func TestDeriveLevels_EscalationCap_MaintainerCannotMintMaintainer(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	maint := testKey(t)    // becomes a maintainer via an owner grant
	target := testKey(t)   // maint tries to mint this one as maintainer
	promoted := testKey(t) // maint validly makes this one a contributor
	revokee := testKey(t)  // maint validly revokes this one

	events := []*nostr.Event{
		// Owner establishes maint as level 2 FIRST (t=1000).
		grant(t, owner, ba, maint.PubKeyHex(), RoleMaintainer, 0, 1000),
		// maint (not board author) tries to mint a new maintainer — MUST be ignored.
		grant(t, maint, ba, target.PubKeyHex(), RoleMaintainer, 0, 2000),
		// maint grants a contributor — allowed within the cap.
		grant(t, maint, ba, promoted.PubKeyHex(), RoleContributor, 0, 2001),
		// maint revokes a key — allowed within the cap.
		grant(t, maint, ba, revokee.PubKeyHex(), RoleRevoked, 0, 2002),
	}

	levels, _ := DeriveLevels(events, ba, testBoardD)

	if _, ok := levels[target.PubKeyHex()]; ok {
		t.Errorf("maintainer-signed maintainer grant MUST be ignored; target present with level %d", levels[target.PubKeyHex()])
	}
	if got := levels[promoted.PubKeyHex()]; got != LevelContributor {
		t.Errorf("maintainer-signed contributor grant should be honored: level = %d, want %d", got, LevelContributor)
	}
	if got := levels[revokee.PubKeyHex()]; got != LevelRevoked {
		t.Errorf("maintainer-signed revoke should be honored: level = %d, want %d", got, LevelRevoked)
	}
}

// TestDeriveLevels_OwnerIrrevocable is the HIGH-1 privilege-escalation proof: a
// level-2 MAINTAINER (not the board author) cannot revoke — or downgrade — the
// board author. Before the fix the escalation cap checked only the minted role and
// the signer level, never the GRANTEE identity, so a maintainer-signed role=revoked
// TARGETING the owner was applied, dropping the owner to level 0 (its own events
// then discarded at the projection until-gate). The owner is irrevocable: it keeps
// level 2 and +infinity authoritative-until no matter what a maintainer publishes.
func TestDeriveLevels_OwnerIrrevocable(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	maint := testKey(t) // an owner-appointed maintainer, the attacker

	events := []*nostr.Event{
		// Owner appoints maint as a level-2 maintainer.
		grant(t, owner, ba, maint.PubKeyHex(), RoleMaintainer, 0, 1000),
		// maint (a maintainer, not the owner) tries to REVOKE the owner — MUST be ignored.
		grant(t, maint, ba, ba, RoleRevoked, 0, 2000),
		// maint also tries to DOWNGRADE the owner to contributor — MUST be ignored.
		grant(t, maint, ba, ba, RoleContributor, 0, 2001),
	}
	levels, until := DeriveLevels(events, ba, testBoardD)

	if got := levels[ba]; got != LevelMaintainer {
		t.Errorf("owner-lockout breach: board author level = %d, want %d (owner is irrevocable)", got, LevelMaintainer)
	}
	if got := until[ba]; got != authoritativeForever {
		t.Errorf("owner-lockout breach: board author authoritative-until = %d, want +inf (its events must never be gated)", got)
	}
}

// TestDeriveLevels_PeerMaintainerCannotBeRevokedByPeer proves the peer-maintainer
// protection: only the owner may revoke a maintainer. A maintainer-signed revoke of
// a PEER maintainer is ignored (the peer keeps level 2), while the SAME owner-signed
// revoke still takes effect.
func TestDeriveLevels_PeerMaintainerCannotBeRevokedByPeer(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	m1 := testKey(t) // attacker maintainer
	m2 := testKey(t) // victim peer maintainer

	events := []*nostr.Event{
		grant(t, owner, ba, m1.PubKeyHex(), RoleMaintainer, 0, 1000),
		grant(t, owner, ba, m2.PubKeyHex(), RoleMaintainer, 0, 1001),
		// m1 tries to revoke the peer maintainer m2 — MUST be ignored.
		grant(t, m1, ba, m2.PubKeyHex(), RoleRevoked, 0, 2000),
	}
	levels, _ := DeriveLevels(events, ba, testBoardD)
	if got := levels[m2.PubKeyHex()]; got != LevelMaintainer {
		t.Errorf("peer-maintainer breach: m2 level = %d, want %d (a maintainer cannot revoke a peer)", got, LevelMaintainer)
	}

	// CONTROL: the OWNER revoking a maintainer still works.
	events2 := []*nostr.Event{
		grant(t, owner, ba, m1.PubKeyHex(), RoleMaintainer, 0, 1000),
		grant(t, owner, ba, m1.PubKeyHex(), RoleRevoked, 0, 3000),
	}
	levels2, _ := DeriveLevels(events2, ba, testBoardD)
	if got := levels2[m1.PubKeyHex()]; got != LevelRevoked {
		t.Errorf("owner revoking a maintainer must work: m1 level = %d, want %d", got, LevelRevoked)
	}
}

// TestMayGrant_ClientMirror proves the exported client-side cap mirrors the
// read-side rule (MED-6): a plain contributor may grant nothing; a maintainer may
// grant a contributor but NOT revoke the owner or a peer maintainer; the owner may
// do everything.
func TestMayGrant_ClientMirror(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	maint := testKey(t)
	contrib := testKey(t)
	peer := testKey(t)
	fresh := testKey(t)

	events := []*nostr.Event{
		grant(t, owner, ba, maint.PubKeyHex(), RoleMaintainer, 0, 1000),
		grant(t, owner, ba, contrib.PubKeyHex(), RoleContributor, 0, 1001),
		grant(t, owner, ba, peer.PubKeyHex(), RoleMaintainer, 0, 1002),
	}

	// Contributor may grant nothing.
	if MayGrant(events, ba, testBoardD, contrib.PubKeyHex(), fresh.PubKeyHex(), RoleContributor) {
		t.Error("contributor must not be able to grant contributor")
	}
	// Maintainer may grant a fresh contributor...
	if !MayGrant(events, ba, testBoardD, maint.PubKeyHex(), fresh.PubKeyHex(), RoleContributor) {
		t.Error("maintainer must be able to grant a fresh contributor")
	}
	// ...but not revoke the owner...
	if MayGrant(events, ba, testBoardD, maint.PubKeyHex(), ba, RoleRevoked) {
		t.Error("maintainer must NOT be able to revoke the owner")
	}
	// ...nor revoke a peer maintainer.
	if MayGrant(events, ba, testBoardD, maint.PubKeyHex(), peer.PubKeyHex(), RoleRevoked) {
		t.Error("maintainer must NOT be able to revoke a peer maintainer")
	}
	// Owner may mint a maintainer and revoke anyone.
	if !MayGrant(events, ba, testBoardD, ba, fresh.PubKeyHex(), RoleMaintainer) {
		t.Error("owner must be able to grant maintainer")
	}
	if !MayGrant(events, ba, testBoardD, ba, peer.PubKeyHex(), RoleRevoked) {
		t.Error("owner must be able to revoke a maintainer")
	}
}

// TestDeriveLevels_EscalationCap_ContributorCannotGrant proves a level-1
// contributor cannot delegate anything — all its grants are ignored.
func TestDeriveLevels_EscalationCap_ContributorCannotGrant(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	contrib := testKey(t)
	victim := testKey(t)

	events := []*nostr.Event{
		grant(t, owner, ba, contrib.PubKeyHex(), RoleContributor, 0, 1000),
		// contributor tries to grant a contributor — ignored (may grant nothing).
		grant(t, contrib, ba, victim.PubKeyHex(), RoleContributor, 0, 2000),
	}
	levels, _ := DeriveLevels(events, ba, testBoardD)
	if _, ok := levels[victim.PubKeyHex()]; ok {
		t.Errorf("contributor-signed grant MUST be ignored; victim present with level %d", levels[victim.PubKeyHex()])
	}
}

// TestDeriveLevels_EscalationCap_MaintainerAuthorityMustPrecedeGrant proves the
// cap is evaluated against state replayed SO FAR: a would-be maintainer's grant
// signed BEFORE the owner grants it maintainer is ignored (it was not yet level 2).
func TestDeriveLevels_EscalationCap_MaintainerAuthorityMustPrecedeGrant(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	maint := testKey(t)
	target := testKey(t)

	events := []*nostr.Event{
		// maint signs a contributor grant at t=500, BEFORE it is a maintainer.
		grant(t, maint, ba, target.PubKeyHex(), RoleContributor, 0, 500),
		// owner grants maint maintainer only at t=1000.
		grant(t, owner, ba, maint.PubKeyHex(), RoleMaintainer, 0, 1000),
	}
	levels, _ := DeriveLevels(events, ba, testBoardD)
	if _, ok := levels[target.PubKeyHex()]; ok {
		t.Errorf("grant signed before signer had authority MUST be ignored; target level %d", levels[target.PubKeyHex()])
	}
	if got := levels[maint.PubKeyHex()]; got != LevelMaintainer {
		t.Errorf("maint should still be level %d, got %d", LevelMaintainer, got)
	}
}

// TestDeriveLevels_ProspectiveRevocation proves authoritative-until = the revoking
// grant's created_at by default, and that an explicit from=T overrides it (the
// retroactive repudiation escape hatch).
func TestDeriveLevels_ProspectiveRevocation(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	clean := testKey(t)       // revoked prospectively (no from)
	compromised := testKey(t) // revoked with from=T (retroactive)

	events := []*nostr.Event{
		grant(t, owner, ba, clean.PubKeyHex(), RoleContributor, 0, 1000),
		grant(t, owner, ba, clean.PubKeyHex(), RoleRevoked, 0, 3000), // prospective @3000
		grant(t, owner, ba, compromised.PubKeyHex(), RoleMaintainer, 0, 1000),
		grant(t, owner, ba, compromised.PubKeyHex(), RoleRevoked, 2500, 4000), // from=2500 overrides created_at
	}
	levels, until := DeriveLevels(events, ba, testBoardD)

	if got := levels[clean.PubKeyHex()]; got != LevelRevoked {
		t.Errorf("clean revoked level = %d, want %d", got, LevelRevoked)
	}
	if got := until[clean.PubKeyHex()]; got != 3000 {
		t.Errorf("prospective revoke until = %d, want 3000 (revoke created_at)", got)
	}
	if got := until[compromised.PubKeyHex()]; got != 2500 {
		t.Errorf("from=T revoke until = %d, want 2500 (from overrides created_at)", got)
	}
}

// TestDeriveLevels_ForeignBoardGrantIgnored proves a grant whose "a" authority
// coordinate names a DIFFERENT board owner is not part of this board's authority
// chain and is ignored (the parallel-board self-escalation path, A5).
func TestDeriveLevels_ForeignBoardGrantIgnored(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	attacker := testKey(t)
	self := testKey(t)

	// attacker forks its OWN board and self-grants maintainer on it.
	foreign, err := BuildRoleGrantEvent(attacker, RoleGrantSpec{
		BoardD:      testBoardD,
		BoardAuthor: attacker.PubKeyHex(), // a = 30301:<attacker>:ready — NOT ba's board
		Grantee:     self.PubKeyHex(),
		Role:        RoleMaintainer,
	}, 2000)
	if err != nil {
		t.Fatalf("build foreign grant: %v", err)
	}
	levels, _ := DeriveLevels([]*nostr.Event{foreign}, ba, testBoardD)
	if _, ok := levels[self.PubKeyHex()]; ok {
		t.Errorf("foreign-board grant MUST be ignored; self present with level %d", levels[self.PubKeyHex()])
	}
}

// TestDeriveLevels_DifferentBoardDGrantNotHonored is the GAP-2 (ready-885) proof:
// a grant on 30301:<owner>:<OTHER-d> — SAME owner, DIFFERENT boardD — is NOT honored
// when deriving for the pinned board 30301:<owner>:ready, while a grant on the pinned
// coordinate still confers its level. Before the fix deriveGrants bound by owner ALONE
// (g.BoardOwner != boardAuthor), so a grant on any other boardD of the same owner bled
// onto the pinned board (cross-board grant bleed). The full-coordinate match closes it.
func TestDeriveLevels_DifferentBoardDGrantNotHonored(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	crossBoard := testKey(t) // granted on a DIFFERENT boardD (same owner)
	pinned := testKey(t)     // granted on the PINNED boardD

	otherBoardGrant, err := BuildRoleGrantEvent(owner, RoleGrantSpec{
		BoardD:      "other-board", // 30301:<owner>:other-board — NOT the pinned "ready"
		BoardAuthor: ba,
		Grantee:     crossBoard.PubKeyHex(),
		Role:        RoleMaintainer,
	}, 1000)
	if err != nil {
		t.Fatalf("build cross-board grant: %v", err)
	}
	pinnedGrant := grant(t, owner, ba, pinned.PubKeyHex(), RoleContributor, 0, 1001) // BoardD=testBoardD

	levels, _ := DeriveLevels([]*nostr.Event{otherBoardGrant, pinnedGrant}, ba, testBoardD)

	if _, ok := levels[crossBoard.PubKeyHex()]; ok {
		t.Errorf("cross-board grant bleed: a grant on a DIFFERENT boardD (same owner) was honored on the pinned board (level %d)", levels[crossBoard.PubKeyHex()])
	}
	if got := levels[pinned.PubKeyHex()]; got != LevelContributor {
		t.Errorf("pinned-board grant must still confer its level: got %d want %d", got, LevelContributor)
	}

	// CONTROL: deriving for the OTHER boardD honors the cross-board grant and NOT the
	// pinned one — proving the boardD is the discriminator, not some incidental drop.
	otherLevels, _ := DeriveLevels([]*nostr.Event{otherBoardGrant, pinnedGrant}, ba, "other-board")
	if got := otherLevels[crossBoard.PubKeyHex()]; got != LevelMaintainer {
		t.Errorf("control: the cross-board grant must be honored when deriving for ITS boardD: got %d want %d", got, LevelMaintainer)
	}
	if _, ok := otherLevels[pinned.PubKeyHex()]; ok {
		t.Errorf("control: the pinned-board grant must NOT be honored for the other boardD")
	}
}

// TestDeriveReadTrust_MembershipIncludesGranteesAndOwner is the GAP-1 (ready-7c1)
// unit: the derived read-trust membership set is { board author } ∪ { cap-valid
// grantees, including revoked }, and EXCLUDES an ungranted foreign key (fail-closed).
func TestDeriveReadTrust_MembershipIncludesGranteesAndOwner(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	contrib := testKey(t)
	revoked := testKey(t)
	foreign := testKey(t) // never granted

	events := []*nostr.Event{
		grant(t, owner, ba, contrib.PubKeyHex(), RoleContributor, 0, 1000),
		grant(t, owner, ba, revoked.PubKeyHex(), RoleContributor, 0, 1001),
		grant(t, owner, ba, revoked.PubKeyHex(), RoleRevoked, 0, 2000),
	}
	trust := DeriveReadTrust(events, ba, testBoardD)

	if !trust[ba] {
		t.Error("board author (bootstrap root) must be in the derived read-trust set")
	}
	if !trust[contrib.PubKeyHex()] {
		t.Error("granted contributor must be in the derived read-trust set")
	}
	if !trust[revoked.PubKeyHex()] {
		t.Error("revoked-but-once-granted key must STAY in read-trust (its PAST events survive; the until gate drops its future)")
	}
	if trust[foreign.PubKeyHex()] {
		t.Error("fail-closed: an ungranted foreign key must NOT be in the derived read-trust set")
	}
}

// TestDeriveLevels_ZeroGrantsBootstrapFallback proves backward compatibility: with
// zero 39301 events, only the board author is derived (level 2), so already-migrated
// items need no re-migration (design §6).
func TestDeriveLevels_ZeroGrantsBootstrapFallback(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	levels, until := DeriveLevels(nil, ba, testBoardD)
	if len(levels) != 1 || levels[ba] != LevelMaintainer {
		t.Errorf("zero-grant fallback: levels = %v, want {%s:2}", levels, ba)
	}
	if until[ba] != authoritativeForever {
		t.Errorf("board author until = %d, want +inf", until[ba])
	}
}

// TestDeriveLevels_TamperedGrantIgnored proves the schnorr gate is live: mutating a
// signed grant's role tag (without re-signing) invalidates it, so it is dropped.
func TestDeriveLevels_TamperedGrantIgnored(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	subject := testKey(t)

	e := grant(t, owner, ba, subject.PubKeyHex(), RoleContributor, 0, 1000)
	// Tamper: escalate the role tag AFTER signing — id/sig no longer match.
	for _, tag := range e.Tags {
		if len(tag) >= 2 && tag[0] == "role" {
			tag[1] = RoleMaintainer
		}
	}
	if err := e.Verify(); err == nil {
		t.Fatal("precondition: tampered event should fail Verify")
	}
	levels, _ := DeriveLevels([]*nostr.Event{e}, ba, testBoardD)
	if _, ok := levels[subject.PubKeyHex()]; ok {
		t.Errorf("tampered grant MUST be dropped; subject present with level %d", levels[subject.PubKeyHex()])
	}
}

// TestRoleGrantBuildParseRoundTrip proves a built 39301 event carries exactly the
// wire fields the spec mandates and parses back losslessly.
func TestRoleGrantBuildParseRoundTrip(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	subject := testKey(t)

	e, err := BuildRoleGrantEvent(owner, RoleGrantSpec{
		BoardD:      testBoardD,
		BoardAuthor: ba,
		Grantee:     subject.PubKeyHex(),
		Role:        RoleMaintainer,
		From:        1234,
		Label:       "pm-agent",
	}, 9000)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if e.Kind != KindRoleGrant {
		t.Errorf("kind = %d, want %d", e.Kind, KindRoleGrant)
	}
	if got := tagValue(e, "d"); got != testBoardD+":"+subject.PubKeyHex() {
		t.Errorf("d tag = %q, want %q", got, testBoardD+":"+subject.PubKeyHex())
	}
	if got := tagValue(e, "p"); got != subject.PubKeyHex() {
		t.Errorf("p tag = %q, want grantee", got)
	}
	if got := tagValue(e, "a"); got != BoardCoord(ba, testBoardD) {
		t.Errorf("a tag = %q, want %q", got, BoardCoord(ba, testBoardD))
	}
	if err := e.Verify(); err != nil {
		t.Fatalf("built event must verify: %v", err)
	}

	g, ok := parseRoleGrant(e)
	if !ok {
		t.Fatal("parseRoleGrant returned ok=false for a well-formed event")
	}
	if g.Signer != ba || g.Grantee != subject.PubKeyHex() || g.Role != RoleMaintainer {
		t.Errorf("parsed grant mismatch: %+v", g)
	}
	if g.BoardOwner != ba || g.BoardD != testBoardD {
		t.Errorf("parsed board coord mismatch: owner=%s d=%s", g.BoardOwner, g.BoardD)
	}
	if g.From != 1234 {
		t.Errorf("parsed from = %d, want 1234", g.From)
	}
	if g.Label != "pm-agent" {
		t.Errorf("parsed label = %q, want pm-agent", g.Label)
	}
}

// TestBuildRoleGrantEvent_RejectsBadInput proves the builder validates its inputs
// (empty fields, unknown role) rather than emitting a malformed event.
func TestBuildRoleGrantEvent_RejectsBadInput(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	good := RoleGrantSpec{BoardD: testBoardD, BoardAuthor: ba, Grantee: "abc", Role: RoleContributor}

	bad := map[string]RoleGrantSpec{
		"empty board":   {BoardAuthor: ba, Grantee: "abc", Role: RoleContributor},
		"empty author":  {BoardD: testBoardD, Grantee: "abc", Role: RoleContributor},
		"empty grantee": {BoardD: testBoardD, BoardAuthor: ba, Role: RoleContributor},
		"bad role":      {BoardD: testBoardD, BoardAuthor: ba, Grantee: "abc", Role: "superuser"},
	}
	for name, spec := range bad {
		if _, err := BuildRoleGrantEvent(owner, spec, 1000); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
	if _, err := BuildRoleGrantEvent(owner, good, 1000); err != nil {
		t.Errorf("good spec should build: %v", err)
	}
}
