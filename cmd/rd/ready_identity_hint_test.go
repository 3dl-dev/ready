package main

// ready-497: `rd ready` (and the other identity-scoped named views) must not
// return bare empty output when the identity scope hides a non-empty board --
// the repro that filed the item was `rd ready` (0 bytes, exit 0) vs
// `rd ready --for ""` (10 items) vs `rd list` (17 open items). Silence read as
// "all caught up" when 17 items existed for other parties.
//
// BEWARE THE TEST TRAP THIS SWARM KEEPS HITTING (per the item's own warning):
// a fixture that structurally cannot exhibit the defect, or a test that
// returns before the branch actually under test. These tests therefore:
//   - drive readyCmd.RunE itself (not printIdentityScopeHint in isolation)
//     for the four output shapes explicitly called out in the item (TTY tree,
//     --flat, piped non-TTY, --json), each asserting the hint lands on
//     STDERR and the STDOUT contract for that shape is unchanged;
//   - include a genuinely-empty-board case that must stay silent, so a
//     regression that fires the hint unconditionally (rather than only when
//     the identity-blind recompute finds something) is caught too.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/state"
)

// setForFilter sets the --for flag to an explicit value (Changed=true so
// runReady's "default to session identity" branch does not override it) and
// returns a restore func.
func setForFilter(t *testing.T, value string) func() {
	t.Helper()
	if err := readyCmd.Flags().Set("for", value); err != nil {
		t.Fatalf("setting --for=%q: %v", value, err)
	}
	return func() {
		if err := readyCmd.Flags().Set("for", ""); err != nil {
			t.Fatalf("restoring --for: %v", err)
		}
	}
}

// buildIdentityScopedProject publishes one ready item owned by "other-party@test"
// (never the session's own pubkey) and returns the project dir plus the
// session's own pubkey hex -- the identity that will scope it away.
func buildIdentityScopedProject(t *testing.T, projectName string) (dir string, ownHex string) {
	t.Helper()
	items := []*state.Item{
		{ID: projectName + "-hidden-1", Status: "inbox", Priority: "p2", Title: "Hidden from scope", For: "other-party@test"},
	}
	dir = setupNostrProjectWithItems(t, projectName, items)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	return dir, k.PubKeyHex()
}

// runReadyCapturingBoth runs readyCmd.RunE and captures stdout and stderr
// separately (nesting captureStderrPipe's redirect inside
// captureStdoutPipe's, so both streams are swapped for the same call).
func runReadyCapturingBoth(t *testing.T) (stdout, stderr string) {
	t.Helper()
	stdout = captureStdoutPipe(t, func() {
		stderr = captureStderrPipe(t, func() {
			if err := readyCmd.RunE(readyCmd, []string{}); err != nil {
				t.Fatalf("readyCmd.RunE: %v", err)
			}
		})
	})
	return stdout, stderr
}

// TestReadyCmd_RunE_IdentityScopeHint_FiresAcrossOutputModes is the headline
// coverage for ready-497's "your empty-output handling has to be right on
// ALL those paths -- tree, --flat, piped, and --json -- not just the one you
// happen to test" instruction. Same fixture (one item scoped to another
// party), four separate RunE calls forcing each output mode.
func TestReadyCmd_RunE_IdentityScopeHint_FiresAcrossOutputModes(t *testing.T) {
	cases := []struct {
		name        string
		tty         bool
		flat        bool
		json        bool
		wantStdout  string // exact expected stdout (after TrimSpace), "" meaning empty
		checkStdout func(t *testing.T, stdout string)
	}{
		{
			name: "TTY_tree_default", tty: true, flat: false, json: false,
			checkStdout: func(t *testing.T, stdout string) {
				if strings.TrimSpace(stdout) != "nothing ready" {
					t.Errorf("TTY tree mode: expected stdout %q, got %q", "nothing ready", stdout)
				}
			},
		},
		{
			name: "TTY_flat", tty: true, flat: true, json: false,
			checkStdout: func(t *testing.T, stdout string) {
				if strings.TrimSpace(stdout) != "nothing ready" {
					t.Errorf("TTY flat mode: expected stdout %q, got %q", "nothing ready", stdout)
				}
			},
		},
		{
			name: "piped_nonTTY", tty: false, flat: false, json: false,
			checkStdout: func(t *testing.T, stdout string) {
				if strings.TrimSpace(stdout) != "" {
					t.Errorf("piped mode: expected empty stdout (no bare IDs), got %q", stdout)
				}
			},
		},
		{
			name: "json", tty: true, flat: false, json: true,
			checkStdout: func(t *testing.T, stdout string) {
				var decoded []map[string]interface{}
				if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
					t.Fatalf("--json mode: stdout is not valid JSON: %v\nstdout: %q", err, stdout)
				}
				if len(decoded) != 0 {
					t.Errorf("--json mode: expected an empty JSON array, got %d entries: %s", len(decoded), stdout)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origTTY := isTTYStdout
			defer func() { isTTYStdout = origTTY }()
			isTTYStdout = func() bool { return tc.tty }

			origJSON := jsonOutput
			defer func() { jsonOutput = origJSON }()

			_, ownHex := buildIdentityScopedProject(t, "hintproj-"+tc.name)
			defer resetReadyRunFlags(t)()
			defer setForFilter(t, ownHex)()
			if tc.flat {
				if err := readyCmd.Flags().Set("flat", "true"); err != nil {
					t.Fatalf("setting --flat=true: %v", err)
				}
			}
			jsonOutput = tc.json

			stdout, stderr := runReadyCapturingBoth(t)

			tc.checkStdout(t, stdout)

			if !strings.Contains(stderr, "1 item(s) exist for other parties") {
				t.Errorf("%s: expected stderr hint naming 1 hidden item, got stderr=%q", tc.name, stderr)
			}
			if !strings.Contains(stderr, ownHex) && !strings.Contains(stderr, ownHex[:12]) {
				t.Errorf("%s: expected stderr hint to name the identity in effect (%s), got %q", tc.name, ownHex, stderr)
			}
			if !strings.Contains(stderr, `rd ready`) {
				t.Errorf("%s: expected stderr hint to suggest an rd ready command, got %q", tc.name, stderr)
			}
			// The hidden item's title/ID must never leak into stdout -- the
			// hint is informational (count only), not a content bypass of the
			// identity scope.
			if strings.Contains(stdout, "Hidden from scope") {
				t.Errorf("%s: stdout leaked the identity-scoped item's content: %q", tc.name, stdout)
			}
		})
	}
}

// TestReadyCmd_RunE_IdentityScopeHint_SilentWhenGenuinelyEmpty guards the
// other half of the DONE CONDITION: "a genuinely empty board should still
// read as genuinely empty". A board with zero items anywhere must produce
// the pre-existing silent/"nothing ready" behavior with NO stderr hint --
// otherwise the fix would regress into crying wolf on every truly-empty
// board.
func TestReadyCmd_RunE_IdentityScopeHint_SilentWhenGenuinelyEmpty(t *testing.T) {
	origTTY := isTTYStdout
	defer func() { isTTYStdout = origTTY }()
	isTTYStdout = func() bool { return true }
	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	setupNostrProjectWithItems(t, "hintproj-empty", nil)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	defer resetReadyRunFlags(t)()
	defer setForFilter(t, k.PubKeyHex())()

	stdout, stderr := runReadyCapturingBoth(t)

	if strings.TrimSpace(stdout) != "nothing ready" {
		t.Errorf("expected stdout %q, got %q", "nothing ready", stdout)
	}
	if stderr != "" {
		t.Errorf("expected NO stderr hint on a genuinely empty board, got %q", stderr)
	}
}

// TestReadyCmd_RunE_IdentityScopeHint_SilentWhenForExplicitlyEmpty covers
// the guard in printIdentityScopeHint: forFilter == "" means the caller
// already asked to see everyone's work (`rd ready --for ""`), so there is no
// identity to blame emptiness on and the hint must not fire even if other
// filters (label/project) are what zeroed the result.
func TestReadyCmd_RunE_IdentityScopeHint_SilentWhenForExplicitlyEmpty(t *testing.T) {
	origTTY := isTTYStdout
	defer func() { isTTYStdout = origTTY }()
	isTTYStdout = func() bool { return true }
	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	buildIdentityScopedProject(t, "hintproj-explicit-blank")
	defer resetReadyRunFlags(t)()
	defer setForFilter(t, "")()
	// Apply a label filter that matches nothing, so the final result is
	// empty for a reason that has nothing to do with identity.
	defer resetReadyLabelFlag(t, []string{"no-such-label-xyz"})()

	stdout, stderr := runReadyCapturingBoth(t)

	if strings.TrimSpace(stdout) != "nothing ready" {
		t.Errorf("expected stdout %q, got %q", "nothing ready", stdout)
	}
	if strings.Contains(stderr, "exist for other parties") {
		t.Errorf("expected NO identity-scope hint when --for \"\" was explicit, got %q", stderr)
	}
}

// TestIdentityBlindViewFilter_MyWork_DelegatedShapes unit-tests the two
// identity-baked views directly: MyWorkFilterSet/DelegatedFilterSet embed
// identity restriction INSIDE the Filter object itself (unlike every other
// named view, where identity scoping is layered on afterward in runReady),
// so identityBlindViewFilter must build a separate identity-blind predicate
// for exactly these two rather than reusing the Filter as-is. This exercises
// that branch directly, without needing a full nostr project fixture.
func TestIdentityBlindViewFilter_MyWork_DelegatedShapes(t *testing.T) {
	assigned := &state.Item{ID: "a", By: "someone@test", Status: "active"}
	unassigned := &state.Item{ID: "b", By: "", Status: "inbox"}
	doneAssigned := &state.Item{ID: "c", By: "someone@test", Status: "done"}

	myWorkBlind := identityBlindViewFilter("my-work", nil)
	if !myWorkBlind(assigned) {
		t.Error("my-work identity-blind filter should match a non-terminal assigned item regardless of who it's assigned to")
	}
	if myWorkBlind(unassigned) {
		t.Error("my-work identity-blind filter should not match an item with no assignee")
	}
	if myWorkBlind(doneAssigned) {
		t.Error("my-work identity-blind filter should not match a terminal item")
	}

	delegated := &state.Item{ID: "d", For: "owner@test", By: "other@test", Status: "active"}
	selfWork := &state.Item{ID: "e", For: "owner@test", By: "owner@test", Status: "active"}
	delegatedBlind := identityBlindViewFilter("delegated", nil)
	if !delegatedBlind(delegated) {
		t.Error("delegated identity-blind filter should match an active item delegated to someone else")
	}
	if delegatedBlind(selfWork) {
		t.Error("delegated identity-blind filter should not match an item For==By (not a delegation)")
	}

	// Every other view reuses the passed-in Filter unchanged.
	sentinel := func(*state.Item) bool { return true }
	if got := identityBlindViewFilter("ready", sentinel); got(assigned) != sentinel(assigned) {
		t.Error("identityBlindViewFilter(\"ready\", f) must reuse f unchanged")
	}
}
