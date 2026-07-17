package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/rdconfig"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// randPubkey returns a random 64-hex-char pubkey-shaped string for tests that
// only exercise coordinate parsing/resolution (no signing).
func randPubkey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return hex.EncodeToString(b)
}

// isolatedProject builds an isolated, empty project dir (no .ready yet) with
// RD_HOME pinned into a sibling sandbox and cwd chdir'd into the project. It is
// the minimal setup for exercising initNostr / board-binding resolution without
// touching real state.
func isolatedProject(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	projectDir := filepath.Join(base, "proj")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	t.Setenv("RD_HOME", filepath.Join(base, "rdhome"))
	t.Setenv("RD_NOSTR_RELAY_URL", "")
	t.Setenv("RD_NOSTR", "")
	t.Setenv("RD_NOSTR_READ", "")
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir project: %v", err)
	}
	return projectDir
}

// TestBoardBinding_FreshCloneResolvesBoardNoFollow is the ready-f12 done
// condition: a repo dir that carries ONLY a committed .ready/board.json (the
// simulated fresh clone / worktree — no config.json, no log, no link/follow
// step) resolves the correct board coordinate and relays. This is what makes
// `rd ready` target the right board straight out of version control.
func TestBoardBinding_FreshCloneResolvesBoardNoFollow(t *testing.T) {
	dir := isolatedProject(t)

	owner := randPubkey(t)
	boardD := projectPrefix(dir)
	if boardD == "" {
		t.Fatalf("projectPrefix(%q) empty; project dir must have a >=2-char name", dir)
	}
	coord := rdSync.BoardCoord(owner, boardD)

	// The ONLY .ready/ file that exists is the committed binding — no config.json.
	binding := &rdconfig.BoardBinding{
		Board:       coord,
		ProjectName: "proj",
		RelayEndpoints: []rdconfig.RelayEndpoint{
			{URL: "wss://relay.example.test", Read: true, Write: true},
		},
	}
	if err := rdconfig.SaveBoardBinding(dir, binding); err != nil {
		t.Fatalf("SaveBoardBinding: %v", err)
	}
	// Prove there is genuinely no config.json to fall back on.
	if _, err := os.Stat(rdconfig.SyncConfigPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("expected NO .ready/config.json in a fresh-clone binding-only project; stat err=%v", err)
	}

	// Board coordinate resolves from the committed binding alone.
	if got := nostrPinnedBoard(dir); got != coord {
		t.Errorf("nostrPinnedBoard = %q, want %q (must resolve from board.json with no config.json/follow step)", got, coord)
	}

	// Cards published from this clone bind to the OWNER's board authority, not
	// the local signer's — resolved from the committed coordinate.
	gotAuthor, err := nostrBoardAuthor(dir, randPubkey(t))
	if err != nil {
		t.Fatalf("nostrBoardAuthor: %v", err)
	}
	if gotAuthor != owner {
		t.Errorf("nostrBoardAuthor = %q, want owner %q from committed board.json", gotAuthor, owner)
	}

	// Relays resolve from the committed binding (BYOR travels with the repo).
	eps := resolveRelayConfig()
	if len(eps) != 1 || eps[0].URL != "wss://relay.example.test" {
		t.Errorf("resolveRelayConfig = %+v, want the single relay from board.json", eps)
	}
}

// TestBoardBinding_ConfigJSONFallbackForLegacyInstalls verifies existing installs
// that carry only the pre-f12 mixed .ready/config.json (no board.json) still
// resolve their board — the migration path is a read-time fallback, never a break.
func TestBoardBinding_ConfigJSONFallbackForLegacyInstalls(t *testing.T) {
	dir := isolatedProject(t)
	if err := os.MkdirAll(filepath.Join(dir, ".ready"), 0o700); err != nil {
		t.Fatalf("mkdir .ready: %v", err)
	}
	owner := randPubkey(t)
	coord := rdSync.BoardCoord(owner, projectPrefix(dir))
	if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{ProjectName: "legacy", Board: coord}); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	// No board.json present.
	if _, err := os.Stat(rdconfig.BoardBindingPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("expected no board.json for legacy fixture; stat err=%v", err)
	}
	if got := nostrPinnedBoard(dir); got != coord {
		t.Errorf("nostrPinnedBoard = %q, want %q (config.json fallback for legacy installs)", got, coord)
	}
}

// TestInit_WritesBoardBindingWithNoSecrets asserts `rd init` writes the committed
// binding by default and that board.json carries ONLY the non-secret coordinate,
// project name, and relays — never the signing key, read keys, or any token/buffer.
func TestInit_WritesBoardBindingWithNoSecrets(t *testing.T) {
	dir := isolatedProject(t)

	if err := initNostr(dir, "proj", "", true, []string{"wss://relay.example.test"}, false, false); err != nil {
		t.Fatalf("initNostr: %v", err)
	}

	bindingPath := rdconfig.BoardBindingPath(dir)
	data, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatalf("reading board.json: %v", err)
	}

	// Structural guarantee: only the allowed top-level keys are present.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal board.json: %v", err)
	}
	allowed := map[string]bool{"board": true, "project_name": true, "relay_endpoints": true}
	for k := range raw {
		if !allowed[k] {
			t.Errorf("board.json carries disallowed field %q — committed binding must hold no secret/volatile material", k)
		}
	}

	// The signing secret must never appear anywhere in the committed bytes.
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	if strings.Contains(string(data), k.SecretHex()) {
		t.Fatal("board.json contains the owner signing secret — committed binding leaks key material")
	}
	for _, bad := range []string{"secret", "nsec", "beacon", "invite", "token", "priv"} {
		if strings.Contains(strings.ToLower(string(data)), bad) {
			t.Errorf("board.json contains forbidden substring %q", bad)
		}
	}

	// And the binding actually resolves the board.
	wantCoord := rdSync.BoardCoord(k.PubKeyHex(), projectPrefix(dir))
	b, err := rdconfig.LoadBoardBinding(dir)
	if err != nil {
		t.Fatalf("LoadBoardBinding: %v", err)
	}
	if b.Board != wantCoord {
		t.Errorf("board.json Board = %q, want %q", b.Board, wantCoord)
	}
	if b.ProjectName != "proj" {
		t.Errorf("board.json ProjectName = %q, want %q", b.ProjectName, "proj")
	}
	if len(b.RelayEndpoints) != 1 || b.RelayEndpoints[0].URL != "wss://relay.example.test" {
		t.Errorf("board.json RelayEndpoints = %+v, want the single --relay endpoint", b.RelayEndpoints)
	}
}

// TestInit_NoCommitBinding_SkipsBoardJSON verifies the --no-commit-binding opt-out:
// init still pins the board in the machine-local config.json but writes NO tracked
// board.json.
func TestInit_NoCommitBinding_SkipsBoardJSON(t *testing.T) {
	dir := isolatedProject(t)

	if err := initNostr(dir, "proj", "", true, nil, false, true); err != nil {
		t.Fatalf("initNostr --no-commit-binding: %v", err)
	}
	if _, err := os.Stat(rdconfig.BoardBindingPath(dir)); !os.IsNotExist(err) {
		t.Errorf("board.json exists under --no-commit-binding; want none (stat err=%v)", err)
	}
	// The board is still resolvable via the machine-local config.json.
	cfg, err := rdconfig.LoadSyncConfig(dir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	if cfg.Board == "" {
		t.Error("config.json Board empty under --no-commit-binding; init must still pin locally")
	}
}

// TestGitignore_TracksBoardJSONIgnoresVolatile asserts the committed .gitignore
// keeps every volatile .ready/ file OUT of git while tracking board.json. It
// exercises the REAL repo .gitignore against a throwaway git repo via
// `git check-ignore`.
func TestGitignore_TracksBoardJSONIgnoresVolatile(t *testing.T) {
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not in a git worktree: %v", err)
	}
	repoRoot := strings.TrimSpace(string(root))
	gitignore, err := os.ReadFile(filepath.Join(repoRoot, ".gitignore"))
	if err != nil {
		t.Fatalf("reading repo .gitignore: %v", err)
	}

	tmp := t.TempDir()
	if out, err := exec.Command("git", "-C", tmp, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".gitignore"), gitignore, 0o644); err != nil {
		t.Fatalf("writing .gitignore: %v", err)
	}
	readyDir := filepath.Join(tmp, ".ready")
	if err := os.MkdirAll(readyDir, 0o755); err != nil {
		t.Fatalf("mkdir .ready: %v", err)
	}
	files := []string{
		"board.json",
		"config.json",
		"mutations.jsonl",
		"nostr-log.jsonl",
		"nostr-pending.jsonl",
		"sync-state.json",
		"lock",
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(readyDir, f), []byte("{}"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}

	// git check-ignore exits 0 (and prints the path) when the path IS ignored,
	// exit 1 when it is NOT ignored.
	isIgnored := func(rel string) bool {
		cmd := exec.Command("git", "-C", tmp, "check-ignore", "-q", rel)
		err := cmd.Run()
		if err == nil {
			return true
		}
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return false
		}
		t.Fatalf("git check-ignore %s: unexpected error %v", rel, err)
		return false
	}

	if isIgnored(".ready/board.json") {
		t.Error(".ready/board.json is git-ignored; the committed binding MUST be tracked")
	}
	for _, f := range []string{"config.json", "mutations.jsonl", "nostr-log.jsonl", "nostr-pending.jsonl", "sync-state.json", "lock"} {
		rel := ".ready/" + f
		if !isIgnored(rel) {
			t.Errorf("%s is NOT git-ignored; volatile/machine-local .ready state must stay out of git", rel)
		}
	}

	// And board.json is actually seen by git as an untracked (trackable) file.
	out, err := exec.Command("git", "-C", tmp, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if !strings.Contains(string(out), ".ready/board.json") {
		t.Errorf("git status does not list .ready/board.json as trackable; output:\n%s", out)
	}
}
