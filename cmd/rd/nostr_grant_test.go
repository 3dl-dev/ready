package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// TestAllowlistFileRoundtrip proves writeAllowlistFile emits stable, sorted-key JSON
// that readAllowlistFile parses back identically — the on-disk format the relay
// plugin reads is preserved byte-for-byte across a regenerate cycle.
func TestAllowlistFileRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "write-allowlist.json")
	in := map[string]string{
		"bbbb": "second",
		"aaaa": "first",
		"cccc": "third",
	}
	if err := writeAllowlistFile(path, in); err != nil {
		t.Fatalf("writeAllowlistFile: %v", err)
	}
	// Sorted-key, stable output.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"aaaa\": \"first\",\n  \"bbbb\": \"second\",\n  \"cccc\": \"third\"\n}\n"
	if string(got) != want {
		t.Errorf("file =\n%q\nwant\n%q", got, want)
	}
	back, err := readAllowlistFile(path)
	if err != nil {
		t.Fatalf("readAllowlistFile: %v", err)
	}
	if !reflect.DeepEqual(back, in) {
		t.Errorf("roundtrip = %v, want %v", back, in)
	}
}

// TestReadAllowlistFile_MissingIsEmpty proves a missing allowlist file reads as an
// empty map (not an error), so a first run has a clean empty baseline.
func TestReadAllowlistFile_MissingIsEmpty(t *testing.T) {
	m, err := readAllowlistFile(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("missing file should be empty map, got %v", m)
	}
}

func TestSplitCSV(t *testing.T) {
	cases := map[string][]string{
		"relay-a,relay-b": {"relay-a", "relay-b"},
		" a , b ,,c ":               {"a", "b", "c"},
		"":                          nil,
		"only":                      {"only"},
	}
	for in, want := range cases {
		if got := splitCSV(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitCSV(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestResolveBoardAuthorD_PinnedWins proves that when a board is pinned in
// .ready/config.json, resolveBoardAuthorD returns the PINNED owner/boardD (not the
// signer / project prefix) — so a grant binds to the owner's authority chain even
// when signed by a non-owner actor.
func TestResolveBoardAuthorD_PinnedWins(t *testing.T) {
	dir := t.TempDir()
	owner := "a9f766ae56bbf466d2d361e5b1788b7cd689fd8e3b418e35b002b313f478db25"
	cfg := &rdconfig.SyncConfig{Board: "30301:" + owner + ":ready"}
	if err := rdconfig.SaveSyncConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	gotOwner, gotD, err := resolveBoardAuthorD(dir, "deadbeef")
	if err != nil {
		t.Fatalf("resolveBoardAuthorD: %v", err)
	}
	if gotOwner != owner || gotD != "ready" {
		t.Errorf("resolveBoardAuthorD = (%s,%s), want (%s,ready)", gotOwner, gotD, owner)
	}
}

// TestPublishRoleGrant_NonMaintainerRejectedClientSide is the MED-6 proof: a signer
// that is NOT the board owner and holds NO maintainer grant is rejected CLIENT-SIDE
// when it runs `rd nostr grant/revoke` — a clear early error, not a silently-ignored
// grant. The project's board is pinned to a FOREIGN owner, so the local signer is a
// plain contributor (absent from the derived level map) and MayGrant returns false.
func TestPublishRoleGrant_NonMaintainerRejectedClientSide(t *testing.T) {
	dir, _ := setupNostrNativeProject(t)

	// Re-pin the board to a FOREIGN owner so the local signer is not the board author
	// and has no maintainer grant.
	foreign, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey (foreign owner): %v", err)
	}
	boardD := projectPrefix(dir)
	cfg, err := rdconfig.LoadSyncConfig(dir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	cfg.Board = rdSync.BoardCoord(foreign.PubKeyHex(), boardD)
	if err := rdconfig.SaveSyncConfig(dir, cfg); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}

	grantee, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey (grantee): %v", err)
	}

	// A plain contributor attempting to grant a contributor must be rejected client-side.
	_, err = publishRoleGrant(grantee.PubKeyHex(), rdSync.RoleContributor, "", 0, "")
	if err == nil || !strings.Contains(err.Error(), "escalation cap") {
		t.Fatalf("non-maintainer grant = %v, want an 'escalation cap' client-side rejection", err)
	}
	// And attempting to revoke must be rejected the same way.
	_, err = publishRoleGrant(grantee.PubKeyHex(), rdSync.RoleRevoked, "", 0, "")
	if err == nil || !strings.Contains(err.Error(), "escalation cap") {
		t.Fatalf("non-maintainer revoke = %v, want an 'escalation cap' client-side rejection", err)
	}
	assertNoDotCf(t)
}

// TestPublishRoleGrant_UppercaseGrantee_PublishesLowercase pins nostr_grant.go's
// publishRoleGrant normalization IN ISOLATION (ready-3e1 rework): it calls
// publishRoleGrant directly, bypassing runNostrGrantRevoke entirely (which has
// its own, separate normalization at cmd/rd/authz_nostr.go), so this test goes
// RED if publishRoleGrant's `grantee = normalizeHexPubkey(grantee)` reverts —
// independent of the caller-side normalization. `go test ./cmd/rd/...` staying
// green with only this site reverted (verified before writing this test) was
// because every existing test reaches publishRoleGrant through
// runNostrGrantRevoke, which re-normalizes before calling in; this test is the
// one that does not go through that door.
func TestPublishRoleGrant_UppercaseGrantee_PublishesLowercase(t *testing.T) {
	dir, _ := setupNostrNativeProject(t)

	granteeKey, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	lower := granteeKey.PubKeyHex()
	upper := strings.ToUpper(lower)

	if _, err := publishRoleGrant(upper, rdSync.RoleContributor, "", 0, ""); err != nil {
		t.Fatalf("publishRoleGrant(%q): %v", upper, err)
	}

	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll log: %v", err)
	}
	var found bool
	for _, e := range events {
		if e.Kind != rdSync.KindRoleGrant {
			continue
		}
		p, hasP := tagVal(e.Tags, "p")
		if !hasP {
			continue
		}
		if p == upper {
			t.Fatalf("published grant's p tag is %q (as-typed uppercase) — publishRoleGrant must normalize to canonical lowercase before signing", p)
		}
		if p == lower {
			found = true
		}
	}
	if !found {
		t.Fatalf("no published kind-39301 grant carries the grantee's canonical lowercase pubkey %q", lower)
	}
}

// TestPublishRoleGrant_ClaimSingleUse is the ready-ce0 security-property (c) proof at
// the CLI seam: the owner binds a first self-minted key to claim-nonce N (ok); a
// SECOND grant reusing the SAME N for a DIFFERENT key is REFUSED client-side (one
// claim-nonce admits exactly one pubkey). Re-granting the SAME key under its own N is
// allowed (e.g. a later role change).
func TestPublishRoleGrant_ClaimSingleUse(t *testing.T) {
	setupNostrNativeProject(t)
	const claim = "cli-claim-01"
	// ready-c40: a --claim grant is now only honored for a nonce this owner
	// actually minted via `rd invite` (recorded in unclaimed-invites). Mint the
	// record here so this test continues to exercise single-use REUSE rejection,
	// not the (separately covered) unminted-nonce rejection.
	if err := appendLocalClaim(unclaimedInvitesPath(RDHome()), localClaim{
		Claim: claim, Board: "irrelevant-for-this-check", ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatalf("appendLocalClaim: %v", err)
	}
	a, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey a: %v", err)
	}
	b, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey b: %v", err)
	}
	if _, err := publishRoleGrant(a.PubKeyHex(), rdSync.RoleContributor, "a", 0, claim); err != nil {
		t.Fatalf("first --claim grant should succeed: %v", err)
	}
	_, err = publishRoleGrant(b.PubKeyHex(), rdSync.RoleContributor, "b", 0, claim)
	if err == nil || !strings.Contains(err.Error(), "already consumed") {
		t.Fatalf("second grant reusing claim = %v, want 'already consumed' refusal", err)
	}
	if _, err := publishRoleGrant(a.PubKeyHex(), rdSync.RoleContributor, "a2", 0, claim); err != nil {
		t.Fatalf("same-key re-grant under its own claim should succeed: %v", err)
	}
	assertNoDotCf(t)
}

// grantNeverAdmitted re-reads dir's authoritative log and folds it through the
// SAME projection the client trusts (DeriveReadTrust), proving a rejected --claim
// grant never advanced past publishRoleGrant's error into a state where grantee
// is admitted — not merely that the call returned an error.
func grantNeverAdmitted(t *testing.T, dir, boardAuthor, boardD, grantee string) {
	t.Helper()
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("reading local log for post-rejection fold: %v", err)
	}
	if rdSync.DeriveReadTrust(events, boardAuthor, boardD)[grantee] {
		t.Errorf("grantee %s is in the derived read-trust set after a rejected --claim grant — the rejected grant was folded in anyway", grantee)
	}
	for _, e := range events {
		if e.Kind == rdSync.KindRoleGrant && eventTagValue(e, "p") == grantee {
			t.Errorf("a role-grant event for %s was appended to the local log despite the rejection: id=%s", grantee, e.ID)
		}
	}
}

// TestPublishRoleGrant_RejectsUnmintedClaim is the ready-c40 reproduction (half
// a): security sweep ready-348 found that `rd grant --claim <nonce>` bound an
// arbitrary caller-supplied nonce string to a grantee with NO check that this
// owner ever minted that nonce via `rd invite` (which records it in
// unclaimed-invites, cmd/rd/nostr_invite.go:445). Before the fix, an owner
// SOCIAL-ENGINEERED into running `rd grant --claim <attacker-chosen-string>
// <attacker-pubkey>` would confer write access on a nonce that was never issued
// at all. The fix requires a matching unclaimed-invites record before a --claim
// grant is honored.
func TestPublishRoleGrant_RejectsUnmintedClaim(t *testing.T) {
	dir, owner := setupNostrNativeProject(t)
	boardD := projectPrefix(dir)
	const neverMinted = "this-nonce-was-never-issued-by-rd-invite"

	grantee, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	_, err = publishRoleGrant(grantee.PubKeyHex(), rdSync.RoleContributor, "attacker-induced", 0, neverMinted)
	if err == nil {
		t.Fatal("grant with an unminted claim-nonce succeeded; want rejection")
	}
	if !strings.Contains(err.Error(), "no matching mint record") && !strings.Contains(err.Error(), "never minted") {
		t.Errorf("rejection reason = %q, want it to name the missing mint record", err.Error())
	}
	grantNeverAdmitted(t, dir, owner, boardD, grantee.PubKeyHex())
	assertNoDotCf(t)
}

// TestPublishRoleGrant_RejectsExpiredClaim is the ready-c40 reproduction (half
// b): the claim token's TTL is enforced ONLY on the join side
// (decodeNostrClaimToken, cmd/rd/nostr_invite.go:133 and redeemNostrClaimToken,
// :218) — the grant side never checked the mint record's expiry at all. A claim
// nonce minted (and expired) hours ago must be rejected here too, the step that
// actually confers write authority.
func TestPublishRoleGrant_RejectsExpiredClaim(t *testing.T) {
	dir, owner := setupNostrNativeProject(t)
	boardD := projectPrefix(dir)
	const claim = "expired-invite-nonce"

	if err := appendLocalClaim(unclaimedInvitesPath(RDHome()), localClaim{
		Claim: claim, Board: rdSync.BoardCoord(owner, boardD), ExpiresAt: time.Now().Add(-time.Hour).Unix(),
	}); err != nil {
		t.Fatalf("appendLocalClaim: %v", err)
	}

	grantee, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	_, err = publishRoleGrant(grantee.PubKeyHex(), rdSync.RoleContributor, "late-grant", 0, claim)
	if err == nil {
		t.Fatal("grant with an expired claim-nonce mint record succeeded; want rejection")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("rejection reason = %q, want it to say the nonce expired", err.Error())
	}
	grantNeverAdmitted(t, dir, owner, boardD, grantee.PubKeyHex())
	assertNoDotCf(t)
}

// TestResolveBoardAuthorD_UnpinnedFallsBackToSigner proves that with no pin,
// resolveBoardAuthorD falls back to (signer, projectPrefix) — the owner signing their
// own board (pre-pin behaviour, zero migration).
func TestResolveBoardAuthorD_UnpinnedFallsBackToSigner(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "ready")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gotOwner, gotD, err := resolveBoardAuthorD(dir, "cafebabe")
	if err != nil {
		t.Fatalf("resolveBoardAuthorD: %v", err)
	}
	if gotOwner != "cafebabe" || gotD != "ready" {
		t.Errorf("resolveBoardAuthorD = (%s,%s), want (cafebabe,ready)", gotOwner, gotD)
	}
}

// TestLinkCmd_UppercaseOwner_PinsCanonicalCoordinate is ready-3e1 at `rd link`'s
// owner input (runLinkOrPinBoard), the second of the two coordinate-writing entry
// points the item's "everywhere hex pubkeys are accepted" clause covers.
//
// `rd link` takes the owner from as-typed human input in two forms — the
// positional 30301:<owner>:<d> coordinate and --owner — and both are validated by
// the case-insensitive isHex. Unnormalized, the coordinate it builds is written to
// .ready/config.json AND the COMMITTED .ready/board.json, and every consumer
// matches it byte-for-byte against a board event's author pubkey, which is always
// lowercase. So an uppercase owner pins the repo to a coordinate that resolves to
// no board — reads come back empty, writes bind to a board nobody else reads — and
// because board.json is committed, the dead pin travels to every clone. `rd link`
// prints "linked board: ..." either way.
//
// Reverting nostr_grant.go's `owner = normalizeHexPubkey(owner)` turns this red
// for both input forms.
func TestLinkCmd_UppercaseOwner_PinsCanonicalCoordinate(t *testing.T) {
	cases := []struct {
		name string
		// args/flag as `rd link` would receive them, given the UPPERCASE owner.
		invoke func(t *testing.T, upper, boardD string) error
	}{
		{
			name: "positional 30301:<UPPERCASE-owner>:<d>",
			invoke: func(t *testing.T, upper, boardD string) error {
				t.Helper()
				return runLinkOrPinBoard(nostrLinkCmd, []string{"30301:" + upper + ":" + boardD})
			},
		},
		{
			name: "--owner <UPPERCASE>",
			invoke: func(t *testing.T, upper, boardD string) error {
				t.Helper()
				if err := nostrLinkCmd.Flags().Set("owner", upper); err != nil {
					t.Fatalf("set --owner: %v", err)
				}
				if err := nostrLinkCmd.Flags().Set("board-d", boardD); err != nil {
					t.Fatalf("set --board-d: %v", err)
				}
				t.Cleanup(func() {
					_ = nostrLinkCmd.Flags().Set("owner", "")
					_ = nostrLinkCmd.Flags().Set("board-d", "")
				})
				return runLinkOrPinBoard(nostrLinkCmd, nil)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := setupNostrCmdTest(t)

			// A REAL owner key that published a REAL board event — so "the
			// coordinate must resolve" is a fact about signed data, not a string
			// comparison the test invented.
			ownerKey, err := nostr.GenerateKey()
			if err != nil {
				t.Fatalf("GenerateKey: %v", err)
			}
			owner := ownerKey.PubKeyHex()
			boardD := "shared"
			be, err := rdSync.BuildBoardEvent(ownerKey, rdSync.BoardSpec{
				BoardD: boardD, Title: boardD, Maintainers: []string{owner},
			}, time.Now().Unix())
			if err != nil {
				t.Fatalf("BuildBoardEvent: %v", err)
			}
			if _, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).AppendUnique([]*nostr.Event{be}); err != nil {
				t.Fatalf("append board event: %v", err)
			}
			wantCoord := rdSync.BoardCoord(owner, boardD)

			if err := tc.invoke(t, strings.ToUpper(owner), boardD); err != nil {
				t.Fatalf("rd link with an UPPERCASE owner: %v", err)
			}

			cfg, err := rdconfig.LoadSyncConfig(dir)
			if err != nil {
				t.Fatalf("LoadSyncConfig: %v", err)
			}
			if cfg.Board != wantCoord {
				t.Errorf(".ready/config.json board = %q, want the canonical %q", cfg.Board, wantCoord)
			}
			binding, err := rdconfig.LoadBoardBinding(dir)
			if err != nil {
				t.Fatalf("LoadBoardBinding: %v", err)
			}
			if binding.Board != wantCoord {
				t.Errorf("COMMITTED .ready/board.json board = %q, want the canonical %q — this file is "+
					"version-controlled, so a non-canonical coordinate is a dead pin in every clone",
					binding.Board, wantCoord)
			}
			if got := nostrPinnedBoard(dir); got != wantCoord {
				t.Errorf("nostrPinnedBoard = %q, want %q", got, wantCoord)
			}

			// The pin RESOLVES: the board author derived from it is the key that
			// actually signed the board event, which is what binds this repo's
			// cards and grants to a live board rather than to nothing.
			gotAuthor, gotD, err := resolveBoardAuthorD(dir, "deadbeef")
			if err != nil {
				t.Fatalf("resolveBoardAuthorD: %v", err)
			}
			if gotAuthor != owner || gotD != boardD {
				t.Errorf("resolveBoardAuthorD = (%s,%s), want (%s,%s) — the pinned owner must equal the "+
					"board event's signer", gotAuthor, gotD, owner, boardD)
			}
			if got := rdSync.DiscoverOwnerBoards([]*nostr.Event{be}, []string{gotAuthor}, ""); len(got) != 1 || got[0] != wantCoord {
				t.Errorf("the pinned owner discovers boards %v, want [%s] — the pinned coordinate names "+
					"no board that exists", got, wantCoord)
			}
		})
	}
}
