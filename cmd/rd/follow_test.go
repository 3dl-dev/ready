package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/identity"
	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	"github.com/3dl-dev/ready/pkg/state"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// TestFollow_BindsAllOwnerBoardsKeepingKey is the ready-636 DONE condition, driven
// end-to-end against a SEEDED snapshot (no live relay — followFetch is the injected
// seam, the ready-6d5 pattern). Setup: a scratch RD_HOME holding an EXISTING owner
// key that published 3 boards (each with an item) plus a person-alias mapping the
// owner email to that key. `rd follow baron@3dl.dev` must:
//
//   - bind ALL 3 boards, writing a committed .ready/board.json for each;
//   - publish exactly ONE person-alias;
//   - leave the pre-existing key UNCHANGED (never re-mint / overwrite — edge #1);
//   - import each board's full history so `rd ready` (ProjectItems) returns its item;
//   - never require or print a raw 64-hex coordinate the operator had to type.
func TestFollow_BindsAllOwnerBoardsKeepingKey(t *testing.T) {
	base := t.TempDir()
	rdHome := filepath.Join(base, "rdhome")
	if err := os.MkdirAll(rdHome, 0o700); err != nil {
		t.Fatalf("mkdir rdhome: %v", err)
	}
	t.Setenv("RD_HOME", rdHome)
	t.Setenv("RD_NOSTR_RELAY_URL", "")
	t.Setenv("RD_NOSTR", "")
	t.Setenv("RD_NOSTR_READ", "")

	// PRE-EXISTING owner key: whatever nostrKey() first-run creates in this RD_HOME
	// IS the owner identity we then seed boards under. Snapshot its bytes so we can
	// prove `rd follow` never rewrites it.
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	owner := k.PubKeyHex()
	keyPath, err := nostr.ActorKeyPath(rdHome, rdActor())
	if err != nil {
		t.Fatalf("ActorKeyPath: %v", err)
	}
	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("reading pre-existing key: %v", err)
	}

	const email = "baron@3dl.dev"
	boardNames := []string{"proj1", "proj2", "proj3"}

	// SEED the relay snapshot: one person-alias (email->owner, signed by owner, so
	// the local resolver trusts it since owner IS this machine's key), plus for each
	// board a signed 30301 board event, an owner-signed card, and a board-scoped
	// status event — the full history a fresh box would pull.
	var seeded []*nostr.Event
	alias, err := identity.BuildAliasEvent(k, identity.AliasSpec{
		Handle:  email,
		Pubkeys: []string{owner},
		Emails:  []string{email},
	}, 1000)
	if err != nil {
		t.Fatalf("BuildAliasEvent: %v", err)
	}
	seeded = append(seeded, alias)

	itemForBoard := map[string]string{}
	for i, name := range boardNames {
		ts := int64(1000 + i)
		be, err := rdSync.BuildBoardEvent(k, rdSync.BoardSpec{BoardD: name, Title: name, Maintainers: []string{owner}}, ts)
		if err != nil {
			t.Fatalf("BuildBoardEvent %s: %v", name, err)
		}
		itemID := "ready-" + name
		itemForBoard[name] = itemID
		coord := rdSync.BoardCoord(owner, name)
		card, err := rdSync.BuildCardEvent(k, rdSync.CardSpec{
			ItemID:      itemID,
			Title:       itemID,
			Status:      state.StatusActive,
			Type:        "task",
			BoardD:      name,
			BoardAuthor: owner,
		}, ts)
		if err != nil {
			t.Fatalf("BuildCardEvent %s: %v", name, err)
		}
		st, err := rdSync.BuildStatusEventWithIssueRoot(k, itemID, state.StatusActive, card.ID, "", coord, "", ts, nil)
		if err != nil {
			t.Fatalf("BuildStatusEvent %s: %v", name, err)
		}
		seeded = append(seeded, be, card, st)
	}

	// Inject the seeded snapshot as the relay medium (no network).
	origFetch := followFetch
	followFetch = func(_ context.Context, _ []string, _ map[string]any) ([]*nostr.Event, error) {
		return seeded, nil
	}
	t.Cleanup(func() { followFetch = origFetch })

	root := filepath.Join(base, "projects")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}

	rep, err := runFollow(followOpts{
		who:    email,
		email:  email,
		root:   root,
		relays: []string{"wss://seed.example.test"},
	})
	if err != nil {
		t.Fatalf("runFollow: %v", err)
	}

	// (a) KEY UNCHANGED: not minted, bytes byte-identical.
	if rep.MintedKey {
		t.Error("rd follow re-minted the machine key; it must KEEP an existing owner key (edge #1)")
	}
	keyAfter, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("reading key after follow: %v", err)
	}
	if string(keyBefore) != string(keyAfter) {
		t.Error("the pre-existing owner key file changed after rd follow; it must be left UNTOUCHED")
	}
	if rep.Pubkey != owner {
		t.Errorf("reported pubkey %s != pre-existing owner %s", rep.Pubkey, owner)
	}

	// (b) ALL 3 BOARDS BOUND, each with a committed board.json holding the coord.
	if len(rep.BoardDirs) != 3 {
		t.Fatalf("bound %d boards, want 3: %+v", len(rep.BoardDirs), rep.BoardDirs)
	}
	aliasCount := 0
	for _, name := range boardNames {
		dir := filepath.Join(root, name)
		wantCoord := rdSync.BoardCoord(owner, name)

		b, err := rdconfig.LoadBoardBinding(dir)
		if err != nil {
			t.Fatalf("LoadBoardBinding %s: %v", name, err)
		}
		if b.Board != wantCoord {
			t.Errorf("board.json[%s].Board = %q, want %q", name, b.Board, wantCoord)
		}
		if _, err := os.Stat(rdconfig.BoardBindingPath(dir)); err != nil {
			t.Errorf("no committed board.json for %s: %v", name, err)
		}

		// (c) FULL HISTORY IMPORTED: ProjectItems over the bound log returns the item
		// (this is what `rd ready` reads).
		events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
		if err != nil {
			t.Fatalf("ReadAll %s: %v", name, err)
		}
		for _, e := range events {
			if e.Kind == identity.KindPersonAlias {
				aliasCount++
			}
		}
		items := rdSync.ProjectItems(events, rdSync.ProjectOptions{
			Trusted:     map[string]bool{owner: true},
			PinnedBoard: wantCoord,
		})
		it := items[itemForBoard[name]]
		if it == nil {
			t.Errorf("board %s: item %q did not project — rd ready would show nothing", name, itemForBoard[name])
			continue
		}
		if it.Status != state.StatusActive {
			t.Errorf("board %s item status = %q, want %q", name, it.Status, state.StatusActive)
		}
	}

	// (d) EXACTLY ONE alias published across all bound logs.
	if aliasCount != 1 {
		t.Errorf("found %d person-alias events across bound board logs, want exactly 1", aliasCount)
	}
	if rep.AliasEventID == "" {
		t.Error("runFollow reported no alias event id; it must publish one person-alias")
	}
}

// TestFollow_NeverPrintsARawCoordinate asserts the human output uses the
// `rd grant --all-boards <pubkey>` line and never emits a 30301:<hex>:<d> board
// coordinate the operator would have to copy — the whole point of `rd follow`.
func TestFollow_NeverPrintsARawCoordinate(t *testing.T) {
	owner := randPubkey(t)
	rep := &followReport{
		Pubkey:    owner,
		MintedKey: false,
		BoardDirs: map[string]string{"proj1": "/tmp/projects/proj1", "proj2": "/tmp/projects/proj2"},
		Boards: []string{
			rdSync.BoardCoord(owner, "proj1"),
			rdSync.BoardCoord(owner, "proj2"),
		},
		AliasEventID: "deadbeef",
	}

	out := captureStdoutPipe(t, func() { printFollowReport(rep) })
	assertNoCoord(t, out, owner)
}

func assertNoCoord(t *testing.T, out, owner string) {
	t.Helper()
	if strings.Contains(out, "30301:") {
		t.Errorf("rd follow printed a raw board coordinate the operator must retype:\n%s", out)
	}
	wantGrant := "rd grant --all-boards " + owner
	if !strings.Contains(out, wantGrant) {
		t.Errorf("output missing the exact grant line %q:\n%s", wantGrant, out)
	}
}

// TestDecodeNpub_CanonicalVector decodes the canonical NIP-19 npub test vector to
// its 32-byte hex pubkey — proving the standalone bech32 decoder (no external dep)
// is correct, and that a corrupted npub is rejected on checksum.
func TestDecodeNpub_CanonicalVector(t *testing.T) {
	const npub = "npub1sn0wdenkukak0d9dfczzeacvhkrgz92ak56egt7vdgzn8pv2wfqqhrjdv9"
	const wantHex = "84dee6e676e5bb67b4ad4e042cf70cbd8681155db535942fcc6a0533858a7240"

	got, err := decodeNpub(npub)
	if err != nil {
		t.Fatalf("decodeNpub(canonical): %v", err)
	}
	if got != wantHex {
		t.Errorf("decodeNpub = %q, want %q", got, wantHex)
	}

	// A single-character corruption must fail the checksum, not decode to a
	// different-but-plausible owner.
	corrupt := npub[:len(npub)-1] + "0"
	if _, err := decodeNpub(corrupt); err == nil {
		t.Error("decodeNpub accepted a corrupted npub — checksum not enforced")
	}
}

// TestImportFollowedBoard_DropsForeignAndForgedEvents is the ready-50c fail-closed
// pin on importFollowedBoard's admit loop (~line 337-353): a hostile relay can
// serve (a) a validly-signed event authored by a FOREIGN key that is neither the
// board owner nor granted any role, and (b) a FORGED event whose pubkey field
// claims the owner's identity but whose signature does not verify against it.
// Both must be dropped before AppendUnique — Verify() catches the forgery,
// trusted[e.PubKey] (derived from DeriveReadTrust) catches the foreign key — so
// neither ever lands in the bound board's authoritative log. A legit owner-signed
// card in the same snapshot IS admitted, proving the loop is selective rather than
// accidentally dropping everything.
func TestImportFollowedBoard_DropsForeignAndForgedEvents(t *testing.T) {
	base := t.TempDir()
	t.Setenv("RD_HOME", filepath.Join(base, "rdhome"))
	t.Setenv("RD_NOSTR_RELAY_URL", "")
	t.Setenv("RD_NOSTR", "")
	t.Setenv("RD_NOSTR_READ", "")

	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey owner: %v", err)
	}
	attacker, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey attacker: %v", err)
	}
	ownerHex := owner.PubKeyHex()
	const boardD = "proj1"
	coord := rdSync.BoardCoord(ownerHex, boardD)

	// (a) LEGIT: owner-signed, board-scoped card. Must be admitted — the control
	// that proves the admit loop isn't just dropping everything.
	legit, err := rdSync.BuildCardEvent(owner, rdSync.CardSpec{
		ItemID: "ready-legit", Title: "legit", Status: state.StatusActive, Type: "task",
		BoardD: boardD, BoardAuthor: ownerHex,
	}, 1000)
	if err != nil {
		t.Fatalf("BuildCardEvent legit: %v", err)
	}

	// (b) FOREIGN KEY: validly signed by the ATTACKER (Verify() passes — nothing
	// wrong with the signature itself), board-scoped via BoardAuthor=owner so it
	// clears eventBelongsToFollowedBoard — but the attacker holds no board-owner
	// status and no role grant, so trusted[e.PubKey] must reject it.
	foreign, err := rdSync.BuildCardEvent(attacker, rdSync.CardSpec{
		ItemID: "ready-foreign", Title: "foreign", Status: state.StatusActive, Type: "task",
		BoardD: boardD, BoardAuthor: ownerHex,
	}, 1001)
	if err != nil {
		t.Fatalf("BuildCardEvent foreign: %v", err)
	}
	if err := foreign.Verify(); err != nil {
		t.Fatalf("foreign event must verify (it IS validly signed, just untrusted): %v", err)
	}

	// (c) FORGED: signed by the attacker, then the pubkey field is overwritten to
	// claim the OWNER's identity and the id recomputed to match the new canonical
	// form — but the signature is still the attacker's, over the ORIGINAL
	// (attacker-pubkey) id. An id-only check would be fooled; Verify() must reject
	// it because the schnorr signature does not verify against the claimed owner
	// pubkey.
	forged, err := rdSync.BuildCardEvent(attacker, rdSync.CardSpec{
		ItemID: "ready-forged", Title: "forged", Status: state.StatusActive, Type: "task",
		BoardD: boardD, BoardAuthor: ownerHex,
	}, 1002)
	if err != nil {
		t.Fatalf("BuildCardEvent forged: %v", err)
	}
	forged.PubKey = ownerHex
	forged.ID = forged.ComputeID()
	if err := forged.Verify(); err == nil {
		t.Fatal("forged event unexpectedly verifies — test fixture is not actually forged")
	}

	snapshot := []*nostr.Event{legit, foreign, forged}

	dir := filepath.Join(base, "board")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir board dir: %v", err)
	}

	// relays=nil: followFetch's board-scoped re-fetch and the best-effort
	// PublishBoard backfill both no-op on an empty relay set (no network, no
	// extra events reach the log) — see followFetch's empty-loop return and the
	// offline-Publisher pattern used elsewhere in this package.
	if err := importFollowedBoard(context.Background(), dir, coord, ownerHex, boardD, snapshot, nil); err != nil {
		t.Fatalf("importFollowedBoard: %v", err)
	}

	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	ids := map[string]bool{}
	for _, e := range events {
		ids[e.ID] = true
	}
	if !ids[legit.ID] {
		t.Error("legit owner-signed card was NOT admitted — admit loop is over-dropping")
	}
	if ids[foreign.ID] {
		t.Error("FOREIGN-key event was admitted into the bound board log — a hostile relay's untrusted-signer events must be dropped fail-closed")
	}
	if ids[forged.ID] {
		t.Error("FORGED event was admitted into the bound board log — a hostile relay's tampered-signature events must be dropped fail-closed")
	}
	if len(events) != 1 {
		t.Errorf("bound board log holds %d events, want exactly 1 (only the legit card): %+v", len(events), ids)
	}
}

// TestResolveFollowTarget_HostileAliasNeverResolvesEmail is the ready-50c email-case
// fail-closed pin (~line 263-272): a hostile relay serves a person-alias event
// mapping the owner's email to an ATTACKER pubkey, signed by the ATTACKER, not by
// self. Because identity.Resolve's trust root is self ALONE (v1 single-operator
// trust — package doc, pkg/identity/alias.go), an alias signed by any other key
// contributes nothing to the party graph, so resolveFollowTarget must refuse to
// resolve the email at all rather than silently binding to the attacker's boards
// under the owner's own email handle.
func TestResolveFollowTarget_HostileAliasNeverResolvesEmail(t *testing.T) {
	self, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey self: %v", err)
	}
	attacker, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey attacker: %v", err)
	}
	const email = "baron@3dl.dev"

	hostile, err := identity.BuildAliasEvent(attacker, identity.AliasSpec{
		Handle:  email,
		Pubkeys: []string{attacker.PubKeyHex()},
		Emails:  []string{email},
	}, 1000)
	if err != nil {
		t.Fatalf("BuildAliasEvent hostile: %v", err)
	}
	if err := hostile.Verify(); err != nil {
		t.Fatalf("hostile alias must verify (it IS validly signed by the attacker): %v", err)
	}

	keys, _, err := resolveFollowTarget(email, email, []*nostr.Event{hostile}, self.PubKeyHex())
	if err == nil {
		t.Fatalf("resolveFollowTarget resolved %q to %v via a HOSTILE relay-served alias signed by an untrusted attacker key — trust root must be self only", email, keys)
	}
	if len(keys) != 0 {
		t.Errorf("resolveFollowTarget returned owner keys %v on the error path; want none", keys)
	}

	// Control: the SAME email, with a LEGIT alias signed by self ALSO present (as
	// well as the hostile one), resolves to self's key only — proving the rejection
	// above is genuinely about trust (attacker excluded), not a broken email lookup.
	legit, err := identity.BuildAliasEvent(self, identity.AliasSpec{
		Handle:  email,
		Pubkeys: []string{self.PubKeyHex()},
		Emails:  []string{email},
	}, 1000)
	if err != nil {
		t.Fatalf("BuildAliasEvent legit: %v", err)
	}
	keys, _, err = resolveFollowTarget(email, email, []*nostr.Event{hostile, legit}, self.PubKeyHex())
	if err != nil {
		t.Fatalf("resolveFollowTarget with a legit self-signed alias present: %v", err)
	}
	if len(keys) != 1 || keys[0] != self.PubKeyHex() {
		t.Errorf("resolveFollowTarget = %v, want [%s] (self only, attacker excluded)", keys, self.PubKeyHex())
	}
}

// TestResolveFollowTarget_TokenAndHex covers the non-email identity forms: an rd1_
// token resolves to its board owner, and a bare 64-hex pubkey is taken verbatim.
func TestResolveFollowTarget_TokenAndHex(t *testing.T) {
	owner := randPubkey(t)
	self := randPubkey(t)

	// Bare hex.
	keys, _, err := resolveFollowTarget(owner, "e@x", nil, self)
	if err != nil || len(keys) != 1 || keys[0] != owner {
		t.Fatalf("hex resolve = %v, %v, want [%s]", keys, err, owner)
	}

	// rd1_ token: owner comes from the board coordinate, relays ride along.
	coord := rdSync.BoardCoord(owner, "proj1")
	tok, err := buildNostrClaimToken(coord, []string{"wss://r.example"}, "nonce123", 1, 1<<62, owner)
	if err != nil {
		t.Fatalf("buildNostrClaimToken: %v", err)
	}
	keys, relays, err := resolveFollowTarget(tok, "e@x", nil, self)
	if err != nil || len(keys) != 1 || keys[0] != owner {
		t.Fatalf("token resolve = %v, %v, want [%s]", keys, err, owner)
	}
	if len(relays) != 1 || relays[0] != "wss://r.example" {
		t.Errorf("token relays = %v, want [wss://r.example]", relays)
	}
}
