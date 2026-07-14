package main

// complete_trace_test.go — proves `rd complete --branch/--session` no longer
// silently discards those flags (ready-cb6 veracity fix SHOULD FIX #4). They are
// folded into the close reason and land durably in the item's nostr audit history.

import (
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/state"
)

// TestCompleteReasonWithTrace_FoldsFlags is the pure-function guard: each provided
// flag is annotated onto the reason; empty flags add nothing.
func TestCompleteReasonWithTrace_FoldsFlags(t *testing.T) {
	cases := []struct {
		reason, branch, session, want string
	}{
		{"done", "", "", "done"},
		{"done", "work/ready-cb6", "", "done [branch=work/ready-cb6]"},
		{"done", "", "sess-abc", "done [session=sess-abc]"},
		{"done", "work/ready-cb6", "sess-abc", "done [branch=work/ready-cb6] [session=sess-abc]"},
	}
	for _, tc := range cases {
		if got := completeReasonWithTrace(tc.reason, tc.branch, tc.session); got != tc.want {
			t.Fatalf("completeReasonWithTrace(%q,%q,%q) = %q, want %q",
				tc.reason, tc.branch, tc.session, got, tc.want)
		}
	}
}

// TestCompleteCmd_BranchSessionLandInAuditHistory drives the real completeCmd
// cobra RunE on a nostr-native project and proves --branch/--session survive into
// the terminal history entry's note — i.e. they are wired, not discarded.
func TestCompleteCmd_BranchSessionLandInAuditHistory(t *testing.T) {
	setupNostrNativeProject(t)

	id, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "Trace item", itemType: "task", priority: "p2",
	})
	if err != nil {
		t.Fatalf("runCreateNostr: %v", err)
	}
	if err := runClaimNostr(id, "picking up"); err != nil {
		t.Fatalf("runClaimNostr: %v", err)
	}

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("flag set: %v", err)
		}
	}
	must(completeCmd.Flags().Set("reason", "implemented"))
	must(completeCmd.Flags().Set("branch", "work/ready-cb6"))
	must(completeCmd.Flags().Set("session", "sess-xyz"))
	t.Cleanup(func() {
		_ = completeCmd.Flags().Set("reason", "")
		_ = completeCmd.Flags().Set("branch", "")
		_ = completeCmd.Flags().Set("session", "")
	})

	if err := completeCmd.RunE(completeCmd, []string{id}); err != nil {
		t.Fatalf("completeCmd.RunE: %v", err)
	}

	item, err := nostrResolveItem(id)
	if err != nil {
		t.Fatalf("resolve after complete: %v", err)
	}
	if item.Status != state.StatusDone {
		t.Fatalf("status after complete = %q, want done", item.Status)
	}
	last := item.History[len(item.History)-1]
	if !strings.Contains(last.Note, "[branch=work/ready-cb6]") {
		t.Fatalf("terminal history note %q missing branch annotation — --branch discarded", last.Note)
	}
	if !strings.Contains(last.Note, "[session=sess-xyz]") {
		t.Fatalf("terminal history note %q missing session annotation — --session discarded", last.Note)
	}
	assertNoDotCf(t)
}
