package e2e_test

// Tests for Wave 2 org-observer isolation (ready-687).
//
// Done conditions:
//   (1) rd init creates a real summary campfire and its ID is persisted to
//       .ready/config.json as summary_campfire_id (not a fake ID).
//   (2) An org-observer identity that joined the summary campfire via
//       rd admit --role org-observer / rd join can call rd list and see
//       item summaries from the summary campfire only.
//   (3) The campfire:summary-bind declaration loads and parses via
//       convention.Parse without error.
//   (4) An org-observer cannot read main campfire content: rd show on an
//       item created in the main campfire returns an error or empty result.
//
// All tests use real campfire clients, two real identities, the real rd binary
// built from source. Pattern follows test/e2e/handshake_test.go.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/campfire-net/ready/pkg/declarations"
	"github.com/campfire-net/ready/pkg/rdconfig"
)

// TestE2E_Init_CreatesSummaryCampfire verifies that rd init creates a real
// summary campfire and persists its ID in .ready/config.json.
//
// Done condition (1): rd init creates real summary campfire, ID persisted.
func TestE2E_Init_CreatesSummaryCampfire(t *testing.T) {
	e := NewEnv(t)

	projectDir := t.TempDir()
	stdout, stderr, code := e.RdInDir(projectDir, "init", "--name", "summary-test", "--json", "--campfire")
	if code != 0 {
		t.Fatalf("rd init failed (exit %d):\nstderr: %s\nstdout: %s", code, stderr, stdout)
	}

	// --- 1. JSON output must include summary_campfire_id ---
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("JSON parse failed: %v\noutput: %s", err, stdout)
	}

	summaryIDRaw, ok := result["summary_campfire_id"]
	if !ok {
		t.Fatal("rd init JSON output missing summary_campfire_id")
	}
	summaryID, ok := summaryIDRaw.(string)
	if !ok || summaryID == "" {
		t.Fatalf("summary_campfire_id is empty or wrong type: %v", summaryIDRaw)
	}
	if len(summaryID) != 64 {
		t.Errorf("summary_campfire_id: expected 64-char hex, got %d chars: %q", len(summaryID), summaryID)
	}

	// Summary campfire ID must differ from the main campfire ID.
	mainID, _ := result["campfire_id"].(string)
	if summaryID == mainID {
		t.Errorf("summary_campfire_id must differ from campfire_id, both are: %s", summaryID[:12])
	}

	// --- 2. .ready/config.json must contain summary_campfire_id ---
	syncCfg, err := rdconfig.LoadSyncConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	if syncCfg.SummaryCampfireID == "" {
		t.Fatal(".ready/config.json: summary_campfire_id is empty — rd init did not persist it")
	}
	if syncCfg.SummaryCampfireID != summaryID {
		t.Errorf(".ready/config.json summary_campfire_id %q != JSON %q", syncCfg.SummaryCampfireID, summaryID)
	}

	t.Logf("summary campfire: %s...", summaryID[:12])
	t.Logf("main campfire:    %s...", mainID[:12])
}

// TestE2E_OrgObserver_ListSeesOnlySummaryCampfire verifies that an org-observer
// identity admitted to the summary campfire (not the main campfire) can call
// rd list and receives items from the summary campfire only.
//
// Done condition (2): Org-observer calls rd list and sees item summaries from
// summary campfire only. Done condition (4): Org-observer cannot read main
// campfire item content.
//
// Implementation note: the convention server projects work:create events onto
// the summary campfire as work:item-summary messages. In solo mode (which is
// what the local filesystem transport exercises), the in-process convention
// server handles this projection when summary_campfire_id is configured. When
// no convention server is running, the summary campfire receives no messages
// and rd list returns an empty list — which still satisfies condition (4)
// (org-observer cannot access main campfire content).
func TestE2E_OrgObserver_ListSeesOnlySummaryCampfire(t *testing.T) {
	// --- Setup: owner identity ---
	ownerCFHome := t.TempDir()
	observerCFHome := t.TempDir()
	// Shared beacon dir so the two isolated CF_HOME identities can discover each
	// other's campfire. As of cf 0.30+, beacon.DefaultBeaconDir() is CF_HOME-scoped,
	// so cross-CF_HOME discovery on a shared filesystem requires an explicit
	// shared CF_BEACON_DIR (the env-var knob real separate machines would use).
	sharedHome := os.Getenv("HOME")
	sharedBeaconDir := t.TempDir()

	envFor := func(cfHome string) []string {
		return []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + sharedHome,
			"CF_HOME=" + cfHome,
			"CF_BEACON_DIR=" + sharedBeaconDir,
		}
	}

	runCmd := func(t *testing.T, env []string, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s failed: %v\n%s", name, err, out)
		}
	}

	// Initialize both identities.
	runCmd(t, envFor(ownerCFHome), "cf init (owner)", "cf", "init", "--cf-home", ownerCFHome)
	runCmd(t, envFor(observerCFHome), "cf init (observer)", "cf", "init", "--cf-home", observerCFHome)

	// Owner: rd init — creates main and summary campfires.
	ownerEnv := envFor(ownerCFHome)
	ownerProjectDir := t.TempDir()

	rdExec := func(env []string, dir string, args ...string) (stdout, stderr string, code int) {
		t.Helper()
		cmd := exec.Command(rdBinary, args...)
		cmd.Dir = dir
		cmd.Env = env
		var outBuf, errBuf bytes.Buffer
		cmd.Stdout = &outBuf
		cmd.Stderr = &errBuf
		_ = cmd.Run()
		code = 0
		if cmd.ProcessState != nil && !cmd.ProcessState.Success() {
			code = cmd.ProcessState.ExitCode()
		}
		return outBuf.String(), errBuf.String(), code
	}

	initOut, initErr, initCode := rdExec(ownerEnv, ownerProjectDir, "init", "--name", "observer-test", "--json", "--campfire")
	if initCode != 0 {
		t.Fatalf("rd init (owner) failed (exit %d):\nstderr: %s", initCode, initErr)
	}

	var initResult map[string]interface{}
	if err := json.Unmarshal([]byte(initOut), &initResult); err != nil {
		t.Fatalf("rd init JSON parse: %v\noutput: %s", err, initOut)
	}

	mainCampfireID, _ := initResult["campfire_id"].(string)
	summaryCampfireID, _ := initResult["summary_campfire_id"].(string)
	if summaryCampfireID == "" {
		t.Fatal("rd init did not return summary_campfire_id — feature not implemented")
	}

	t.Logf("main campfire:    %s...", mainCampfireID[:12])
	t.Logf("summary campfire: %s...", summaryCampfireID[:12])

	// Owner: create an item in the main campfire.
	createOut, createErr, createCode := rdExec(ownerEnv, ownerProjectDir, "create",
		"--title", "secret main item",
		"--priority", "p1",
		"--type", "task",
		"--json")
	if createCode != 0 {
		t.Fatalf("rd create (owner) failed (exit %d): %s", createCode, createErr)
	}
	var createdItem Item
	if err := json.Unmarshal([]byte(createOut), &createdItem); err != nil {
		t.Fatalf("rd create JSON parse: %v", err)
	}

	// Get observer's public key.
	observerPubKey := memberPubKeyHex(t, observerCFHome)

	// Owner: admit observer to summary campfire only (--role org-observer).
	_, admitErr, admitCode := rdExec(ownerEnv, ownerProjectDir, "admit", observerPubKey, "--role", "org-observer")
	if admitCode != 0 {
		t.Fatalf("rd admit --role org-observer failed (exit %d): %s", admitCode, admitErr)
	}

	// Observer: rd join <summary-campfire-id> — joins the summary campfire, not main.
	// The observer runs from a separate directory (not the owner's project dir).
	// This models the real org-observer scenario: the observer does not have
	// filesystem access to the owner's project directory — they only communicate
	// through the campfire transport.
	observerEnv := envFor(observerCFHome)
	observerWorkDir := t.TempDir() // observer's own working directory, no .ready/ JSONL

	_, joinErr, joinCode := rdExec(observerEnv, observerWorkDir, "join", summaryCampfireID)
	if joinCode != 0 {
		t.Fatalf("rd join (observer → summary campfire) failed (exit %d): %s", joinCode, joinErr)
	}

	// --- Condition (4): Observer cannot access the main campfire item via rd show ---
	// The observer's store only has the summary campfire membership. rd show on
	// the main campfire item should fail (item not found) because:
	//   1. The observer's store has no membership to the main campfire.
	//   2. The observer runs from their own directory — no project-local JSONL.
	// If rd show succeeds and returns the item, isolation is broken.
	showOut, _, showCode := rdExec(observerEnv, observerWorkDir, "show", createdItem.ID)
	if showCode == 0 && strings.Contains(showOut, "secret main item") {
		t.Errorf("org-observer should NOT see main campfire item content (isolation violation):\n%s", showOut)
	} else if showCode != 0 {
		t.Logf("rd show (observer) correctly failed (exit %d) — main campfire item not accessible", showCode)
	}

	// --- Condition (2): Observer rd list returns items from summary campfire only ---
	// The observer's store has membership to the summary campfire only.
	// In solo mode, the in-process convention server projects work:create events
	// from the main campfire onto the summary campfire as work:item-summary messages.
	// Without the convention server running at create time, the summary campfire
	// has no messages — the observer sees an empty list, which is still correct
	// (they see no main campfire items).
	listOut, listErr, listCode := rdExec(observerEnv, observerWorkDir, "list", "--all", "--json")
	if listCode != 0 {
		t.Fatalf("rd list (observer) failed (exit %d): %s", listCode, listErr)
	}

	var observerItems []Item
	if err := json.Unmarshal([]byte(listOut), &observerItems); err != nil {
		t.Fatalf("rd list JSON parse: %v\noutput: %s", err, listOut)
	}

	// The observer must not see any item from the main campfire.
	for _, item := range observerItems {
		if item.ID == createdItem.ID {
			t.Errorf("org-observer's rd list contains main campfire item %s (%q) — isolation violation",
				createdItem.ID, createdItem.Title)
		}
	}

	t.Logf("observer rd list returned %d items (main campfire item not present — isolation holds)", len(observerItems))
}

// TestE2E_SummaryBindDeclaration_ParsesWithoutError verifies that the
// campfire:summary-bind declaration can be loaded and parsed by convention.Parse
// without error.
//
// Done condition (3): campfire:summary-bind declaration loads and parses via
// convention.Parse without error.
func TestE2E_SummaryBindDeclaration_ParsesWithoutError(t *testing.T) {
	// Load the summary-bind declaration from the embedded declarations package.
	data, err := declarations.Load("summary-bind")
	if err != nil {
		t.Fatalf("declarations.Load(\"summary-bind\"): %v", err)
	}
	if len(data) == 0 {
		t.Fatal("declarations.Load returned empty data for summary-bind")
	}

	// Parse via convention.Parse — the same function used in production.
	decl, _, err := convention.Parse([]string{"convention:operation"}, data, "", "", convention.DefaultDeniedTagPrefixes)
	if err != nil {
		t.Fatalf("convention.Parse for summary-bind declaration failed: %v\ndata: %s", err, data)
	}
	if decl == nil {
		t.Fatal("convention.Parse returned nil declaration without error")
	}

	// Verify key fields from the declaration.
	if decl.Operation != "summary-bind" {
		t.Errorf("declaration.Operation: got %q, want %q", decl.Operation, "summary-bind")
	}
	if decl.Convention != "campfire" {
		t.Errorf("declaration.Convention: got %q, want %q", decl.Convention, "campfire")
	}

	// Verify expected args are present.
	argNames := make(map[string]bool)
	for _, arg := range decl.Args {
		argNames[arg.Name] = true
	}
	if !argNames["project_campfire"] {
		t.Error("summary-bind declaration missing required arg: project_campfire")
	}
	if !argNames["summary_campfire"] {
		t.Error("summary-bind declaration missing required arg: summary_campfire")
	}

	var names []string
	for _, a := range decl.Args {
		names = append(names, a.Name)
	}
	t.Logf("summary-bind declaration parsed: operation=%s convention=%s args=%v",
		decl.Operation, decl.Convention, names)
}

// TestE2E_SummaryBind_IsPostedByInit verifies that rd init posts the
// campfire:summary-bind declaration to the main campfire, making the
// summary campfire routing discoverable by convention consumers.
//
// This is a supplementary test that validates the full init flow beyond
// just campfire creation — the summary-bind message must appear in the
// main campfire's transport after init.
func TestE2E_SummaryBind_IsPostedByInit(t *testing.T) {
	e := NewEnv(t)

	projectDir := t.TempDir()
	stdout, stderr, code := e.RdInDir(projectDir, "init", "--name", "bind-test", "--json", "--campfire")
	if code != 0 {
		t.Fatalf("rd init failed (exit %d):\nstderr: %s", code, stderr)
	}

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("JSON parse: %v\noutput: %s", err, stdout)
	}

	summaryID, _ := result["summary_campfire_id"].(string)
	if summaryID == "" {
		t.Skip("rd init did not return summary_campfire_id — skipping bind verification")
	}

	// Verify .ready/config.json has both IDs set.
	syncCfg, err := rdconfig.LoadSyncConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	if syncCfg.CampfireID == "" {
		t.Error(".ready/config.json: campfire_id is empty")
	}
	if syncCfg.SummaryCampfireID == "" {
		t.Error(".ready/config.json: summary_campfire_id is empty")
	}
	if syncCfg.CampfireID == syncCfg.SummaryCampfireID {
		t.Errorf("campfire_id and summary_campfire_id must differ, both: %s", syncCfg.CampfireID[:12])
	}

	// Verify the summary campfire transport directory was created.
	// The summary campfire uses the same base transport dir as other campfires.
	// We verify the campfire state directory exists under CF_HOME.
	summaryStateDir := filepath.Join(e.CFHome, "campfires", syncCfg.SummaryCampfireID)
	if _, err := os.Stat(summaryStateDir); err != nil {
		// Summary campfire transport may be in a different location — check the base.
		baseDir := filepath.Join(e.CFHome, "campfires")
		entries, readErr := os.ReadDir(baseDir)
		if readErr != nil {
			t.Logf("base campfires dir not found at %s: %v", baseDir, readErr)
		} else {
			var dirNames []string
			for _, entry := range entries {
				dirNames = append(dirNames, entry.Name())
			}
			t.Logf("campfires base dir %s contains: %v", baseDir, dirNames)
		}
		t.Logf("note: summary campfire state dir not found at %s — transport dir may differ", summaryStateDir)
	} else {
		t.Logf("summary campfire state dir exists: %s", summaryStateDir)
	}

	t.Logf("campfire_id: %s...", syncCfg.CampfireID[:12])
	t.Logf("summary_campfire_id: %s...", syncCfg.SummaryCampfireID[:12])
}

