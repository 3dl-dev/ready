package main

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// rewriteLog replaces the project's nostr log with exactly events, in order. Test
// scaffolding for manufacturing a historical on-disk state the current code can no
// longer produce.
func rewriteLog(t *testing.T, dir string, events []*nostr.Event) error {
	t.Helper()
	var b strings.Builder
	for _, e := range events {
		raw, err := json.Marshal(e)
		if err != nil {
			return err
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	return os.WriteFile(rdSync.NostrLogPath(dir), []byte(b.String()), 0o600)
}

// forceSharedSlotGrants rewrites every CEK-bearing grant in the log back to the
// PRE-ready-889 shared slot "<boardD>:<grantee>", re-signing with the owner key,
// and returns how many it rewrote.
//
// This manufactures the state the production ready board is actually in: grants
// minted by a build that predates per-epoch slots. Without it the recovery command
// has nothing to recover and its test would be vacuous — the current code never
// writes a shared-slot CEK grant again.
func forceSharedSlotGrants(t *testing.T, dir string, ownerKey *nostr.Key, boardD string) int {
	t.Helper()
	log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
	events, err := log.ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var rewritten []*nostr.Event
	n := 0
	for _, e := range events {
		if e.Kind != rdSync.KindRoleGrant || tagVal1(e, "cek") == "" {
			continue
		}
		grantee := tagVal1(e, "p")
		shared := boardD + ":" + grantee
		if tagVal1(e, "d") == shared {
			continue
		}
		clone := &nostr.Event{Kind: e.Kind, CreatedAt: e.CreatedAt, Content: e.Content}
		for _, tg := range e.Tags {
			if len(tg) >= 2 && tg[0] == "d" {
				clone.Tags = append(clone.Tags, []string{"d", shared})
				continue
			}
			clone.Tags = append(clone.Tags, append([]string(nil), tg...))
		}
		if err := clone.Sign(ownerKey); err != nil {
			t.Fatalf("sign shared-slot clone: %v", err)
		}
		rewritten = append(rewritten, clone)
		n++
	}
	if n == 0 {
		return 0
	}
	// Drop the per-epoch originals so ONLY the shared-slot form exists, exactly as
	// it would on a board whose grants were all written by the older build.
	var kept []*nostr.Event
	for _, e := range events {
		if e.Kind == rdSync.KindRoleGrant && tagVal1(e, "cek") != "" {
			continue
		}
		kept = append(kept, e)
	}
	if err := rewriteLog(t, dir, append(kept, rewritten...)); err != nil {
		t.Fatalf("rewrite log: %v", err)
	}
	return n
}

// TestRepublishEpochsRestoresRelayOnlyReads is the done condition of ready-12c:
// after the recovery, a reader holding ONLY what a relay serves decrypts the
// pre-rotation cards again.
//
// The board is put into the exact production state first — rotate, then force
// every CEK grant back to the shared slot — so the relay-retention model drops the
// old epoch just as the live relay did.
func TestRepublishEpochsRestoresRelayOnlyReads(t *testing.T) {
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
	const preTitle = "sealed under epoch 1 and stranded by the rotation"
	preID, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: preTitle, context: "pre-rotation", itemType: "task", priority: "p1",
	})
	if err != nil {
		t.Fatalf("create pre-rotation item: %v", err)
	}
	if _, _ = rotateOnce(t, dir, owner, boardD); true {
		if n := forceSharedSlotGrants(t, dir, ownerKey, boardD); n == 0 {
			t.Fatal("no CEK grants were forced back to the shared slot — the production state was not reproduced")
		}
	}

	// Confirm the damage before repairing it: relay-retained state has lost epoch 1.
	full, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if kr := rdSync.DeriveBoardKeyring(relayRetained(full), ownerKey, owner, boardD); func() bool {
		_, ok := kr.CEK(coord, 1)
		return ok
	}() {
		t.Fatal("epoch 1 still present in relay-retained state — the stranding was not reproduced, so the repair proves nothing")
	}

	// Repair.
	if err := confidentialRepublishEpochCmd.Flags().Set("no-verify", "true"); err != nil {
		t.Fatalf("set --no-verify: %v", err)
	}
	t.Cleanup(func() {
		confidentialRepublishEpochCmd.Flags().Set("no-verify", "false") //nolint:errcheck // test cleanup
		confidentialRepublishEpochCmd.Flags().Set("dry-run", "false")   //nolint:errcheck // test cleanup
	})
	out := captureStdoutPipe(t, func() {
		if err := confidentialRepublishEpochCmd.RunE(confidentialRepublishEpochCmd, nil); err != nil {
			t.Fatalf("republish-epochs: %v", err)
		}
	})
	if !strings.Contains(out, ":e1") {
		t.Fatalf("output does not name the per-epoch slot it re-addressed to:\n%s", out)
	}

	// The done condition: relay-only readers get every epoch back, and the
	// pre-rotation card reads as plaintext again.
	repaired, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	retained := relayRetained(repaired)
	for name, reader := range map[string]*nostr.Key{"owner": ownerKey, "member": member} {
		kr := rdSync.DeriveBoardKeyring(retained, reader, owner, boardD)
		if _, ok := kr.CEK(coord, 1); !ok {
			t.Fatalf("%s still holds no epoch-1 CEK from relay-only state — the recovery did not work", name)
		}
		if _, ok := kr.CEK(coord, 2); !ok {
			t.Fatalf("%s lost epoch 2 during the recovery", name)
		}
	}
	items := projectFrom(t, retained, ownerKey, owner, boardD)
	if got := items[preID]; got == nil || got.Title != preTitle {
		t.Fatalf("pre-rotation item from relay-only state = %q, want %q", titleOrMissing(items, preID), preTitle)
	}
}

// TestRepublishPreservesKeyBytesAndTimestamp pins the two properties that make
// this recovery safe to point at a production board.
//
// The wrapped CEK is copied VERBATIM — the recovery re-addresses, it does not
// re-key, so a grantee receives the bytes the owner originally sealed to it and a
// machine that cannot open a wrap can still restore it faithfully. And created_at
// is PRESERVED, because a grant's authz weight is latest-wins per grantee by
// (created_at, id) across every slot: stamping the copy "now" would float an old
// grant to the top of that ordering and could resurrect authority a later revoke
// had removed.
func TestRepublishPreservesKeyBytesAndTimestamp(t *testing.T) {
	dir, owner := setupConfidentialProject(t)
	boardD := projectPrefix(dir)
	ownerKey, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	if _, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "bootstrap", itemType: "task", priority: "p2",
	}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if n := forceSharedSlotGrants(t, dir, ownerKey, boardD); n == 0 {
		t.Fatal("nothing forced to the shared slot")
	}
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	plan := planEpochRepublish(events, owner, boardD, 0)
	if len(plan) == 0 {
		t.Fatal("planEpochRepublish found nothing to re-address")
	}
	for _, g := range plan {
		ev, err := buildRepublishedGrant(ownerKey, g, owner, boardD)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if got, want := tagVal1(ev, "cek"), tagVal1(g.Src, "cek"); got != want {
			t.Errorf("wrapped CEK was not copied verbatim for %s", g.Grantee)
		}
		if got, want := tagVal1(ev, "ltk"), tagVal1(g.Src, "ltk"); got != want {
			t.Errorf("wrapped LTK was not copied verbatim for %s", g.Grantee)
		}
		if ev.CreatedAt != g.Src.CreatedAt {
			t.Errorf("created_at %d != original %d — the copy would re-order authz", ev.CreatedAt, g.Src.CreatedAt)
		}
		if got, want := tagVal1(ev, "role"), tagVal1(g.Src, "role"); got != want {
			t.Errorf("role changed: %q -> %q", want, got)
		}
		if got, want := tagVal1(ev, "p"), g.Grantee; got != want {
			t.Errorf("grantee changed: %q -> %q", want, got)
		}
		wantD := boardD + ":" + g.Grantee + ":e" + strconv.Itoa(g.Epoch)
		if got := tagVal1(ev, "d"); got != wantD {
			t.Errorf("d = %q, want %q", got, wantD)
		}
		if err := ev.Verify(); err != nil {
			t.Errorf("re-addressed grant does not verify: %v", err)
		}
	}

	// Idempotent: once re-addressed, a second run finds nothing.
	if err := confidentialRepublishEpochCmd.Flags().Set("no-verify", "true"); err != nil {
		t.Fatalf("set --no-verify: %v", err)
	}
	t.Cleanup(func() {
		confidentialRepublishEpochCmd.Flags().Set("no-verify", "false") //nolint:errcheck // test cleanup
	})
	captureStdoutPipe(t, func() {
		if err := confidentialRepublishEpochCmd.RunE(confidentialRepublishEpochCmd, nil); err != nil {
			t.Fatalf("first run: %v", err)
		}
	})
	out := captureStdoutPipe(t, func() {
		if err := confidentialRepublishEpochCmd.RunE(confidentialRepublishEpochCmd, nil); err != nil {
			t.Fatalf("second run: %v", err)
		}
	})
	if !strings.Contains(out, "nothing stranded") {
		t.Fatalf("a second run is not a no-op — the recovery is not idempotent:\n%s", out)
	}
}

// TestRepublishDryRunPublishesNothing: the preview must be a preview.
func TestRepublishDryRunPublishesNothing(t *testing.T) {
	dir, owner := setupConfidentialProject(t)
	boardD := projectPrefix(dir)
	ownerKey, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	if _, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "bootstrap", itemType: "task", priority: "p2",
	}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if n := forceSharedSlotGrants(t, dir, ownerKey, boardD); n == 0 {
		t.Fatal("nothing forced to the shared slot")
	}
	before := logSnapshot(t, dir)

	for flag, val := range map[string]string{"dry-run": "true", "no-verify": "true"} {
		if err := confidentialRepublishEpochCmd.Flags().Set(flag, val); err != nil {
			t.Fatalf("set --%s: %v", flag, err)
		}
	}
	t.Cleanup(func() {
		confidentialRepublishEpochCmd.Flags().Set("dry-run", "false")   //nolint:errcheck // test cleanup
		confidentialRepublishEpochCmd.Flags().Set("no-verify", "false") //nolint:errcheck // test cleanup
	})
	out := captureStdoutPipe(t, func() {
		if err := confidentialRepublishEpochCmd.RunE(confidentialRepublishEpochCmd, nil); err != nil {
			t.Fatalf("dry run: %v", err)
		}
	})
	if !strings.Contains(out, "nothing published") {
		t.Errorf("dry run does not say it published nothing:\n%s", out)
	}
	after := logSnapshot(t, dir)
	if len(after) != len(before) {
		t.Fatalf("dry run wrote %d new event(s) to the log", len(after)-len(before))
	}
	_ = owner
}
