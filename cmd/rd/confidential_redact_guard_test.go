package main

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// dropEpochGrantsFromLog rewrites the log with every kind-39301 grant carrying
// cek_epoch=epoch REMOVED, and returns how many it dropped.
//
// This is not a contrivance — it is what a NIP-01 relay ACTUALLY does. A grant is
// addressable with d = "<boardD>:<grantee>" (rolegrant.go roleGrantD), so every
// epoch reuses one slot and a new-epoch grant REPLACES its predecessor. Verified
// against the live public relay on 2026-07-28: after one rotation of the ready
// board, wss://relay.3dl.network returned FOUR grants, all epoch 2, and zero
// epoch-1 grants — while 200 of that board's 344 cards were still sealed at epoch
// 1. So this function reproduces the state of any reader who bootstraps from a
// relay instead of from this one workstation's append-only log.
func dropEpochGrantsFromLog(t *testing.T, dir string, epoch string) int {
	t.Helper()
	path := rdSync.NostrLogPath(dir)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	var kept []string
	dropped := 0
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e nostr.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			kept = append(kept, line)
			continue
		}
		drop := false
		if e.Kind == 39301 {
			for _, tg := range e.Tags {
				if len(tg) >= 2 && tg[0] == "cek_epoch" && tg[1] == epoch {
					drop = true
				}
			}
		}
		if drop {
			dropped++
			continue
		}
		kept = append(kept, line)
	}
	if err := sc.Err(); err != nil {
		f.Close()
		t.Fatalf("scan log: %v", err)
	}
	f.Close()
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("rewrite log: %v", err)
	}
	return dropped
}

// latestCardContent returns the Content of the newest kind-30302 card for itemID
// in the log, plus its cek_epoch tag.
func latestCardContent(t *testing.T, dir, itemID string) (content, epoch string) {
	t.Helper()
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var newest *nostr.Event
	for _, e := range events {
		if e.Kind != 30302 {
			continue
		}
		d, _ := tagVal(e.Tags, "d")
		if d != itemID {
			continue
		}
		if newest == nil || e.CreatedAt > newest.CreatedAt ||
			(e.CreatedAt == newest.CreatedAt && e.ID > newest.ID) {
			newest = e
		}
	}
	if newest == nil {
		t.Fatalf("no card found for %s", itemID)
	}
	ep, _ := tagVal(newest.Tags, "cek_epoch")
	return newest.Content, ep
}

// TestUnreadableItemIsNeverRepublished is the regression test for the
// placeholder round-trip that DESTROYED four items on the live ready board
// (ready-76b): ready-2b25 and three enc-live fixtures each ended up with the
// literal string "[encrypted]" sealed as their real title and context.
//
// The mechanism, reproduced end to end here:
//
//  1. an item is created on a confidential board and sealed under epoch 1;
//  2. the board is rotated to epoch 2, which REPLACES the epoch-1 grant in its
//     addressable slot, so a relay-sourced reader loses the epoch-1 CEK;
//  3. the reader can no longer decrypt the epoch-1 card, and the projection
//     correctly fail-closes Title/Context/Description to "[encrypted]";
//  4. any mutation rebuilds the WHOLE latest-wins card from that projected item
//     (CardSpecFromItem) — so before the guard, step 4 re-sealed the PLACEHOLDER
//     as the item's content under epoch 2 and the original was gone for good.
//
// The assertions that matter are BOTH halves: the mutation must fail loudly, and
// the stored ciphertext must be byte-identical afterwards. Asserting only the
// error would still pass an implementation that errored after publishing.
func TestUnreadableItemIsNeverRepublished(t *testing.T) {
	dir, owner := setupConfidentialProject(t)
	boardD := projectPrefix(dir)
	coord := rdSync.BoardCoord(owner, boardD)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}

	const realTitle = "PRE-ROTATION secret that must survive"
	const realContext = "this context is the thing the placeholder round-trip destroyed"
	itemID, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: realTitle, context: realContext, itemType: "task", priority: "p1",
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}

	// Sanity: sealed at epoch 1 and readable right now.
	sealedBefore, epochBefore := latestCardContent(t, dir, itemID)
	if epochBefore != "1" {
		t.Fatalf("card sealed at epoch %q, want 1", epochBefore)
	}
	if _, byID, err := nostrProjectAllItems(); err != nil {
		t.Fatalf("project: %v", err)
	} else if got := byID[itemID]; got == nil {
		t.Fatalf("item %s is not in the projection right after creation", itemID)
	} else if got.Title != realTitle {
		t.Fatalf("pre-rotation title = %q, want %q", got.Title, realTitle)
	}

	// Rotate to epoch 2, then reproduce what the relay does to the superseded
	// epoch-1 grant: drop it.
	if _, _ = rotateOnce(t, dir, owner, boardD); true {
		if dropped := dropEpochGrantsFromLog(t, dir, "1"); dropped == 0 {
			t.Fatal("no epoch-1 grants were dropped — the test did not reproduce the relay's replacement")
		}
	}
	kr := keyringFor(t, dir, k, owner, boardD)
	if _, ok := kr.CEK(coord, 1); ok {
		t.Fatal("reader still holds the epoch-1 CEK; the lost-key state was not reproduced")
	}
	if _, _, ok := kr.CurrentEpoch(coord); !ok {
		t.Fatal("reader lost ALL epochs; the test needs a writable board at epoch 2")
	}

	// The projection must fail closed AND mark the item, which is what the write
	// path keys on.
	_, byID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("project after rotation: %v", err)
	}
	item := byID[itemID]
	if item == nil {
		t.Fatalf("item %s vanished from the projection", itemID)
	}
	if item.Title != "[encrypted]" {
		t.Fatalf("title = %q, want the fail-closed placeholder", item.Title)
	}
	if !item.Redacted {
		t.Fatal("projection left Redacted=false on an item it could not decrypt — the write guard has nothing to key on")
	}

	// EVERY mutation path must refuse. Each of these rebuilds and re-seals the
	// whole card, so each is a distinct route to the same destruction.
	mutations := map[string]func() error{
		"claim": func() error { return runClaimNostr(itemID, "") },
		"close": func() error { return runCloseNostr(itemID, "done", "closing an item I cannot read", "done") },
		"label": func() error { return runLabelAddNostr(itemID, "urgent") },
		"update-fields": func() error {
			return runUpdateNostr(itemID, nostrUpdateSpec{priority: "p0", hasFieldUpdate: true})
		},
		"update-status": func() error {
			return runUpdateNostr(itemID, nostrUpdateSpec{statusTo: "active", hasStatusUpdate: true})
		},
		"delegate": func() error { return runDelegateNostr(itemID, owner, "") },
		"gate":     func() error { return runGateNostr(itemID, "design", "needs a ruling") },
	}
	for name, mutate := range mutations {
		err := mutate()
		if err == nil {
			t.Fatalf("%s SUCCEEDED on an unreadable item — it just overwrote the sealed content with a placeholder", name)
		}
		if !strings.Contains(err.Error(), itemID) {
			t.Errorf("%s error does not name the item: %v", name, err)
		}
		if !strings.Contains(err.Error(), "could not be decrypted") {
			t.Errorf("%s error does not explain why it refused: %v", name, err)
		}
	}

	// The stored ciphertext must be untouched — the whole point.
	sealedAfter, epochAfter := latestCardContent(t, dir, itemID)
	if sealedAfter != sealedBefore || epochAfter != epochBefore {
		t.Fatalf("the card was rewritten despite the refusal: epoch %s->%s, content changed=%v",
			epochBefore, epochAfter, sealedAfter != sealedBefore)
	}
}
