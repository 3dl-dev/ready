package main

// writevectors_test.go (ready-bc5) is the WRITE-side counterpart to
// internal/foldvectors/vectors_test.go. It lives in cmd/rd -- NOT in
// internal/writevectors -- because the vectors it replays call the REAL,
// un-exported run*Nostr command bodies (runClaimNostr, runCloseNostr, ...)
// that this package alone can see; internal/writevectors holds only the
// DATA shape (see that package's own citation/floor tests).
//
// Every vector REPLAYS the committed testdata/write.vectors.json through:
//   seed item(s) (published via the SAME real writer every create uses,
//     publishItemFullCreateNostr -- never a synthetic/injected event)
//     -> the vector's Op, dispatched to its real run*Nostr function
//     -> the newly appended event(s), compared kind+tags+content against
//        the vector's Events
//     -> a ROUND-TRIP fold of the WHOLE resulting log through
//        pkg/sync.ProjectItems, compared against PostFold (done condition 4:
//        "a write rd cannot read back is the failure this whole item exists
//        to prevent").
//
// NO MOCKS: every project is a real isolated nostr-native project
// (setupNostrCmdTest's temp dir + RD_NOSTR_RELAY_URL pointed at an
// unreachable loopback, exactly nostr_test.go's Group A harness), every
// signature is real (a fresh secp256k1 key per test, via the real nostrKey()
// path), and the log read back is the real on-disk .ready/nostr-log.jsonl.
import (
	"reflect"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/internal/writevectors"
	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	"github.com/3dl-dev/ready/pkg/state"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

const writeVectorPath = "../../testdata/write.vectors.json"

// writeVectorProject builds an isolated, PUBLIC (plaintext) nostr-native
// project exactly like setupNostrNativeProject, but returns the board
// coordinate too and asserts the fixed boardD assumption every committed
// vector's "a" tags rely on (see testdata/write.vectors.json's "compared"
// note): setupNostrCmdTest always names the project directory literally
// "project", so projectPrefix(dir) is deterministic across every run and
// every vector can hardcode ":project" instead of templating a board-name
// placeholder. This assertion fails loudly the moment that assumption stops
// holding, rather than the vector suite silently comparing against the wrong
// string.
func writeVectorProject(t *testing.T) (dir, owner, boardCoord string) {
	t.Helper()
	dir = setupNostrCmdTest(t)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	owner = k.PubKeyHex()
	boardD := projectPrefix(dir)
	if boardD != "project" {
		t.Fatalf("projectPrefix(%q) = %q, want the fixed %q every committed vector assumes -- "+
			"setupNostrCmdTest's project dirname changed; testdata/write.vectors.json's hardcoded "+
			"':project' board-d values need updating too", dir, boardD, "project")
	}
	boardCoord = rdSync.BoardCoord(owner, boardD)
	if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{ProjectName: "project", Board: boardCoord, Public: true}); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	return dir, owner, boardCoord
}

// seedItem publishes an ItemFixture through the REAL create writer
// (publishItemFullCreateNostr — the exact function runCreateNostr itself
// calls), defaulting Status to inbox and For to the signer (mirroring
// runCreateNostr's own default-to-self, cmd/rd/nostrwrite.go:523-526) so a
// hand-seeded fixture carries the same "for" tag any real item does.
func seedItem(t *testing.T, dir, owner string, f writevectors.ItemFixture) {
	t.Helper()
	it := &state.Item{
		ID: f.ID, Title: f.Title, Context: f.Context, Type: f.Type, Priority: f.Priority,
		Status: f.Status, By: f.By, For: f.For, Level: f.Level, ParentID: f.ParentID,
		ETA: f.ETA, Due: f.Due, Labels: f.Labels, BlockedBy: f.BlockedBy,
		Gate: f.Gate, WaitingType: f.WaitingType, WaitingOn: f.WaitingOn,
	}
	if it.Status == "" {
		it.Status = state.StatusInbox
	}
	if it.For == "" {
		it.For = owner
	}
	if err := publishItemFullCreateNostr(dir, owner, it); err != nil {
		t.Fatalf("seed %s: %v", f.ID, err)
	}
}

// dispatchWriteVectorOp resolves the vector's target item id and invokes the
// REAL run*Nostr function its Op names. This is the whole opTable the
// package doc promises: every op name testdata/write.vectors.json uses maps
// to exactly one line below.
func dispatchWriteVectorOp(t *testing.T, dir string, v writevectors.Vector) (targetID string, err error) {
	t.Helper()
	targetID = v.Args["item_id"]
	if v.Seed != nil {
		targetID = v.Seed.ID
	}
	switch v.Op {
	case "create":
		targetID = v.Args["id"]
		_, err = runCreateNostr(dir, nostrCreateSpec{
			id: v.Args["id"], title: v.Args["title"], context: v.Args["context"],
			itemType: v.Args["type"], priority: v.Args["priority"],
		})
	case "claim":
		err = runClaimNostr(targetID, v.Args["reason"])
	case "close":
		err = runCloseNostr(targetID, v.Args["resolution"], v.Args["reason"], v.Args["human_verb"])
	case "delegate":
		err = runDelegateNostr(targetID, v.Args["to"], v.Args["reason"])
	case "gate_open":
		err = runGateNostr(targetID, v.Args["gate_type"], v.Args["description"])
	case "gate_approve":
		err = runApproveNostr(targetID, v.Args["reason"])
	case "gate_reject":
		err = runRejectNostr(targetID, v.Args["reason"])
	case "dep_add":
		err = runDepAddNostr(targetID, v.Args["blocker_id"])
	case "dep_remove":
		err = runDepRemoveNostr(targetID, v.Args["blocker_id"])
	case "label_add":
		err = runLabelAddNostr(targetID, v.Args["label"])
	case "label_remove":
		err = runLabelRemoveNostr(targetID, v.Args["label"])
	case "update_fields":
		err = runUpdateNostr(targetID, nostrUpdateSpec{
			hasFieldUpdate: true,
			title:          v.Args["title"], context: v.Args["context"], priority: v.Args["priority"],
			eta: v.Args["eta"], due: v.Args["due"], level: v.Args["level"], parentID: v.Args["parent_id"],
		})
	case "update_status":
		err = runUpdateNostr(targetID, nostrUpdateSpec{
			hasStatusUpdate: true,
			statusTo:        v.Args["status_to"], waitingOn: v.Args["waiting_on"], waitingType: v.Args["waiting_type"],
			note: v.Args["note"],
		})
	default:
		t.Fatalf("vector %q: unknown op %q -- add it to dispatchWriteVectorOp's opTable", v.Name, v.Op)
	}
	return targetID, err
}

// expandPlaceholders substitutes every {{owner}} / {{card:<id>}} /
// {{issue:<id>}} token in s -- see testdata/write.vectors.json's top-level
// "compared" field for why these three, and only these three, are
// templated rather than compared as literal hex.
func expandPlaceholders(s, owner string, cardIDs, issueIDs map[string]string) string {
	s = strings.ReplaceAll(s, "{{owner}}", owner)
	for id, cid := range cardIDs {
		s = strings.ReplaceAll(s, "{{card:"+id+"}}", cid)
	}
	for id, iid := range issueIDs {
		s = strings.ReplaceAll(s, "{{issue:"+id+"}}", iid)
	}
	return s
}

func expandTags(tags [][]string, owner string, cardIDs, issueIDs map[string]string) [][]string {
	out := make([][]string, len(tags))
	for i, tag := range tags {
		row := make([]string, len(tag))
		for j, v := range tag {
			row[j] = expandPlaceholders(v, owner, cardIDs, issueIDs)
		}
		out[i] = row
	}
	return out
}

// TestWriteConformanceVectors is done-condition 1 (ready-bc5): every vector
// runs through rd's REAL writer and must reproduce the exact event(s) it
// appends to the local authoritative log, plus the round-trip fold
// (done condition 4).
func TestWriteConformanceVectors(t *testing.T) {
	f, err := writevectors.Load(writeVectorPath)
	if err != nil {
		t.Fatalf("load write vectors: %v", err)
	}
	t.Logf("write conformance: %d vectors from %s (spec %s)", len(f.Vectors), writeVectorPath, f.Spec)

	for _, v := range f.Vectors {
		v := v
		t.Run(v.Name, func(t *testing.T) {
			dir, owner, boardCoord := writeVectorProject(t)

			for _, es := range v.ExtraSeeds {
				seedItem(t, dir, owner, es)
			}
			if v.Seed != nil {
				seedItem(t, dir, owner, *v.Seed)
			}

			log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
			before, err := log.ReadAll()
			if err != nil {
				t.Fatalf("read log before op: %v", err)
			}

			targetID, opErr := dispatchWriteVectorOp(t, dir, v)

			after, err := log.ReadAll()
			if err != nil {
				t.Fatalf("read log after op: %v", err)
			}
			newEvents := after[len(before):]

			if v.ExpectError {
				if opErr == nil {
					t.Fatalf("expected an error containing %q, got nil", v.ErrorContains)
				}
				if v.ErrorContains != "" && !strings.Contains(opErr.Error(), v.ErrorContains) {
					t.Errorf("error = %q, want it to contain %q", opErr.Error(), v.ErrorContains)
				}
				if len(newEvents) != 0 {
					t.Errorf("a refused (fail-closed) op appended %d new event(s), want 0 -- a refusal MUST NOT "+
						"partially publish", len(newEvents))
				}
				return
			}
			if opErr != nil {
				t.Fatalf("op %q on %s: %v", v.Op, targetID, opErr)
			}

			// SS19.7/SS25.5 cross-cutting check: no LIVE event, from ANY op, ever
			// carries a "by" tag -- attribution is the event's OWN PubKey, never a
			// separate spoofable tag (the exact shape the read-side spoof guard,
			// pkg/sync/nostrproject.go:401-404, exists to reject). Folding this into
			// every vector's run (rather than one bespoke test) means it is checked
			// against every op this suite exercises, not just one.
			for _, e := range newEvents {
				for _, tag := range e.Tags {
					if len(tag) >= 1 && tag[0] == "by" {
						t.Errorf("op %q emitted a %q tag on kind %d -- SS19.7/SS25.5 forbid any live "+
							"write from ever carrying a by tag", v.Op, "by", e.Kind)
					}
				}
			}

			cardIDs := map[string]string{}
			for _, e := range newEvents {
				if e.Kind == rdSync.KindCard {
					if d, ok := tagVal(e.Tags, "d"); ok && d != "" {
						cardIDs[d] = e.ID
					}
				}
			}
			issueIDs := map[string]string{}
			for _, e := range after {
				if e.Kind == rdSync.KindIssue {
					if d, ok := tagVal(e.Tags, "d"); ok && d != "" {
						issueIDs[d] = e.ID
					}
				}
			}

			if len(newEvents) != len(v.Events) {
				t.Fatalf("appended %d event(s), want %d -- got kinds %v", len(newEvents), len(v.Events), kindsOf(newEvents))
			}
			for i, want := range v.Events {
				got := newEvents[i]
				if got.Kind != want.Kind {
					t.Errorf("event %d: kind = %d, want %d", i, got.Kind, want.Kind)
				}
				wantTags := expandTags(want.Tags, owner, cardIDs, issueIDs)
				if !reflect.DeepEqual(wantTags, got.Tags) {
					t.Errorf("event %d (kind %d) tags mismatch:\n  want: %v\n  got:  %v", i, want.Kind, wantTags, got.Tags)
				}
				switch want.Content.Mode {
				case "empty":
					if got.Content != "" {
						t.Errorf("event %d content = %q, want empty", i, got.Content)
					}
				case "literal":
					if got.Content != want.Content.Value {
						t.Errorf("event %d content = %q, want %q", i, got.Content, want.Content.Value)
					}
				default:
					t.Fatalf("event %d: unknown content mode %q", i, want.Content.Mode)
				}
			}

			if v.PostFold == nil {
				return
			}
			items := rdSync.ProjectItems(after, rdSync.ProjectOptions{PinnedBoard: boardCoord})
			it, ok := items[v.PostFold.ID]
			if !ok {
				t.Fatalf("round-trip: item %s not found in the projection of this vector's own produced log -- "+
					"a write rd cannot read back is exactly the failure this suite exists to catch", v.PostFold.ID)
			}
			checkPostFold(t, v.PostFold, it, owner)
		})
	}
}

// checkPostFold compares only the NON-ZERO fields of want against the real
// projected item -- see writevectors.ItemFixture's doc comment: PostFold is
// deliberately partial (a "don't care" field is simply omitted from the
// vector), because most vectors only need to prove ONE or TWO fields
// actually round-tripped, not restate the whole item.
func checkPostFold(t *testing.T, want *writevectors.ItemFixture, got *state.Item, owner string) {
	t.Helper()
	if want.Status != "" && got.Status != want.Status {
		t.Errorf("post-fold status = %q, want %q", got.Status, want.Status)
	}
	if want.Title != "" && got.Title != want.Title {
		t.Errorf("post-fold title = %q, want %q", got.Title, want.Title)
	}
	if want.Priority != "" && got.Priority != want.Priority {
		t.Errorf("post-fold priority = %q, want %q", got.Priority, want.Priority)
	}
	if want.Context != "" && got.Context != want.Context {
		t.Errorf("post-fold context = %q, want %q", got.Context, want.Context)
	}
	if want.By != "" {
		wantBy := expandPlaceholders(want.By, owner, nil, nil)
		if got.By != wantBy {
			t.Errorf("post-fold by = %q, want %q", got.By, wantBy)
		}
	}
	if want.Gate != "" && got.Gate != want.Gate {
		t.Errorf("post-fold gate = %q, want %q", got.Gate, want.Gate)
	}
	if want.WaitingType != "" && got.WaitingType != want.WaitingType {
		t.Errorf("post-fold waiting_type = %q, want %q", got.WaitingType, want.WaitingType)
	}
	if want.Labels != nil {
		if !reflect.DeepEqual(sortedCopy(want.Labels), sortedCopy(got.Labels)) {
			t.Errorf("post-fold labels = %v, want %v", got.Labels, want.Labels)
		}
	}
	if want.BlockedBy != nil {
		if !reflect.DeepEqual(sortedCopy(want.BlockedBy), sortedCopy(got.BlockedBy)) {
			t.Errorf("post-fold blocked_by = %v, want %v", got.BlockedBy, want.BlockedBy)
		}
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	// Small, fixed-size test fixtures -- insertion sort is plenty and avoids
	// importing "sort" just for this.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// kindsOf is a t.Fatalf debugging aid: the event kinds in order, so a count
// mismatch's failure message shows WHAT was appended, not just how many.
func kindsOf(events []*nostr.Event) []int {
	out := make([]int, len(events))
	for i, e := range events {
		out[i] = e.Kind
	}
	return out
}

// TestWriteVector_SameSecondWritesToTheSameItemAreStrictlyOrdered is the
// write-vector-level proof of board-fold-spec.md SS17.2/SS17.3
// (nostrNextCreatedAt): two writes to the SAME item's causal chain, issued
// back to back (in practice within the same wall-clock second), must still
// get STRICTLY increasing created_at -- the negative vector
// "a second write in the same second must still produce a strictly ordered
// pair" ready-bc5 asks for. This drives it through the REAL command
// sequence (claim then delegate), not nostrNextCreatedAt in isolation
// (already covered at unit level by cmd/rd/nostr_test.go).
func TestWriteVector_SameSecondWritesToTheSameItemAreStrictlyOrdered(t *testing.T) {
	dir, _, _ := writeVectorProject(t)
	id, err := runCreateNostr(dir, nostrCreateSpec{id: "wv-mono", title: "monotonic", itemType: "task", priority: "p2"})
	if err != nil {
		t.Fatalf("runCreateNostr: %v", err)
	}
	log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
	if err := runClaimNostr(id, "first"); err != nil {
		t.Fatalf("runClaimNostr: %v", err)
	}
	if err := runDelegateNostr(id, "abc123deadbeefabc123deadbeefabc123deadbeefabc123deadbeefabc12300", "second"); err != nil {
		t.Fatalf("runDelegateNostr: %v", err)
	}
	events, err := log.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	var statusTimes []int64
	for _, e := range events {
		if d, ok := tagVal(e.Tags, "d"); ok && e.Kind == rdSync.KindStatusOpen && d == id {
			statusTimes = append(statusTimes, e.CreatedAt)
		}
	}
	if len(statusTimes) < 3 { // create's own status(inbox) + claim + delegate
		t.Fatalf("expected at least 3 status events for %s, got %d", id, len(statusTimes))
	}
	for i := 1; i < len(statusTimes); i++ {
		if statusTimes[i] <= statusTimes[i-1] {
			t.Fatalf("status event %d created_at=%d is not STRICTLY after event %d's created_at=%d -- "+
				"same-second writes to one item's chain must still be strictly ordered (SS17.2/SS17.3)",
				i, statusTimes[i], i-1, statusTimes[i-1])
		}
	}
}
