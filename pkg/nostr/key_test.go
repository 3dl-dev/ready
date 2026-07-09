package nostr

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func TestKeyFromHex_RejectsMalformed(t *testing.T) {
	cases := []string{
		"",               // empty
		"zz",             // not hex
		"00",             // wrong length
		strings32Zeros(), // zero scalar
	}
	for _, c := range cases {
		if _, err := KeyFromHex(c); err == nil {
			t.Errorf("KeyFromHex(%q) should have failed", c)
		}
	}
}

func strings32Zeros() string {
	b := make([]byte, 64)
	for i := range b {
		b[i] = '0'
	}
	return string(b)
}

func TestKeyRoundTrip_Hex(t *testing.T) {
	k, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	k2, err := KeyFromHex(k.SecretHex())
	if err != nil {
		t.Fatalf("from hex: %v", err)
	}
	if k.SecretHex() != k2.SecretHex() {
		t.Fatalf("secret round-trip mismatch")
	}
	if k.PubKeyHex() != k2.PubKeyHex() {
		t.Fatalf("pubkey round-trip mismatch")
	}
	if len(k.PubKeyHex()) != 64 {
		t.Fatalf("x-only pubkey must be 32-byte hex, got %q", k.PubKeyHex())
	}
}

func TestSaveLoadKeyFile_PermsAndRoundTrip(t *testing.T) {
	// t.TempDir() is under $TMPDIR, outside any git work tree, so the guard's
	// git-ignore defense is a no-op; the under-root check accepts a path at the
	// allowedRoot itself. This mirrors real usage ($RD_HOME/nostr-identity.json).
	root := t.TempDir()
	path := filepath.Join(root, "nostr-identity.json")

	k, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := SaveKeyFile(path, k, root); err != nil {
		t.Fatalf("save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("key file perms = %o, want 0600 (secret must not be world-readable)", perm)
	}

	loaded, err := LoadKeyFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.SecretHex() != k.SecretHex() {
		t.Fatalf("loaded secret mismatch")
	}
}

func TestLoadOrCreatePortfolioKey_GeneratesThenLoads(t *testing.T) {
	// DefaultKeyPath is called with a real RDHome() in production. t.TempDir()
	// stands in for $RD_HOME (outside any git work tree, so the guard accepts it).
	rdHome := t.TempDir()
	path := DefaultKeyPath(rdHome)

	k1, err := LoadOrCreatePortfolioKey(path, rdHome)
	if err != nil {
		t.Fatalf("first call (generate): %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("key file not persisted: %v", err)
	}

	k2, err := LoadOrCreatePortfolioKey(path, rdHome)
	if err != nil {
		t.Fatalf("second call (load): %v", err)
	}
	if k1.SecretHex() != k2.SecretHex() {
		t.Fatalf("LoadOrCreatePortfolioKey regenerated instead of loading existing key")
	}
}

// TestLoadOrCreatePortfolioKey_ConcurrentFirstCallersConverge is the ready-53f
// regression test: it reproduces the TOCTOU race where N goroutines all race
// LoadOrCreatePortfolioKey against the SAME fresh (nonexistent) path. Against
// the old "os.Stat then SaveKeyFile" implementation, multiple goroutines
// observe the file missing, each generates its own random key, and the last
// SaveKeyFile write wins — silently overwriting the others, so some
// goroutines return a secret that does not match what ends up on disk (an
// identity mismatch). The fixed implementation must have every goroutine
// converge on exactly one secret, and exactly one key file must exist on
// disk. Run with -race to also catch any data race in the create path.
func TestLoadOrCreatePortfolioKey_ConcurrentFirstCallersConverge(t *testing.T) {
	rdHome := t.TempDir()
	path := DefaultKeyPath(rdHome)

	const n = 32
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		secrets = make(map[string]int)
		errs    []error
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			k, err := LoadOrCreatePortfolioKey(path, rdHome)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			secrets[k.SecretHex()]++
		}()
	}
	wg.Wait()

	for _, err := range errs {
		t.Errorf("LoadOrCreatePortfolioKey concurrent call failed: %v", err)
	}
	if len(secrets) != 1 {
		t.Fatalf("concurrent first-time callers diverged: got %d distinct secrets (%v), want exactly 1 — this is the ready-53f TOCTOU: concurrent callers must converge on ONE key", len(secrets), secrets)
	}

	// Confirm the winning secret is what's actually persisted on disk, and
	// that a fresh load agrees with every goroutine's in-memory result.
	onDisk, err := LoadKeyFile(path)
	if err != nil {
		t.Fatalf("load persisted key: %v", err)
	}
	var winner string
	for s := range secrets {
		winner = s
	}
	if onDisk.SecretHex() != winner {
		t.Fatalf("persisted key %q does not match the secret every goroutine converged on %q", onDisk.SecretHex(), winner)
	}

	entries, err := os.ReadDir(rdHome)
	if err != nil {
		t.Fatalf("read rd home: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one file in %s, found %d: %v", rdHome, len(entries), entries)
	}
}

// TestSaveKeyFile_RejectsPathOutsideAllowedRoot proves the resolved-path-under-
// root check (docs/design/nostr-identity-model.md §5, check 1): a key path that
// is NOT under the declared rd home is refused, even though nothing is committed.
// This is what the old lexical ".cf"-name sniff could not express — a foreign
// directory that merely shares a base name no longer passes.
func TestSaveKeyFile_RejectsPathOutsideAllowedRoot(t *testing.T) {
	allowedRoot := filepath.Join(t.TempDir(), "rdhome")
	// A sibling directory NOT under allowedRoot.
	foreign := filepath.Join(t.TempDir(), "elsewhere", ".cf")
	path := filepath.Join(foreign, "nostr-identity.json")

	k, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := SaveKeyFile(path, k, allowedRoot); err == nil {
		t.Fatalf("SaveKeyFile(%q, root=%q) should have been rejected: path is not under the rd home", path, allowedRoot)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Fatalf("SaveKeyFile must not write the file when the guard rejects the path")
	}
}

func TestSaveKeyFile_AcceptsPathUnderAllowedRoot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "nostr-identity.json")

	k, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := SaveKeyFile(path, k, root); err != nil {
		t.Fatalf("SaveKeyFile(%q, root=%q) should have succeeded: %v", path, root, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("key file not persisted: %v", err)
	}
}

// TestGuard_RejectsUnignoredPathInsideGitRepo proves the git-ignore defense-in-
// depth (§5, check 2, and adversary A6): a ".cf" (or any) directory inside a git
// work tree that does NOT ignore it is refused — the exact FALSE-SAFETY case the
// old lexical guard let through and would have committed the secret. allowedRoot
// is left "" so ONLY the git-ignore check can be responsible for the rejection.
func TestGuard_RejectsUnignoredPathInsideGitRepo(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo) // no .gitignore: nothing is ignored
	path := filepath.Join(repo, ".cf", "nostr-identity.json")

	k, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := SaveKeyFile(path, k, ""); err == nil {
		t.Fatalf("SaveKeyFile(%q) inside a git repo with no ignore rule should be rejected (it would be committed)", path)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Fatalf("SaveKeyFile must not write the file when the guard rejects the path")
	}
}

// TestGuard_AcceptsGitIgnoredPathInsideGitRepo is the counterpart: the SAME repo,
// but with the key's directory git-ignored, is accepted. This is the repo-local
// $RD_HOME (walk-up ".rd/" marker) case — a secret under an ignored dir cannot be
// committed, so it is allowed.
func TestGuard_AcceptsGitIgnoredPathInsideGitRepo(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".rd/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	rdHome := filepath.Join(repo, ".rd")
	path := filepath.Join(rdHome, "nostr-identity.json")

	k, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := SaveKeyFile(path, k, rdHome); err != nil {
		t.Fatalf("SaveKeyFile(%q) under a git-ignored dir should succeed: %v", path, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("key file not persisted: %v", err)
	}
}

// gitInit initializes a minimal git repo at dir so git check-ignore has a work
// tree to reason about. Fails the test (not skips) if git is unavailable — the
// guard's defense-in-depth is a hard requirement, not an optional check.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is required for the key-path guard's defense-in-depth check but was not found: %v", err)
	}
	cmd := exec.Command("git", "init", "-q", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}

// TestWriteKeyFileExclusive_NeverOverwrites is the identity-preserving migration
// primitive's core property: the first write persists the key; a SECOND write
// with a DIFFERENT key is a no-op (returns nil) and leaves the ORIGINAL secret on
// disk. This is what guarantees the migration copy can never clobber (or
// regenerate over) an existing identity.
func TestWriteKeyFileExclusive_NeverOverwrites(t *testing.T) {
	root := t.TempDir()
	path := DefaultKeyPath(root)

	k1, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate k1: %v", err)
	}
	if err := WriteKeyFileExclusive(path, k1, root); err != nil {
		t.Fatalf("first exclusive write: %v", err)
	}

	k2, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate k2: %v", err)
	}
	if k1.SecretHex() == k2.SecretHex() {
		t.Fatalf("test precondition: two generated keys must differ")
	}
	// Second write with a different key must NOT overwrite and must NOT error.
	if err := WriteKeyFileExclusive(path, k2, root); err != nil {
		t.Fatalf("second exclusive write should converge (no error): %v", err)
	}

	onDisk, err := LoadKeyFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if onDisk.SecretHex() != k1.SecretHex() {
		t.Fatalf("WriteKeyFileExclusive overwrote the original identity: on-disk=%s want=%s", onDisk.PubKeyHex(), k1.PubKeyHex())
	}
}

// TestStoredPubKeyHex_MatchesDerived verifies the self-consistency tripwire the
// startup assertion relies on: a freshly written key file records a pubkey_hex
// that equals the pubkey derived from its secret.
func TestStoredPubKeyHex_MatchesDerived(t *testing.T) {
	root := t.TempDir()
	path := DefaultKeyPath(root)
	k, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := SaveKeyFile(path, k, root); err != nil {
		t.Fatalf("save: %v", err)
	}
	stored, err := StoredPubKeyHex(path)
	if err != nil {
		t.Fatalf("stored pubkey: %v", err)
	}
	if stored != k.PubKeyHex() {
		t.Fatalf("stored pubkey %q != derived %q", stored, k.PubKeyHex())
	}
}
