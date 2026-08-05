package main

// docs_reseal_announcement_test.go — ready-402 done-condition guards for
// docs/confidential-reseal-announcement.md and
// docs/confidential-reseal-rollback-runbook.md.
//
// WHY THESE EXIST. The first revision of the announcement carried a
// hand-rolled relay query's numbers — 11 boards, 2,931 plaintext cards, and
// "zero grantees below the current epoch". Within five days ready-43d's dry
// run measured 21 confidential boards, 3,045 coordinates, and exactly ONE
// reader who loses history. Every figure was stale and the one that mattered
// most — whether a real person loses access — was wrong in the direction that
// softens the loss, which this item's constraint forbids.
//
// The failure was not arithmetic. It was that the document presented a
// measurement as standing truth with no route back to the instrument. So the
// guards below check the two things that keep that from recurring: the doc
// must point at the tool that re-derives it, and the operator commands both
// documents instruct a human to run under pressure must actually exist.
//
// What these CANNOT check: whether the tables are currently accurate. That
// needs the live relay, drifts daily, and is the dry run's job, not a unit
// test's — which is exactly why the doc is required to name the dry run.

import (
	"os"
	"strings"
	"testing"

	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

func readResealDoc(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../docs/" + name)
	if err != nil {
		t.Fatalf("reading docs/%s: %v", name, err)
	}
	return string(b)
}

// TestResealAnnouncement_NamesTheInstrumentThatRederivesIt: every figure in
// the announcement is a snapshot of a live, concurrently-written portfolio.
// The document is only safe to read if it says, in its own text, which
// command reproduces it — otherwise the next reader inherits a number with no
// way to tell whether it still holds.
func TestResealAnnouncement_NamesTheInstrumentThatRederivesIt(t *testing.T) {
	doc := readResealDoc(t, "confidential-reseal-announcement.md")

	if !strings.Contains(doc, "go run ./scripts/resealplan") {
		t.Error("the announcement does not name `go run ./scripts/resealplan` — its numbers become unverifiable the moment the portfolio drifts, which is daily")
	}
	if _, err := os.Stat("../../scripts/resealplan/main.go"); err != nil {
		t.Errorf("the announcement instructs the reader to run ./scripts/resealplan, which is not there: %v", err)
	}
	if !strings.Contains(doc, "2026-08-05T15:52:53Z") {
		t.Error("the announcement does not stamp the snapshot its tables came from; an undated table reads as a standing fact")
	}
}

// TestResealAnnouncement_StatesTheLossRatherThanASummaryOfIt: the item's
// constraint is "do not soften the loss". The announcement must name the
// affected reader's pubkey concretely — a count, or a reassurance that the
// number is small, is exactly the softening that produced the wrong "zero"
// in the first revision.
func TestResealAnnouncement_StatesTheLossRatherThanASummaryOfIt(t *testing.T) {
	doc := readResealDoc(t, "confidential-reseal-announcement.md")

	const affected = "3032a516d23509f20e47147e2fc546e53bb1c3ec0fb59780a65f11fd4b0a4ca5"
	if !strings.Contains(doc, affected) {
		t.Errorf("the announcement does not name the affected reader %s; the dry run reports them by pubkey and the announcement is where they get told", affected)
	}
	// The retraction of the earlier "zero" claim has to stay in the document.
	// Removing it would let the same wrong figure be reintroduced by someone
	// reading only the current tables.
	if !strings.Contains(doc, "claimed this number was **zero**") {
		t.Error("the announcement no longer records that an earlier revision claimed zero affected readers — that retraction is what stops the figure being reintroduced")
	}
}

// TestResealDocs_OperatorCommandsExist: both documents are read by a human
// under pressure — one mid-pass, one deciding whether to ask for a grant. A
// command or flag that has since been renamed sends them down a dead end at
// the worst possible moment. Every rd surface the two docs instruct a reader
// to invoke is asserted to exist here.
func TestResealDocs_OperatorCommandsExist(t *testing.T) {
	announcement := readResealDoc(t, "confidential-reseal-announcement.md")
	runbook := readResealDoc(t, "confidential-reseal-rollback-runbook.md")

	if strings.Contains(announcement, "rd grant") {
		if grantCmd == nil {
			t.Fatal("the announcement tells affected readers to ask the owner to run `rd grant`, and there is no grant command")
		}
		if strings.Contains(announcement, "--all-boards") && grantCmd.Flags().Lookup("all-boards") == nil {
			t.Error("the announcement cites `rd grant --all-boards`; that flag does not exist")
		}
	}

	if strings.Contains(runbook, "rd relay audit") {
		for _, flag := range []string{"relay", "board"} {
			if relayAuditCmd.Flags().Lookup(flag) == nil {
				t.Errorf("the rollback runbook's step 2 runs `rd relay audit --%s`; that flag does not exist", flag)
			}
		}
	}

	// The runbook's step 2 reads the dry run's per-coordinate output and keys
	// its stop-point classification on the "already-sealed" skip reason. If
	// that constant is renamed, the runbook's instruction silently stops
	// matching anything the tool prints.
	if strings.Contains(runbook, "already-sealed") && rdSync.SkipAlreadySealed != "already-sealed" {
		t.Errorf("the rollback runbook classifies completed coordinates by the skip reason %q, but sync.SkipAlreadySealed is now %q", "already-sealed", rdSync.SkipAlreadySealed)
	}
}

// TestResealRunbook_KeepsTheNoRestoreConstraint: the runbook's whole reason to
// exist is that the obvious rollback — republish the plaintext — re-exposes
// precisely what the pass was run to protect. That constraint is load-bearing
// and easy to soften away in an edit that makes the document sound more
// reassuring.
func TestResealRunbook_KeepsTheNoRestoreConstraint(t *testing.T) {
	runbook := readResealDoc(t, "confidential-reseal-rollback-runbook.md")

	if !strings.Contains(runbook, `**"Rollback" does NOT mean "restore the plaintext."**`) {
		t.Error("the rollback runbook no longer states that rollback does not mean restoring the plaintext — that is the constraint the entire procedure is built on")
	}
	if !strings.Contains(runbook, "There is no step 5. There is no \"undo.\"") {
		t.Error("the rollback runbook no longer says plainly that there is no undo")
	}
	// Step 3 is the one most likely to be faked by accident: verifying from
	// the machine that just wrote the re-seal proves nothing, because its
	// local log still holds the plaintext.
	if !strings.Contains(runbook, "off the relay") {
		t.Error("the rollback runbook no longer requires verification off the relay; verifying from the writing machine's own log passes unconditionally")
	}
}
