// Package e2e_test tests the rd CLI binary end-to-end against a real campfire.
// It builds the binary via TestMain and exercises commands via exec.Command.
//
// Use this layer to test: CLI flags, command behaviour, JSON output contracts, error messages.
// Use test/integration/ to test: the Go API, state derivation, view predicates.
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
	"sync"
	"testing"
)

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

// Env holds a fully isolated e2e environment per test.
type Env struct {
	CFHome       string // campfire home dir (identity + store)
	CampfireID   string // the project campfire ID
	TransportDir string // filesystem transport directory for the campfire (from cf create --json)
	ProjectDir   string // temp dir with .campfire/root
	t            *testing.T
}

var (
	cfOnce sync.Once
	cfErr  error
)

// NewEnv creates a fresh cf environment for one test.
// Uses cf init + cf create to create a real campfire.
func NewEnv(t *testing.T) *Env {
	t.Helper()

	cfHome := t.TempDir()

	// cf init — create identity in cfHome
	initCmd := exec.Command("cf", "init", "--cf-home", cfHome)
	initCmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("cf init failed: %v\n%s", err, out)
	}

	// cf create — create real campfire
	createCmd := exec.Command("cf", "create", "--cf-home", cfHome,
		"--description", fmt.Sprintf("e2e-test-%s", t.Name()),
		"--json")
	createCmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	out, err := createCmd.Output()
	if err != nil {
		t.Fatalf("cf create failed: %v\n%s", err, out)
	}

	// cf 0.16+ prints "Wrote <path>" before the JSON object; find the first '{'.
	jsonStart := bytes.IndexByte(out, '{')
	if jsonStart < 0 {
		t.Fatalf("cf create: no JSON object in output: %s", out)
	}
	var result struct {
		CampfireID   string `json:"campfire_id"`
		TransportDir string `json:"transport_dir"`
	}
	if err := json.Unmarshal(out[jsonStart:], &result); err != nil {
		t.Fatalf("cf create JSON parse failed: %v\noutput: %s", err, out)
	}
	if result.CampfireID == "" {
		t.Fatalf("cf create returned empty campfire_id; output: %s", out)
	}

	// Create project dir with .campfire/root
	projectDir := t.TempDir()
	campfireDir := filepath.Join(projectDir, ".campfire")
	if err := os.MkdirAll(campfireDir, 0700); err != nil {
		t.Fatalf("mkdir .campfire: %v", err)
	}
	if err := os.WriteFile(filepath.Join(campfireDir, "root"), []byte(result.CampfireID), 0600); err != nil {
		t.Fatalf("write .campfire/root: %v", err)
	}

	return &Env{
		CFHome:       cfHome,
		CampfireID:   result.CampfireID,
		TransportDir: result.TransportDir,
		ProjectDir:   projectDir,
		t:            t,
	}
}

// newCampfireProjectDir creates an ADDITIONAL campfire-backed project directory
// sharing this env's CF_HOME identity: it runs `cf create` and writes
// .campfire/root, exactly as NewEnv does for e.ProjectDir. Post-cutover the default
// `rd init` is nostr-native and provisions NO campfire, so campfire-vestigial
// commands (register, join, admit — the campfire-org topology surface that I5/I7
// deletes) can no longer be set up via `rd init`. Tests that exercise those
// campfire features build their substrate directly through this helper instead.
// Returns the new project dir and its campfire ID.
func (e *Env) newCampfireProjectDir(t *testing.T) (projectDir, campfireID string) {
	t.Helper()
	createCmd := exec.Command("cf", "create", "--cf-home", e.CFHome,
		"--description", fmt.Sprintf("e2e-extra-%s", t.Name()),
		"--json")
	createCmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	out, err := createCmd.Output()
	if err != nil {
		t.Fatalf("cf create (extra) failed: %v\n%s", err, out)
	}
	jsonStart := bytes.IndexByte(out, '{')
	if jsonStart < 0 {
		t.Fatalf("cf create (extra): no JSON object in output: %s", out)
	}
	var result struct {
		CampfireID string `json:"campfire_id"`
	}
	if err := json.Unmarshal(out[jsonStart:], &result); err != nil {
		t.Fatalf("cf create (extra) JSON parse failed: %v\noutput: %s", err, out)
	}
	if result.CampfireID == "" {
		t.Fatalf("cf create (extra) returned empty campfire_id; output: %s", out)
	}
	projectDir = t.TempDir()
	campfireDir := filepath.Join(projectDir, ".campfire")
	if err := os.MkdirAll(campfireDir, 0700); err != nil {
		t.Fatalf("mkdir .campfire (extra): %v", err)
	}
	if err := os.WriteFile(filepath.Join(campfireDir, "root"), []byte(result.CampfireID), 0600); err != nil {
		t.Fatalf("write .campfire/root (extra): %v", err)
	}
	return projectDir, result.CampfireID
}

// Rd runs rd with the given args in the project dir.
// Returns stdout, stderr, and exit code.
func (e *Env) Rd(args ...string) (stdout, stderr string, exitCode int) {
	e.t.Helper()
	cmd := exec.Command(rdBinary, args...)
	cmd.Dir = e.ProjectDir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"CF_HOME=" + e.CFHome,
	}
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

// RdInDir runs rd in a specified directory (instead of e.ProjectDir).
func (e *Env) RdInDir(dir string, args ...string) (stdout, stderr string, exitCode int) {
	e.t.Helper()
	cmd := exec.Command(rdBinary, args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"CF_HOME=" + e.CFHome,
	}
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

// IdentityPubKeyHex returns the hex-encoded public key of the test environment's identity.
// This matches the value that rd uses as the default --for party.
func (e *Env) IdentityPubKeyHex() string {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(e.CFHome, "identity.json"))
	if err != nil {
		e.t.Fatalf("reading identity.json: %v", err)
	}
	var id struct {
		PublicKey []byte `json:"public_key"` // JSON: base64-encoded bytes
	}
	if err := json.Unmarshal(data, &id); err != nil {
		e.t.Fatalf("parsing identity.json: %v", err)
	}
	return fmt.Sprintf("%x", id.PublicKey)
}

// memberPubKeyHex reads the public key from a cf identity.json file at the given
// CF_HOME and returns the hex-encoded public key. Used by multi-identity e2e
// tests to obtain a second identity's pubkey for campfire-org setup (e.g. `cf
// admit`) that isn't the test's own Env identity.
func memberPubKeyHex(t *testing.T, cfHome string) string {
	t.Helper()

	identityPath := filepath.Join(cfHome, "identity.json")
	data, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatalf("reading member identity.json at %s: %v", identityPath, err)
	}

	var id struct {
		PublicKey []byte `json:"public_key"`
	}
	if err := json.Unmarshal(data, &id); err != nil {
		t.Fatalf("parsing member identity.json: %v", err)
	}

	return fmt.Sprintf("%x", id.PublicKey)
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
	if e.CFHome == "" {
		t.Fatal("CFHome is empty")
	}
	if len(e.CampfireID) != 64 {
		t.Fatalf("CampfireID has wrong length %d: %q", len(e.CampfireID), e.CampfireID)
	}
	rootFile := filepath.Join(e.ProjectDir, ".campfire", "root")
	data, err := os.ReadFile(rootFile)
	if err != nil {
		t.Fatalf("reading .campfire/root: %v", err)
	}
	got := string(data)
	for len(got) > 0 && (got[len(got)-1] == '\n' || got[len(got)-1] == '\r') {
		got = got[:len(got)-1]
	}
	if got != e.CampfireID {
		t.Fatalf(".campfire/root content mismatch: got %q, want %q", got, e.CampfireID)
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
