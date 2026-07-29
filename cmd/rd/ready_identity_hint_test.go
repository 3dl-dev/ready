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
//
// NOTE (ready-497 rework): this test alone does NOT prove the `forFilter ==
// ""` guard does anything -- the --label atom it applies is re-applied
// inside printIdentityScopeHint's own recompute (see the loop over
// labelFilters there), which zeroes `hidden` and suppresses the hint via
// len(hidden)==0 regardless of whether the guard exists. Deleting the guard
// and running `go test ./cmd/rd/... -run IdentityScopeHint -count=1` still
// passes this test. It is kept because the behavior it asserts (no hint on
// `--for ""`) is still real and worth pinning, but the guard's actual
// necessity is proven by
// TestReadyCmd_RunE_IdentityScopeHint_GuardNecessary_MyWorkExplicitForEmpty
// below, where the divergence is NOT self-cancelling.
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

// TestReadyCmd_RunE_IdentityScopeHint_GuardNecessary_MyWorkExplicitForEmpty
// is the load-bearing proof that printIdentityScopeHint's `forFilter == ""`
// guard actually does something (ready-497 rework, problem 1: the sibling
// test above is inert because its label-filter divergence self-cancels
// inside the recompute).
//
// --view my-work with --for "" explicit is the one place the divergence does
// NOT self-cancel: MyWorkFilterSet(idset) with an empty idset (forFilter=="")
// always returns false (see pkg/views.MyWorkFilterSet), so the REAL
// computation is unconditionally empty regardless of what's on the board.
// But identityBlindViewFilter("my-work", ...) builds a SEPARATE predicate
// (item.By != "" && !terminal) that ignores identity/idset entirely -- so
// the recompute over fullItems finds the fixture's assigned item even though
// the real computation never could have. Without the guard, that recompute
// alone would make the hint fire. Verified by mutation: commenting out the
// `if forFilter == "" { return }` guard in printIdentityScopeHint and
// re-running
// `go test ./cmd/rd/... -run TestReadyCmd_RunE_IdentityScopeHint_GuardNecessary -count=1`
// turns this test red (stderr contains "exist for other parties"); restoring
// the guard turns it green again.
func TestReadyCmd_RunE_IdentityScopeHint_GuardNecessary_MyWorkExplicitForEmpty(t *testing.T) {
	origTTY := isTTYStdout
	defer func() { isTTYStdout = origTTY }()
	isTTYStdout = func() bool { return true }
	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	items := []*state.Item{
		{ID: "myworkproj-assigned-1", Status: "active", Priority: "p2", Title: "Assigned to someone", By: "someone-else@test"},
	}
	setupNostrProjectWithItems(t, "myworkproj-explicit-blank", items)

	defer resetReadyRunFlags(t)()
	if err := readyCmd.Flags().Set("view", "my-work"); err != nil {
		t.Fatalf("setting --view=my-work: %v", err)
	}
	defer setForFilter(t, "")()

	stdout, stderr := runReadyCapturingBoth(t)

	if strings.TrimSpace(stdout) != "nothing ready" {
		t.Errorf("expected stdout %q, got %q", "nothing ready", stdout)
	}
	if strings.Contains(stderr, "exist for other parties") {
		t.Errorf("expected NO identity-scope hint for --view my-work --for \"\" (caller explicitly asked for no identity scoping, nothing to blame on identity), got %q", stderr)
	}
}

// TestReadyCmd_RunE_IdentityScopeHint_SilentWhenLabelHidesOwnItem covers
// ready-497 rework problem 2: printIdentityScopeHint's recompute re-applies
// filterByProject and the LabelFilter loop over `hidden` specifically so
// that a project/label filter -- not identity -- hiding the caller's OWN
// item does not get misreported as "items exist for other parties". Mutation
// tested: deleting the filterByProject line and the LabelFilter loop from
// printIdentityScopeHint (leaving `hidden` un-reapplied) turns this test red
// -- the fixture's item, which IS for the caller, would then count as
// "hidden" simply because it doesn't carry the requested label.
func TestReadyCmd_RunE_IdentityScopeHint_SilentWhenLabelHidesOwnItem(t *testing.T) {
	origTTY := isTTYStdout
	defer func() { isTTYStdout = origTTY }()
	isTTYStdout = func() bool { return true }
	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	dir := setupNostrProjectWithItems(t, "hintproj-label-own", nil)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	ownHex := k.PubKeyHex()
	item := &state.Item{ID: "hintproj-label-own-1", Status: "inbox", Priority: "p2", Title: "Mine, no matching label", For: ownHex, By: ownHex}
	if err := publishItemFullCreateNostr(dir, ownHex, item); err != nil {
		t.Fatalf("publish own item: %v", err)
	}

	defer resetReadyRunFlags(t)()
	defer setForFilter(t, ownHex)()
	defer resetReadyLabelFlag(t, []string{"no-such-label-xyz"})()

	stdout, stderr := runReadyCapturingBoth(t)

	if strings.TrimSpace(stdout) != "nothing ready" {
		t.Errorf("expected stdout %q, got %q", "nothing ready", stdout)
	}
	if strings.Contains(stderr, "exist for other parties") {
		t.Errorf("expected NO identity-scope hint when a LABEL filter (not identity) hid the caller's OWN item, got %q", stderr)
	}
}

// TestReadyCmd_RunE_IdentityScopeHint_SilentWhenScopeGateDenies is a live
// repro of ready-497 rework problem 3: one item with For==By==the session's
// own pubkey, --for set to that pubkey, and --scope set to an ungranted
// 64-hex key. The `--scope` gate (nostrScopeForKey) zeroes `items` AFTER
// every filter printIdentityScopeHint's recompute reproduces (view/project/
// label), so the recompute cannot see it and would otherwise find the
// caller's own item via the identity-blind pass and misreport the scope
// gate's emptiness as identity scope hiding work. The scope gate already
// prints its own note (asserted below) naming the true cause; the identity
// hint must stay silent on top of it. Mutation tested: removing the
// `scopeGateDenied` plumbing/guard from ready.go turns this test red -- the
// identity hint fires ("... exist for other parties") even though identity
// was never the reason.
func TestReadyCmd_RunE_IdentityScopeHint_SilentWhenScopeGateDenies(t *testing.T) {
	origTTY := isTTYStdout
	defer func() { isTTYStdout = origTTY }()
	isTTYStdout = func() bool { return true }
	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	dir := setupNostrProjectWithItems(t, "hintproj-scope-deny", nil)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	ownHex := k.PubKeyHex()
	item := &state.Item{ID: "hintproj-scope-deny-1", Status: "active", Priority: "p2", Title: "Mine", For: ownHex, By: ownHex}
	if err := publishItemFullCreateNostr(dir, ownHex, item); err != nil {
		t.Fatalf("publish own item: %v", err)
	}
	foreign := freshKeyHex(t)

	defer resetReadyRunFlags(t)()
	defer setForFilter(t, ownHex)()
	if err := readyCmd.Flags().Set("scope", foreign); err != nil {
		t.Fatalf("setting --scope=%q: %v", foreign, err)
	}
	defer func() {
		if err := readyCmd.Flags().Set("scope", ""); err != nil {
			t.Fatalf("resetting --scope: %v", err)
		}
	}()

	stdout, stderr := runReadyCapturingBoth(t)

	if strings.TrimSpace(stdout) != "nothing ready" {
		t.Errorf("expected stdout %q, got %q", "nothing ready", stdout)
	}
	if strings.Contains(stderr, "exist for other parties") {
		t.Errorf("expected NO identity-scope hint when the --scope gate (not identity) zeroed the result, got %q", stderr)
	}
	if !strings.Contains(stderr, "not a granted identity") {
		t.Errorf("expected the scope gate's own denial note (the actual cause) to be printed, got %q", stderr)
	}
}

// TestReadyCmd_RunE_ScopeGateDenialNote_FiresInJSONMode covers ready-497
// rework problem 1 (round 2): the scope gate's denial note used to print
// only `if !jsonOutput`, and printIdentityScopeHint defers to that note via
// scopeGateDenied and never fires itself -- so on the --json path a denied
// --scope key produced `[]` on stdout and NOTHING on stderr: a brand new
// fully-silent empty result, on the exact class of defect this item exists
// to eliminate. Same fixture as
// TestReadyCmd_RunE_IdentityScopeHint_SilentWhenScopeGateDenies, but with
// jsonOutput=true. Regression coverage: reintroducing `if !jsonOutput` around
// the `fmt.Fprintln(os.Stderr, note)` call in ready.go turns this test red
// (stderr goes empty while stdout stays `[]`).
func TestReadyCmd_RunE_ScopeGateDenialNote_FiresInJSONMode(t *testing.T) {
	origTTY := isTTYStdout
	defer func() { isTTYStdout = origTTY }()
	isTTYStdout = func() bool { return false }
	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()

	dir := setupNostrProjectWithItems(t, "hintproj-scope-deny-json", nil)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	ownHex := k.PubKeyHex()
	item := &state.Item{ID: "hintproj-scope-deny-json-1", Status: "active", Priority: "p2", Title: "Mine", For: ownHex, By: ownHex}
	if err := publishItemFullCreateNostr(dir, ownHex, item); err != nil {
		t.Fatalf("publish own item: %v", err)
	}
	foreign := freshKeyHex(t)

	defer resetReadyRunFlags(t)()
	defer setForFilter(t, ownHex)()
	if err := readyCmd.Flags().Set("scope", foreign); err != nil {
		t.Fatalf("setting --scope=%q: %v", foreign, err)
	}
	defer func() {
		if err := readyCmd.Flags().Set("scope", ""); err != nil {
			t.Fatalf("resetting --scope: %v", err)
		}
	}()

	// Set AFTER all fixture setup: setupNostrProjectWithItems (via
	// setupNostrCmdTest) saves/restores jsonOutput around its OWN default of
	// false, so setting it earlier gets silently clobbered back to false
	// before RunE ever runs (caught the hard way -- see this item's
	// test_decisions).
	jsonOutput = true

	stdout, stderr := runReadyCapturingBoth(t)

	var decoded []*state.Item
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("stdout must stay valid JSON in --json mode, got %q: %v", stdout, err)
	}
	if len(decoded) != 0 {
		t.Errorf("expected an empty JSON array on stdout, got %d items", len(decoded))
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected the scope gate's denial note on stderr even in --json mode -- got NOTHING (the exact silent-empty-result defect this item exists to eliminate)")
	}
	if !strings.Contains(stderr, "not a granted identity") {
		t.Errorf("expected the scope gate's own denial note (the actual cause) to be printed, got %q", stderr)
	}
	if strings.Contains(stderr, "exist for other parties") {
		t.Errorf("expected NO identity-scope hint when the --scope gate (not identity) zeroed the result, got %q", stderr)
	}
}

// TestReadyCmd_RunE_IdentityScopeHint_SilentWhenProjectHidesOwnItem covers
// ready-497 rework problem 2: printIdentityScopeHint's recompute re-applies
// filterByProject over `hidden` specifically so that a --project filter --
// not identity -- hiding the caller's OWN item does not get misreported as
// "items exist for other parties".
//
// This is deliberately a SEPARATE test from
// TestReadyCmd_RunE_IdentityScopeHint_SilentWhenLabelHidesOwnItem, which only
// drives --label. The prior mutation proof deleted the
// `filterByProject(hidden, projectFilter)` line and the LabelFilter loop
// TOGETHER, which only shows at least one of the two matters -- deleting
// ONLY the filterByProject line left the whole cmd/rd suite green, since no
// test exercised --project alone. --project is a real user-facing flag
// (readyCmd's --project), so this closes that gap: mutation tested by
// deleting ONLY `hidden = filterByProject(hidden, projectFilter)` from
// printIdentityScopeHint (leaving the label loop intact) -- confirmed this
// test alone turns red (the fixture's item, which IS for the caller, counts
// as "hidden" simply because it doesn't carry the requested --project), then
// restored.
func TestReadyCmd_RunE_IdentityScopeHint_SilentWhenProjectHidesOwnItem(t *testing.T) {
	origTTY := isTTYStdout
	defer func() { isTTYStdout = origTTY }()
	isTTYStdout = func() bool { return true }
	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	dir := setupNostrProjectWithItems(t, "hintproj-project-own", nil)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	ownHex := k.PubKeyHex()
	item := &state.Item{ID: "hintproj-project-own-1", Status: "inbox", Priority: "p2", Title: "Mine, other project", For: ownHex, By: ownHex, Project: "project-a"}
	if err := publishItemFullCreateNostr(dir, ownHex, item); err != nil {
		t.Fatalf("publish own item: %v", err)
	}

	defer resetReadyRunFlags(t)()
	defer setForFilter(t, ownHex)()
	if err := readyCmd.Flags().Set("project", "project-b"); err != nil {
		t.Fatalf("setting --project=%q: %v", "project-b", err)
	}
	defer func() {
		if err := readyCmd.Flags().Set("project", ""); err != nil {
			t.Fatalf("resetting --project: %v", err)
		}
	}()

	stdout, stderr := runReadyCapturingBoth(t)

	if strings.TrimSpace(stdout) != "nothing ready" {
		t.Errorf("expected stdout %q, got %q", "nothing ready", stdout)
	}
	if strings.Contains(stderr, "exist for other parties") {
		t.Errorf("expected NO identity-scope hint when a --project filter (not identity) hid the caller's OWN item, got %q", stderr)
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
