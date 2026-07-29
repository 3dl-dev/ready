package main

import (
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// projectFrom projects an explicit event set as reader, with reader's real
// keyring derived from that SAME set — so a relay-retained slice is read exactly
// as a machine holding only those events would read it.
func projectFrom(t *testing.T, events []*nostr.Event, reader *nostr.Key, owner, boardD string) map[string]*state.Item {
	t.Helper()
	dir := mustDir(t)
	kr := rdSync.DeriveBoardKeyring(events, reader, owner, boardD)
	return rdSync.ProjectItems(events, rdSync.ProjectOptions{
		Trusted:         nostrTrustSet(dir, reader.PubKeyHex()),
		PinnedBoard:     nostrPinnedBoard(dir),
		Decryptor:       kr,
		EncryptedBoards: kr,
	})
}

// titleOrMissing renders an item's title for a failure message, or a marker when
// the item is absent entirely.
func titleOrMissing(items map[string]*state.Item, id string) string {
	if it := items[id]; it != nil {
		return it.Title
	}
	return "<item missing from projection>"
}

// relayRetained returns the subset of events a NIP-01-conformant relay would
// still serve: for an ADDRESSABLE kind (30000–39999) only the newest event per
// (kind, pubkey, d) survives, and everything else is kept.
//
// This is the whole point of the test file. Every existing confidential test
// derives its keyring from the LOCAL append-only log, which keeps superseded
// events forever — so all of them passed while a rotation was silently deleting
// the old epoch's key from the one store that other machines actually read.
// Measuring against the local log cannot see that class of defect at all.
func relayRetained(events []*nostr.Event) []*nostr.Event {
	type slot struct {
		kind int
		pk   string
		d    string
	}
	newest := map[slot]*nostr.Event{}
	var out []*nostr.Event
	for _, e := range events {
		if e.Kind < 30000 || e.Kind > 39999 {
			out = append(out, e)
			continue
		}
		var d string
		for _, tg := range e.Tags {
			if len(tg) >= 2 && tg[0] == "d" {
				d = tg[1]
				break
			}
		}
		k := slot{e.Kind, e.PubKey, d}
		cur, seen := newest[k]
		// NIP-01: newer created_at wins; on a tie the LOWEST id wins.
		if !seen || e.CreatedAt > cur.CreatedAt || (e.CreatedAt == cur.CreatedAt && e.ID < cur.ID) {
			newest[k] = e
		}
	}
	for _, e := range newest {
		out = append(out, e)
	}
	return out
}

// TestRotationSurvivesRelayReplacement is the done condition of ready-889: after
// a rotation, a reader who bootstraps from what a RELAY retains — not from this
// machine's append-only log — must still hold the old epoch and still decrypt
// pre-rotation cards.
//
// Before per-epoch grant slots this was false in production, not just in theory.
// Every epoch reused d = "<boardD>:<grantee>", so the epoch-2 grant replaced the
// epoch-1 grant and the old CEK was deleted. Measured on the live public relay
// after one rotation of the ready board: four grants returned, all epoch 2, zero
// epoch 1, against 200 cards still sealed at epoch 1 — a relay-only reader could
// decrypt 6 of 206 confidential cards.
func TestRotationSurvivesRelayReplacement(t *testing.T) {
	dir, owner := setupConfidentialProject(t)
	boardD := projectPrefix(dir)
	coord := rdSync.BoardCoord(owner, boardD)
	ownerKey, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}

	member, _, _ := mintIdentity(t)
	if _, err := publishRoleGrant(member.PubKeyHex(), rdSync.RoleContributor, "member", 0, ""); err != nil {
		t.Fatalf("grant member: %v", err)
	}

	const preTitle = "sealed under epoch 1, must still be readable after the rotation"
	preID, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: preTitle, context: "pre-rotation", itemType: "task", priority: "p1",
	})
	if err != nil {
		t.Fatalf("create pre-rotation item: %v", err)
	}

	plan, _ := rotateOnce(t, dir, owner, boardD)
	if plan.OldEpoch != 1 || plan.NewEpoch != 2 {
		t.Fatalf("plan epochs = %d -> %d, want 1 -> 2", plan.OldEpoch, plan.NewEpoch)
	}
	const postTitle = "sealed under epoch 2"
	postID, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: postTitle, context: "post-rotation", itemType: "task", priority: "p1",
	})
	if err != nil {
		t.Fatalf("create post-rotation item: %v", err)
	}

	full, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	retained := relayRetained(full)

	// Sanity: the model actually drops something (a rotation DOES supersede the
	// authz slot), so a pass below is not the trivial "nothing was replaced".
	if len(retained) >= len(full) {
		t.Fatalf("relay model retained %d of %d events — it replaced nothing, so this test proves nothing", len(retained), len(full))
	}

	// The owner and the member each bootstrap from ONLY what the relay kept.
	for name, reader := range map[string]*nostr.Key{"owner": ownerKey, "member": member} {
		kr := rdSync.DeriveBoardKeyring(retained, reader, owner, boardD)
		cek1, ok1 := kr.CEK(coord, 1)
		if !ok1 {
			t.Fatalf("%s holds NO epoch-1 CEK after relay replacement — the rotation deleted the old key", name)
		}
		cek2, ok2 := kr.CEK(coord, 2)
		if !ok2 {
			t.Fatalf("%s holds no epoch-2 CEK after relay replacement", name)
		}
		if cek1 == cek2 {
			t.Fatalf("%s: epoch-1 and epoch-2 CEKs are byte-identical — rotation renumbered instead of re-keying", name)
		}
		if ep, _, ok := kr.CurrentEpoch(coord); !ok || ep != 2 {
			t.Fatalf("%s CurrentEpoch = %d (ok=%v), want 2", name, ep, ok)
		}
	}

	// End to end: project the relay-retained events as the OWNER and read both
	// cards' free text back. This is the assertion that would have caught the
	// production defect.
	items := projectFrom(t, retained, ownerKey, owner, boardD)
	if got := items[preID]; got == nil || got.Title != preTitle {
		t.Fatalf("pre-rotation item from relay-only state = %q, want %q", titleOrMissing(items, preID), preTitle)
	}
	if got := items[postID]; got == nil || got.Title != postTitle {
		t.Fatalf("post-rotation item from relay-only state = %q, want %q", titleOrMissing(items, postID), postTitle)
	}
}

// TestRevokeStillSupersedesCEKGrantAcrossSlots guards the ordering decision that
// per-epoch slots forced (ready-889).
//
// A CEK-bearing grant now lives in "<boardD>:<grantee>:e<epoch>" while a revoke
// lives in the bare "<boardD>:<grantee>" slot. If DriftScope kept keying a grant's
// causal chain off "d", those two would land in DIFFERENT monotonic scopes and the
// revoke would no longer be guaranteed to stamp after the grant it supersedes —
// while deriveGrants still replays latest-per-GRANTEE across ALL slots. A
// same-second revoke could then lose to the grant it was meant to revoke: the
// lost-revoke this scoping exists to prevent. DriftScope therefore keys off (a, p).
func TestRevokeStillSupersedesCEKGrantAcrossSlots(t *testing.T) {
	dir, owner := setupConfidentialProject(t)
	boardD := projectPrefix(dir)

	// The board must be confidential BEFORE the grant, or the grant carries no CEK
	// and lands in the bare slot like any plaintext grant — which would make this
	// test vacuous. The owner's first write bootstraps epoch 1.
	if _, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "bootstraps the board to epoch 1", itemType: "task", priority: "p1",
	}); err != nil {
		t.Fatalf("bootstrap item: %v", err)
	}

	member, _, _ := mintIdentity(t)
	pub := member.PubKeyHex()
	if _, err := publishRoleGrant(pub, rdSync.RoleContributor, "member", 0, ""); err != nil {
		t.Fatalf("grant member: %v", err)
	}
	if _, err := publishRoleGrant(pub, rdSync.RoleRevoked, "revoked", 0, ""); err != nil {
		t.Fatalf("revoke member: %v", err)
	}

	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	// The grant and the revoke must share one drift scope even though they occupy
	// different addressable slots — that is what makes the revoke's created_at
	// strictly later.
	var grantEv, revokeEv *nostr.Event
	for _, e := range events {
		if e.Kind != 39301 {
			continue
		}
		if p, _ := tagVal(e.Tags, "p"); p != pub {
			continue
		}
		if role, _ := tagVal(e.Tags, "role"); role == rdSync.RoleRevoked {
			revokeEv = e
		} else if cek, ok := tagVal(e.Tags, "cek"); ok && cek != "" {
			grantEv = e
		}
	}
	if grantEv == nil || revokeEv == nil {
		t.Fatalf("expected both a CEK-bearing grant and a revoke for the member (grant=%v revoke=%v)", grantEv != nil, revokeEv != nil)
	}
	gd, _ := tagVal(grantEv.Tags, "d")
	rd, _ := tagVal(revokeEv.Tags, "d")
	if gd == rd {
		t.Fatalf("the CEK grant and the revoke share slot %q — the per-epoch split did not happen, so this test is vacuous", gd)
	}
	if gs, rs := rdSync.DriftScope(grantEv), rdSync.DriftScope(revokeEv); gs != rs {
		t.Fatalf("drift scopes differ across slots: grant %q vs revoke %q — a same-second revoke can lose to the grant it supersedes", gs, rs)
	}
	if revokeEv.CreatedAt <= grantEv.CreatedAt {
		t.Fatalf("revoke created_at %d does not strictly follow the grant's %d", revokeEv.CreatedAt, grantEv.CreatedAt)
	}

	// And the revoke actually wins, from relay-retained state as well as the log.
	for name, evs := range map[string][]*nostr.Event{"log": events, "relay": relayRetained(events)} {
		levels, _ := rdSync.DeriveLevels(evs, owner, boardD)
		if lvl, ok := levels[pub]; !ok || lvl != 0 {
			t.Fatalf("%s state: revoked member has level %d (present=%v), want 0", name, lvl, ok)
		}
	}
}

// TestBootstrapRefusesWhenTheBoardKeyWasLost covers the other half of ready-889:
// per-epoch slots stop a ROTATION from destroying a key, but a lost log could
// still destroy one by minting over it.
//
// A board with sealed cards and no readable CEK grant is a confidential board
// whose key did not survive into this log — not a plaintext board about to become
// confidential. Bootstrapping a "fresh" epoch-1 key there installs a SECOND key at
// the same epoch and orphans every card sealed under the first, which is exactly
// how three cards on the ready board were permanently destroyed. The check is
// local so it holds offline: refusing on relay unreachability instead would break
// the offline-first write path for the common case (a genuinely new board).
func TestBootstrapRefusesWhenTheBoardKeyWasLost(t *testing.T) {
	dir, _ := setupConfidentialProject(t)

	if _, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "sealed under the original epoch-1 key", itemType: "task", priority: "p1",
	}); err != nil {
		t.Fatalf("create item: %v", err)
	}

	// Lose the key exactly as a rebuilt log or a fresh clone would: the sealed
	// cards remain, every CEK-bearing grant is gone.
	if dropped := dropEpochGrantsFromLog(t, dir, "1"); dropped == 0 {
		t.Fatal("no epoch-1 grants were dropped — the lost-key state was not reproduced")
	}

	_, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "this write must not mint a replacement key", itemType: "task", priority: "p1",
	})
	if err == nil {
		t.Fatal("the write SUCCEEDED — it just minted a second epoch-1 key over the one those cards were sealed under")
	}
	for _, want := range []string{"LOST, not absent", "orphan"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not explain the refusal (missing %q): %v", want, err)
		}
	}

	// And nothing was published: no new CEK-bearing grant reached the log.
	events, rerr := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	for _, e := range events {
		if e.Kind != 39301 {
			continue
		}
		if cek, ok := tagVal(e.Tags, "cek"); ok && cek != "" {
			t.Fatalf("a CEK-bearing grant was published despite the refusal (id %s)", e.ID[:8])
		}
	}
}
