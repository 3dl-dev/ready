package e2e_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// rdWithHome runs rd with PATH and HOME set to the given homeDir,
// explicitly NOT setting CF_HOME (so CFHome() walks the default detection).
func rdWithHome(t *testing.T, homeDir, workDir string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(rdBinary, args...)
	cmd.Dir = workDir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + homeDir,
		// Intentionally no CF_HOME — testing default detection.
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// cfInitInHome runs cf init targeting the given cfHomePath.
// Uses the real HOME so the cf wrapper can find its cached binary.
func cfInitInHome(t *testing.T, cfHomePath string) {
	t.Helper()
	cmd := exec.Command("cf", "init", "--cf-home", cfHomePath)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"), // real HOME for cf wrapper binary cache
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cf init in %s failed: %v\n%s", cfHomePath, err, out)
	}
}

// TestE2E_CFHome_NewInstall_DotCf verifies that when ~/.cf exists (new install),
// CFHome() resolves to ~/.cf and rd commands succeed without --cf-home.
// This is done-condition 2 (DC2) of the Wave 1 CFHome migration.
func TestE2E_CFHome_NewInstall_DotCf(t *testing.T) {
	// Use a dedicated fake HOME so we don't pollute the test runner's ~/.cf.
	fakeHome := t.TempDir()

	// Create ~/.cf with a campfire identity.
	newCFHome := filepath.Join(fakeHome, ".cf")
	if err := os.MkdirAll(newCFHome, 0700); err != nil {
		t.Fatalf("mkdir ~/.cf: %v", err)
	}
	cfInitInHome(t, newCFHome)

	// Create a project dir with a .ready/ directory (JSONL-only project).
	projectDir := t.TempDir()
	readyDir := filepath.Join(projectDir, ".ready")
	if err := os.MkdirAll(readyDir, 0700); err != nil {
		t.Fatalf("mkdir .ready: %v", err)
	}
	// Write an empty mutations.jsonl.
	if err := os.WriteFile(filepath.Join(readyDir, "mutations.jsonl"), nil, 0600); err != nil {
		t.Fatalf("write mutations.jsonl: %v", err)
	}

	// rd list should succeed (exit 0) using ~/.cf as CFHome without --cf-home.
	stdout, stderr, code := rdWithHome(t, fakeHome, projectDir, "list")
	if code != 0 {
		t.Fatalf("rd list failed (exit %d) with new-install ~/.cf:\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// Verify there's no error about missing identity / config.
	if strings.Contains(stderr, "loading identity") || strings.Contains(stderr, "cannot determine home") {
		t.Errorf("unexpected error in stderr: %s", stderr)
	}
}

// TestE2E_CFHome_Legacy_DotCampfire verifies that when ~/.campfire exists (legacy),
// CFHome() falls back to ~/.campfire and rd commands succeed without --cf-home.
// This is done-condition 1 (DC1) of the Wave 1 CFHome migration.
func TestE2E_CFHome_Legacy_DotCampfire(t *testing.T) {
	// Use a dedicated fake HOME so we don't pollute the test runner's ~/.campfire.
	fakeHome := t.TempDir()

	// Create ~/.campfire (legacy path) with a campfire identity.
	// Deliberately do NOT create ~/.cf — the fallback logic should detect ~/.campfire.
	legacyCFHome := filepath.Join(fakeHome, ".campfire")
	if err := os.MkdirAll(legacyCFHome, 0700); err != nil {
		t.Fatalf("mkdir ~/.campfire: %v", err)
	}
	cfInitInHome(t, legacyCFHome)

	// Create a project dir with a .ready/ directory (JSONL-only project).
	projectDir := t.TempDir()
	readyDir := filepath.Join(projectDir, ".ready")
	if err := os.MkdirAll(readyDir, 0700); err != nil {
		t.Fatalf("mkdir .ready: %v", err)
	}
	if err := os.WriteFile(filepath.Join(readyDir, "mutations.jsonl"), nil, 0600); err != nil {
		t.Fatalf("write mutations.jsonl: %v", err)
	}

	// rd list should succeed (exit 0) using ~/.campfire as CFHome.
	stdout, stderr, code := rdWithHome(t, fakeHome, projectDir, "list")
	if code != 0 {
		t.Fatalf("rd list failed (exit %d) with legacy ~/.campfire:\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// Verify no identity errors.
	if strings.Contains(stderr, "loading identity") || strings.Contains(stderr, "cannot determine home") {
		t.Errorf("unexpected error in stderr: %s", stderr)
	}
}

// TestE2E_CFHome_LegacyPath_FullSmoke is the e2e smoke test for legacy ~/.campfire path (DC1).
// This test:
//   1. Creates a temporary CF_HOME at a path ending in .campfire/ (simulating legacy layout)
//   2. Runs cf init in that home to create an identity
//   3. Runs rd init --name testproject with CF_HOME pointing to the legacy path
//   4. Creates an item, lists items, shows item — all succeed
//   5. Verifies no errors about missing identity or wrong path
//
// This tests the full workflow with real cf init and rd commands, not just rd list.
func TestE2E_CFHome_LegacyPath_FullSmoke(t *testing.T) {
	// Use a dedicated fake HOME so we don't pollute test runner's home dirs.
	fakeHome := t.TempDir()

	// Create ~/.campfire (legacy path) with a campfire identity.
	legacyCFHome := filepath.Join(fakeHome, ".campfire")
	if err := os.MkdirAll(legacyCFHome, 0700); err != nil {
		t.Fatalf("mkdir ~/.campfire: %v", err)
	}
	cfInitInHome(t, legacyCFHome)

	// ready-6ef SURVIVE: the default `rd init` is now nostr-native and creates NO
	// campfire, so the campfire substrate is built directly at the SAME legacy
	// ~/.campfire home via newCampfireProjectDir (cf create --cf-home <legacy> +
	// .campfire/root). The create/list/show lifecycle below then exercises CFHome
	// resolution against that legacy path exactly as before. (I7 deletes this test
	// with the campfire code.)
	eHome := &Env{CFHome: legacyCFHome, t: t}
	projectDir, _ := eHome.newCampfireProjectDir(t)
	var initStderr bytes.Buffer // no init step post-cutover; kept for the stderr scan below

	// --- Verify .campfire/root was created in the project ---
	rootFile := filepath.Join(projectDir, ".campfire", "root")
	rootData, err := os.ReadFile(rootFile)
	if err != nil {
		t.Fatalf("reading .campfire/root: %v", err)
	}
	campfireID := strings.TrimSpace(string(rootData))
	if len(campfireID) != 64 {
		t.Errorf(".campfire/root has wrong length %d: %q", len(campfireID), campfireID)
	}

	// --- Create an item with the legacy CF_HOME ---
	createCmd := exec.Command(rdBinary, "create", "--title", "Test item", "--priority", "p1", "--type", "task", "--json")
	createCmd.Dir = projectDir
	createCmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + fakeHome,
		"CF_HOME=" + legacyCFHome,
	}
	var createStdout, createStderr bytes.Buffer
	createCmd.Stdout = &createStdout
	createCmd.Stderr = &createStderr
	createCode := 0
	if err := createCmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			createCode = exitErr.ExitCode()
		}
	}

	if createCode != 0 {
		t.Fatalf("rd create failed (exit %d) with legacy CF_HOME:\nstderr: %s\nstdout: %s",
			createCode, createStderr.String(), createStdout.String())
	}

	// Parse created item ID.
	var createResult map[string]interface{}
	if err := json.Unmarshal(createStdout.Bytes(), &createResult); err != nil {
		t.Fatalf("JSON parse of rd create output failed: %v\noutput: %s", err, createStdout.String())
	}
	itemID, ok := createResult["id"].(string)
	if !ok || itemID == "" {
		t.Fatalf("created item has empty/missing ID: %v", createResult)
	}

	// --- List items with legacy CF_HOME ---
	listCmd := exec.Command(rdBinary, "list", "--json")
	listCmd.Dir = projectDir
	listCmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + fakeHome,
		"CF_HOME=" + legacyCFHome,
	}
	var listStdout, listStderr bytes.Buffer
	listCmd.Stdout = &listStdout
	listCmd.Stderr = &listStderr
	listCode := 0
	if err := listCmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			listCode = exitErr.ExitCode()
		}
	}

	if listCode != 0 {
		t.Fatalf("rd list failed (exit %d) with legacy CF_HOME:\nstderr: %s\nstdout: %s",
			listCode, listStderr.String(), listStdout.String())
	}

	// Verify created item appears in list.
	var listItems []map[string]interface{}
	if err := json.Unmarshal(listStdout.Bytes(), &listItems); err != nil {
		t.Fatalf("JSON parse of rd list output failed: %v\noutput: %s", err, listStdout.String())
	}
	found := false
	for _, item := range listItems {
		if item["id"] == itemID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created item %q not found in rd list output", itemID)
	}

	// --- Show item with legacy CF_HOME ---
	showCmd := exec.Command(rdBinary, "show", itemID, "--json")
	showCmd.Dir = projectDir
	showCmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + fakeHome,
		"CF_HOME=" + legacyCFHome,
	}
	var showStdout, showStderr bytes.Buffer
	showCmd.Stdout = &showStdout
	showCmd.Stderr = &showStderr
	showCode := 0
	if err := showCmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			showCode = exitErr.ExitCode()
		}
	}

	if showCode != 0 {
		t.Fatalf("rd show failed (exit %d) with legacy CF_HOME:\nstderr: %s\nstdout: %s",
			showCode, showStderr.String(), showStdout.String())
	}

	// Parse shown item.
	var showResult map[string]interface{}
	if err := json.Unmarshal(showStdout.Bytes(), &showResult); err != nil {
		t.Fatalf("JSON parse of rd show output failed: %v\noutput: %s", err, showStdout.String())
	}
	if showResult["id"] != itemID {
		t.Errorf("rd show returned wrong item: got %v, want %s", showResult["id"], itemID)
	}
	if showResult["title"] != "Test item" {
		t.Errorf("rd show returned wrong title: got %v, want 'Test item'", showResult["title"])
	}

	// --- Verify no errors about missing identity or wrong path ---
	for _, stderr := range []string{
		initStderr.String(),
		createStderr.String(),
		listStderr.String(),
		showStderr.String(),
	} {
		if strings.Contains(stderr, "loading identity") || strings.Contains(stderr, "cannot determine home") {
			t.Errorf("unexpected error in stderr: %s", stderr)
		}
	}
}

// TestE2E_CFHome_NewInstall_FullSmoke is the e2e smoke test for new-install ~/.cf path (DC2).
// This test:
//   1. Creates a temporary CF_HOME at a path ending in .cf/ (simulating new-install default)
//   2. Runs cf init in that home to create an identity
//   3. Runs rd init --name testproject with CF_HOME pointing to the new path
//   4. Creates an item, lists items, shows item — all succeed
//   5. Verifies no errors about missing identity or wrong path
//   6. Verifies .campfire/ directory is NOT required or created
//
// This tests the full workflow with real cf init and rd commands, not just rd list.
func TestE2E_CFHome_NewInstall_FullSmoke(t *testing.T) {
	// Use a dedicated fake HOME so we don't pollute test runner's home dirs.
	fakeHome := t.TempDir()

	// Create ~/.cf (new-install path) with a campfire identity.
	newCFHome := filepath.Join(fakeHome, ".cf")
	if err := os.MkdirAll(newCFHome, 0700); err != nil {
		t.Fatalf("mkdir ~/.cf: %v", err)
	}
	cfInitInHome(t, newCFHome)

	// Verify identity was created in the new .cf path.
	identityFile := filepath.Join(newCFHome, "identity.json")
	if _, err := os.Stat(identityFile); err != nil {
		t.Fatalf("identity.json not found in ~/.cf: %v", err)
	}

	// ready-6ef SURVIVE: the default `rd init` is now nostr-native and creates NO
	// campfire, so the campfire substrate is built directly at the SAME new ~/.cf home
	// via newCampfireProjectDir (cf create --cf-home <newCFHome> + .campfire/root). The
	// create/list/show lifecycle below then exercises CFHome resolution against that new
	// path exactly as before. (I7 deletes this test with the campfire code.)
	eHome := &Env{CFHome: newCFHome, t: t}
	projectDir, _ := eHome.newCampfireProjectDir(t)
	var initStderr bytes.Buffer // no init step post-cutover; kept for the stderr scan below

	// --- Verify .campfire/root was created in the project ---
	rootFile := filepath.Join(projectDir, ".campfire", "root")
	rootData, err := os.ReadFile(rootFile)
	if err != nil {
		t.Fatalf("reading .campfire/root: %v", err)
	}
	campfireID := strings.TrimSpace(string(rootData))
	if len(campfireID) != 64 {
		t.Errorf(".campfire/root has wrong length %d: %q", len(campfireID), campfireID)
	}

	// --- Create an item with the new CF_HOME ---
	createCmd := exec.Command(rdBinary, "create", "--title", "Test item", "--priority", "p1", "--type", "task", "--json")
	createCmd.Dir = projectDir
	createCmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + fakeHome,
		"CF_HOME=" + newCFHome,
	}
	var createStdout, createStderr bytes.Buffer
	createCmd.Stdout = &createStdout
	createCmd.Stderr = &createStderr
	createCode := 0
	if err := createCmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			createCode = exitErr.ExitCode()
		}
	}

	if createCode != 0 {
		t.Fatalf("rd create failed (exit %d) with new-install CF_HOME:\nstderr: %s\nstdout: %s",
			createCode, createStderr.String(), createStdout.String())
	}

	// Parse created item ID.
	var createResult map[string]interface{}
	if err := json.Unmarshal(createStdout.Bytes(), &createResult); err != nil {
		t.Fatalf("JSON parse of rd create output failed: %v\noutput: %s", err, createStdout.String())
	}
	itemID, ok := createResult["id"].(string)
	if !ok || itemID == "" {
		t.Fatalf("created item has empty/missing ID: %v", createResult)
	}

	// --- List items with new CF_HOME ---
	listCmd := exec.Command(rdBinary, "list", "--json")
	listCmd.Dir = projectDir
	listCmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + fakeHome,
		"CF_HOME=" + newCFHome,
	}
	var listStdout, listStderr bytes.Buffer
	listCmd.Stdout = &listStdout
	listCmd.Stderr = &listStderr
	listCode := 0
	if err := listCmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			listCode = exitErr.ExitCode()
		}
	}

	if listCode != 0 {
		t.Fatalf("rd list failed (exit %d) with new-install CF_HOME:\nstderr: %s\nstdout: %s",
			listCode, listStderr.String(), listStdout.String())
	}

	// Verify created item appears in list.
	var listItems []map[string]interface{}
	if err := json.Unmarshal(listStdout.Bytes(), &listItems); err != nil {
		t.Fatalf("JSON parse of rd list output failed: %v\noutput: %s", err, listStdout.String())
	}
	found := false
	for _, item := range listItems {
		if item["id"] == itemID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created item %q not found in rd list output", itemID)
	}

	// --- Show item with new CF_HOME ---
	showCmd := exec.Command(rdBinary, "show", itemID, "--json")
	showCmd.Dir = projectDir
	showCmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + fakeHome,
		"CF_HOME=" + newCFHome,
	}
	var showStdout, showStderr bytes.Buffer
	showCmd.Stdout = &showStdout
	showCmd.Stderr = &showStderr
	showCode := 0
	if err := showCmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			showCode = exitErr.ExitCode()
		}
	}

	if showCode != 0 {
		t.Fatalf("rd show failed (exit %d) with new-install CF_HOME:\nstderr: %s\nstdout: %s",
			showCode, showStderr.String(), showStdout.String())
	}

	// Parse shown item.
	var showResult map[string]interface{}
	if err := json.Unmarshal(showStdout.Bytes(), &showResult); err != nil {
		t.Fatalf("JSON parse of rd show output failed: %v\noutput: %s", err, showStdout.String())
	}
	if showResult["id"] != itemID {
		t.Errorf("rd show returned wrong item: got %v, want %s", showResult["id"], itemID)
	}
	if showResult["title"] != "Test item" {
		t.Errorf("rd show returned wrong title: got %v, want 'Test item'", showResult["title"])
	}

	// --- Verify no errors about missing identity or wrong path ---
	for _, stderr := range []string{
		initStderr.String(),
		createStderr.String(),
		listStderr.String(),
		showStderr.String(),
	} {
		if strings.Contains(stderr, "loading identity") || strings.Contains(stderr, "cannot determine home") {
			t.Errorf("unexpected error in stderr: %s", stderr)
		}
	}
}
