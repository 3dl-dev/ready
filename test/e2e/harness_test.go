// Package e2e_test tests the rd CLI binary end-to-end against a nostr-native
// project. It builds the binary via TestMain and exercises commands via
// exec.Command.
//
// CUTOVER (ready-cb6 I7): the campfire backend is gone. NewEnv provisions a
// project with the nostr-native default `rd init --name ...` (secp256k1 signer,
// local signed-event log, no campfire, no .cf identity/store). Every rd command
// runs against that substrate with a fully hermetic env (isolated HOME / RD_HOME
// / CF_HOME and an unreachable relay so writes stay local and fast).
//
// Use this layer to test: CLI flags, command behaviour, JSON output contracts, error messages.
//
// Run with:
//
//	go test ./test/e2e/
package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// unreachableRelay keeps nostr writes local (no reachable relay) so the e2e
// suite is hermetic and fast. The nostr write path treats the local signed-event
// log as the primary durable write, so create/claim/close all succeed offline.
const unreachableRelay = "ws://127.0.0.1:1"

// rdBinary is the path to the built rd binary, set once in TestMain.
var rdBinary string

// TestMain builds the rd binary once for the entire test suite.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "rd-e2e-bin-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: cannot create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	// Find module root (directory containing go.mod) by walking up from here.
	modRoot, err := findModuleRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: cannot find module root: %v\n", err)
		os.Exit(1)
	}

	rdBinary = filepath.Join(tmp, "rd")
	cmd := exec.Command("go", "build", "-o", rdBinary, "./cmd/rd")
	cmd.Dir = modRoot
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: go build failed: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// findModuleRoot walks up from the current working directory until it finds go.mod.
func findModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found")
		}
		dir = parent
	}
}

// Env holds a fully isolated, nostr-native e2e environment per test.
type Env struct {
	Home       string // isolated HOME
	RDHome     string // isolated RD_HOME (nostr signing identity + rd config)
	CFHome     string // isolated CF_HOME (legacy; the default nostr path never provisions it)
	Board      string // pinned board coordinate 30301:<owner>:<boardD>
	Owner      string // board owner pubkey (secp256k1 self) — the default --for party
	ProjectDir string // temp dir initialised with a nostr-native `rd init`
	t          *testing.T
}

// NewEnv creates a fresh nostr-native environment for one test: it runs the
// default `rd init --name ...` in an isolated project dir with hermetic HOME /
// RD_HOME / CF_HOME and an unreachable relay, then records the pinned board
// coordinate and owner pubkey for assertions.
func NewEnv(t *testing.T) *Env {
	t.Helper()

	e := &Env{
		Home:       t.TempDir(),
		RDHome:     t.TempDir(),
		CFHome:     t.TempDir(),
		ProjectDir: t.TempDir(),
		t:          t,
	}

	// Sanitise the test name into a valid project name (alnum/./-/_).
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, t.Name())

	stdout, stderr, code := e.Rd("init", "--name", name)
	if code != 0 {
		t.Fatalf("rd init failed (exit %d):\nstderr: %s\nstdout: %s", code, stderr, stdout)
	}

	// Read the pinned board coordinate + owner from .ready/config.json.
	cfgData, err := os.ReadFile(filepath.Join(e.ProjectDir, ".ready", "config.json"))
	if err != nil {
		t.Fatalf("reading .ready/config.json after init: %v", err)
	}
	var cfg struct {
		Board string `json:"board"`
	}
	if err := json.Unmarshal(cfgData, &cfg); err != nil {
		t.Fatalf("parsing .ready/config.json: %v\n%s", err, cfgData)
	}
	parts := strings.Split(cfg.Board, ":")
	if len(parts) != 3 || parts[0] != "30301" || len(parts[1]) != 64 {
		t.Fatalf("init did not pin a well-formed board coordinate: %q", cfg.Board)
	}
	e.Board = cfg.Board
	e.Owner = parts[1]
	return e
}

// hermeticEnv builds the env slice every rd invocation runs under: isolated
// HOME / RD_HOME / CF_HOME plus an unreachable relay so writes stay local.
func (e *Env) hermeticEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + e.Home,
		"RD_HOME=" + e.RDHome,
		"CF_HOME=" + e.CFHome,
		"RD_NOSTR_RELAY_URL=" + unreachableRelay,
	}
}

// Rd runs rd with the given args in the project dir.
// Returns stdout, stderr, and exit code.
func (e *Env) Rd(args ...string) (stdout, stderr string, exitCode int) {
	e.t.Helper()
	return e.RdInDir(e.ProjectDir, args...)
}

// RdInDir runs rd in a specified directory (instead of e.ProjectDir).
func (e *Env) RdInDir(dir string, args ...string) (stdout, stderr string, exitCode int) {
	e.t.Helper()
	cmd := exec.Command(rdBinary, args...)
	cmd.Dir = dir
	cmd.Env = e.hermeticEnv()
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	exitCode = 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// RdJSON runs rd with --json appended and unmarshals stdout into v.
func (e *Env) RdJSON(v interface{}, args ...string) error {
	e.t.Helper()
	args = append(args, "--json")
	stdout, stderr, code := e.Rd(args...)
	if code != 0 {
		return fmt.Errorf("rd %v exited %d: %s", args, code, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), v); err != nil {
		return fmt.Errorf("JSON parse failed: %v\noutput: %s", err, stdout)
	}
	return nil
}

// RdMustSucceed runs rd and fatals on non-zero exit. Returns stdout.
func (e *Env) RdMustSucceed(args ...string) string {
	e.t.Helper()
	stdout, stderr, code := e.Rd(args...)
	if code != 0 {
		e.t.Fatalf("rd %v exited %d\nstderr: %s\nstdout: %s", args, code, stderr, stdout)
	}
	return stdout
}

// RdMustFail runs rd and fatals on zero exit (expects failure). Returns stderr.
func (e *Env) RdMustFail(args ...string) string {
	e.t.Helper()
	stdout, stderr, code := e.Rd(args...)
	if code == 0 {
		e.t.Fatalf("rd %v expected non-zero exit but got 0\nstdout: %s", args, stdout)
	}
	return stderr
}

// Item is the JSON representation of a work item from rd --json output.
type Item struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Context     string   `json:"context"`
	Description string   `json:"description"`
	Type        string   `json:"type"`
	Level       string   `json:"level"`
	Project     string   `json:"project"`
	For         string   `json:"for"`
	By          string   `json:"by"`
	Priority    string   `json:"priority"`
	Status      string   `json:"status"`
	ETA         string   `json:"eta"`
	Due         string   `json:"due"`
	ParentID    string   `json:"parent_id"`
	BlockedBy   []string `json:"blocked_by"`
	Blocks      []string `json:"blocks"`
	Gate        string   `json:"gate"`
	GateMsgID   string   `json:"gate_msg_id"`
	WaitingOn   string   `json:"waiting_on"`
	WaitingType string   `json:"waiting_type"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
}

// ListItems runs rd list --json --all and returns parsed items.
func (e *Env) ListItems() []Item {
	e.t.Helper()
	var items []Item
	if err := e.RdJSON(&items, "list", "--all"); err != nil {
		e.t.Fatalf("ListItems: %v", err)
	}
	return items
}

// ReadyItems runs rd ready --json and returns parsed items.
func (e *Env) ReadyItems() []Item {
	e.t.Helper()
	var items []Item
	if err := e.RdJSON(&items, "ready"); err != nil {
		e.t.Fatalf("ReadyItems: %v", err)
	}
	return items
}

// ShowItem runs rd show <id> --json and returns the parsed item.
func (e *Env) ShowItem(id string) Item {
	e.t.Helper()
	var item Item
	if err := e.RdJSON(&item, "show", id); err != nil {
		e.t.Fatalf("ShowItem(%s): %v", id, err)
	}
	return item
}

// IdentityPubKeyHex returns the hex-encoded public key of the test environment's
// nostr signing identity — the board owner (secp256k1 self). This matches the
// value rd uses as the default --for party.
func (e *Env) IdentityPubKeyHex() string {
	e.t.Helper()
	return e.Owner
}

// findItem returns the first item with the given ID from a slice, or zero value.
func findItem(items []Item, id string) (Item, bool) {
	for _, it := range items {
		if it.ID == id {
			return it, true
		}
	}
	return Item{}, false
}

// containsItem returns true if items contains an item with the given ID.
func containsItem(items []Item, id string) bool {
	_, ok := findItem(items, id)
	return ok
}

// --- Harness self-tests ---

func TestHarness_EnvCreates(t *testing.T) {
	e := NewEnv(t)
	if e.ProjectDir == "" {
		t.Fatal("ProjectDir is empty")
	}
	if len(e.Owner) != 64 {
		t.Fatalf("Owner pubkey has wrong length %d: %q", len(e.Owner), e.Owner)
	}
	// The nostr-native init writes a signed-event log and pins a board — and
	// provisions NO campfire artifacts anywhere.
	if _, err := os.Stat(filepath.Join(e.ProjectDir, ".ready", "nostr-log.jsonl")); err != nil {
		t.Fatalf(".ready/nostr-log.jsonl not written by NewEnv: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.ProjectDir, ".campfire")); err == nil {
		t.Fatal("nostr-native NewEnv must NOT create .campfire/ in the project dir")
	}
}

func TestHarness_RdVersion(t *testing.T) {
	e := NewEnv(t)
	stdout, _, code := e.Rd("--version")
	if code != 0 {
		t.Fatalf("rd --version exited %d", code)
	}
	if stdout == "" {
		t.Fatal("rd --version produced no output")
	}
}

func TestHarness_CreateAndList(t *testing.T) {
	e := NewEnv(t)
	var item Item
	if err := e.RdJSON(&item, "create",
		"--title", "Harness self-test item",
		"--priority", "p1",
		"--type", "task",
		"--for", "test@example.com",
	); err != nil {
		t.Fatalf("create: %v", err)
	}
	if item.ID == "" {
		t.Fatal("create returned empty ID")
	}
	if !containsItem(e.ListItems(), item.ID) {
		t.Fatalf("created item %q not found in list", item.ID)
	}
}
