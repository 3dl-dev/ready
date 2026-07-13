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

// runIsolatedRd runs the built rd binary in projectDir with a fully hermetic
// environment: HOME, RD_HOME and CF_HOME all point at isolated temp dirs so the
// command touches nothing outside the sandbox and the real ~/.cf / ~/.config/rd
// are never read or written.
func runIsolatedRd(projectDir, home, rdHome, cfHome string, args ...string) (stdout, stderr string, code int) {
	cmd := exec.Command(rdBinary, args...)
	cmd.Dir = projectDir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"RD_HOME=" + rdHome,
		"CF_HOME=" + cfHome,
	}
	var o, e bytes.Buffer
	cmd.Stdout = &o
	cmd.Stderr = &e
	err := cmd.Run()
	code = 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	return o.String(), e.String(), code
}

// TestE2E_Init_NostrNative asserts the post-cutover nostr-native `rd init`
// (ready-6ef S-init / DONE#1). A fresh `rd init --name x`:
//   - writes .ready/nostr-log.jsonl (the signed 30301 board event),
//   - pins a board coordinate 30301:<owner>:<boardD> in .ready/config.json,
//   - writes NO .campfire/ and NO .cf/ anywhere (the default path provisions
//     no campfire identity), and
//   - prints the board coordinate, NOT a 'campfire: ...' /
//     'declarations: N operations published' line.
func TestE2E_Init_NostrNative(t *testing.T) {
	projectDir := t.TempDir()
	home := t.TempDir()
	rdHome := t.TempDir()
	cfHome := filepath.Join(t.TempDir(), "cf") // must NOT be created by the default path

	stdout, stderr, code := runIsolatedRd(projectDir, home, rdHome, cfHome, "init", "--name", "nostrproj")
	if code != 0 {
		t.Fatalf("rd init failed (exit %d):\nstderr: %s\nstdout: %s", code, stderr, stdout)
	}

	// --- .ready/nostr-log.jsonl exists and holds at least the board event ---
	logPath := filepath.Join(projectDir, ".ready", "nostr-log.jsonl")
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf(".ready/nostr-log.jsonl not written: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf(".ready/nostr-log.jsonl is empty — expected a signed 30301 board event")
	}

	// --- .ready/config.json pins a well-formed board coordinate ---
	cfgData, err := os.ReadFile(filepath.Join(projectDir, ".ready", "config.json"))
	if err != nil {
		t.Fatalf("reading .ready/config.json: %v", err)
	}
	var cfg struct {
		Board       string `json:"board"`
		ProjectName string `json:"project_name"`
	}
	if err := json.Unmarshal(cfgData, &cfg); err != nil {
		t.Fatalf("parsing .ready/config.json: %v\n%s", err, cfgData)
	}
	if cfg.ProjectName != "nostrproj" {
		t.Errorf("project_name: got %q, want nostrproj", cfg.ProjectName)
	}
	parts := strings.Split(cfg.Board, ":")
	if len(parts) != 3 || parts[0] != "30301" {
		t.Fatalf("pinned board coordinate malformed: %q (want 30301:<owner>:<boardD>)", cfg.Board)
	}
	if len(parts[1]) != 64 {
		t.Fatalf("board owner pubkey wrong length %d in %q (want 64 hex)", len(parts[1]), cfg.Board)
	}
	if parts[2] == "" {
		t.Fatalf("board d is empty in %q", cfg.Board)
	}

	// --- NO campfire / .cf artifacts anywhere ---
	if _, err := os.Stat(filepath.Join(projectDir, ".campfire")); err == nil {
		t.Errorf("default nostr init must NOT create .campfire/ in the project dir")
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".cf")); err == nil {
		t.Errorf("default nostr init must NOT create .cf/ in the project dir")
	}
	if _, err := os.Stat(cfHome); err == nil {
		t.Errorf("default nostr init must NOT provision the campfire home %q", cfHome)
	}

	// --- output prints the board coordinate, not campfire/declarations noise ---
	if !strings.Contains(stdout, cfg.Board) {
		t.Errorf("stdout should print the pinned board coordinate %q; got:\n%s", cfg.Board, stdout)
	}
	if strings.Contains(stdout, "declarations") {
		t.Errorf("stdout must not mention campfire 'declarations'; got:\n%s", stdout)
	}
	if strings.Contains(stdout, "campfire:") {
		t.Errorf("stdout must not print a 'campfire:' line; got:\n%s", stdout)
	}
}

// TestE2E_Init_NostrNative_JSON asserts the nostr-native init --json output
// carries the board coordinate and project name and omits campfire fields.
func TestE2E_Init_NostrNative_JSON(t *testing.T) {
	projectDir := t.TempDir()
	home := t.TempDir()
	rdHome := t.TempDir()
	cfHome := filepath.Join(t.TempDir(), "cf")

	stdout, stderr, code := runIsolatedRd(projectDir, home, rdHome, cfHome, "init", "--name", "jsonproj", "--json")
	if code != 0 {
		t.Fatalf("rd init --json failed (exit %d):\nstderr: %s", code, stderr)
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("JSON parse failed: %v\noutput: %s", err, stdout)
	}
	if result["name"] != "jsonproj" {
		t.Errorf("name: got %v, want jsonproj", result["name"])
	}
	board, _ := result["board"].(string)
	if !strings.HasPrefix(board, "30301:") {
		t.Errorf("board: got %v, want a 30301:<owner>:<boardD> coordinate", result["board"])
	}
	if _, hasCampfire := result["campfire_id"]; hasCampfire {
		t.Errorf("nostr-native init --json must not emit campfire_id; got: %v", result["campfire_id"])
	}
}

// TestE2E_Init_NostrNative_DoubleInitFails asserts a second init in an already
// initialized dir is rejected.
func TestE2E_Init_NostrNative_DoubleInitFails(t *testing.T) {
	projectDir := t.TempDir()
	home := t.TempDir()
	rdHome := t.TempDir()
	cfHome := filepath.Join(t.TempDir(), "cf")

	if _, stderr, code := runIsolatedRd(projectDir, home, rdHome, cfHome, "init", "--name", "once"); code != 0 {
		t.Fatalf("first init failed: %s", stderr)
	}
	_, stderr, code := runIsolatedRd(projectDir, home, rdHome, cfHome, "init", "--name", "twice")
	if code == 0 {
		t.Fatalf("second init should fail when already initialized")
	}
	if !strings.Contains(stderr, "already") {
		t.Errorf("expected 'already' in error, got: %q", stderr)
	}
}
