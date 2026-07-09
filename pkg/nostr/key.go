package nostr

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	btcec "github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// Key is a portfolio secp256k1 signing identity for nostr. The secret is held
// only in memory; callers persist it via SaveKeyFile. PubKeyHex returns the
// x-only (32-byte) public key nostr uses.
type Key struct {
	priv *btcec.PrivateKey
}

// GenerateKey creates a fresh random secp256k1 keypair using crypto/rand.
func GenerateKey() (*Key, error) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("nostr: generate key: %w", err)
	}
	return &Key{priv: priv}, nil
}

// KeyFromHex loads a key from a 32-byte lowercase-hex secret (the nostr "nsec"
// raw form). It rejects malformed or out-of-range scalars.
func KeyFromHex(secHex string) (*Key, error) {
	b, err := hex.DecodeString(secHex)
	if err != nil {
		return nil, fmt.Errorf("nostr: decode secret hex: %w", err)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("nostr: secret must be 32 bytes, got %d", len(b))
	}
	// PrivKeyFromBytes does not itself reject zero; guard explicitly so a
	// degenerate all-zero secret can never be used.
	allZero := true
	for _, x := range b {
		if x != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return nil, errors.New("nostr: secret key is zero")
	}
	priv, _ := btcec.PrivKeyFromBytes(b)
	return &Key{priv: priv}, nil
}

// SecretHex returns the 32-byte secret as lowercase hex. Handle with care — this
// is the private key. It exists so the portfolio key can be persisted and so
// tests can cross-check against external tooling (nak).
func (k *Key) SecretHex() string {
	b := k.priv.Serialize()
	return hex.EncodeToString(b)
}

// PubKeyHex returns the x-only 32-byte public key as lowercase hex, the form
// nostr events carry in their "pubkey" field.
func (k *Key) PubKeyHex() string {
	xonly := schnorr.SerializePubKey(k.priv.PubKey())
	return hex.EncodeToString(xonly)
}

// keyFile is the on-disk representation of a portfolio nostr key. It lives under
// the rd home ($RD_HOME, default ~/.config/rd) and, like campfire's identity.json,
// carries the secret — so it must never be committed and must be written 0600.
type keyFile struct {
	Version   int    `json:"version"`
	SecretHex string `json:"secret_hex"`
	PubKeyHex string `json:"pubkey_hex"`
}

// requireIgnorableKeyPath guards SaveKeyFile / LoadOrCreatePortfolioKey against
// persisting the secret to a location where it could be committed to git. It
// replaces the old lexical ".cf"-ancestor sniff (which was FALSE SAFETY — a ".cf"
// directory inside a repo that does not ignore it passed the check yet would be
// committed, and a symlink NAMED ".cf" defeated it entirely). Two orthogonal
// checks, per docs/design/nostr-identity-model.md §5:
//
//  1. Resolved-path-under-root: when allowedRoot != "", filepath.Clean(abs(path))
//     must equal or be under filepath.Clean(abs(allowedRoot)). One canonical
//     root — a foreign directory that merely shares a base name can no longer
//     pass, which closes both the foreign-repo leak and the symlink-name TOCTOU.
//
//  2. git-ignore defense-in-depth: if the path resolves inside a git work tree,
//     `git check-ignore` must confirm it is ignored; otherwise it would be
//     committed and the write is refused. A path OUTSIDE any git work tree (the
//     default ~/.config/rd case) cannot be committed, so the git check is skipped.
func requireIgnorableKeyPath(path, allowedRoot string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("nostr: resolve key path: %w", err)
	}
	abs = filepath.Clean(abs)

	if allowedRoot != "" {
		root, err := filepath.Abs(allowedRoot)
		if err != nil {
			return fmt.Errorf("nostr: resolve rd home: %w", err)
		}
		root = filepath.Clean(root)
		if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
			return fmt.Errorf("nostr: refusing to write key file to %q: it is not under the rd home %q", abs, root)
		}
	}

	insideRepo, ignored, err := pathGitIgnoreStatus(abs)
	if err != nil {
		return fmt.Errorf("nostr: checking git-ignore status of %q: %w", abs, err)
	}
	if insideRepo && !ignored {
		return fmt.Errorf("nostr: refusing to write key file to %q: it is inside a git work tree but not git-ignored, so it could be committed; add it to .gitignore or store the key under $RD_HOME outside the repository", abs)
	}
	return nil
}

// pathGitIgnoreStatus reports whether absPath resolves inside a git work tree
// and, if so, whether git considers it ignored. It shells out to git so the
// answer honors the FULL ignore stack (repo .gitignore, nested .gitignore,
// global excludes, .git/info/exclude) exactly as a commit would — matching what
// the design's "git check-ignore" defense-in-depth requires. absPath need not
// exist yet; check-ignore matches against ignore rules, not the filesystem.
func pathGitIgnoreStatus(absPath string) (insideRepo, ignored bool, err error) {
	dir := nearestExistingDir(filepath.Dir(absPath))
	if dir == "" {
		return false, false, nil
	}
	if _, lookErr := exec.LookPath("git"); lookErr != nil {
		// git absent: the under-root check (if any) already ran; we cannot run
		// the defense-in-depth pass, so treat as "not in a repo" rather than
		// hard-failing. The default $RD_HOME is outside any repo regardless.
		return false, false, nil
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return false, false, nil
	}
	// -q: quiet; exit 0 = ignored, exit 1 = NOT ignored, exit >1 = real error.
	runErr := exec.Command("git", "-C", dir, "check-ignore", "-q", "--", absPath).Run()
	if runErr == nil {
		return true, true, nil
	}
	var ee *exec.ExitError
	if errors.As(runErr, &ee) && ee.ExitCode() == 1 {
		return true, false, nil
	}
	return true, false, fmt.Errorf("git check-ignore failed: %w", runErr)
}

// nearestExistingDir walks up from dir until it finds an existing directory, so
// git commands can be run from a real cwd even when the key's parent dirs have
// not been created yet. Returns "" only if it walks off the filesystem root.
func nearestExistingDir(dir string) string {
	for {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// SaveKeyFile writes the key to path as JSON with 0600 permissions, creating
// parent directories as needed. The pubkey is written for human/tooling
// convenience but is always re-derived from the secret on load.
//
// allowedRoot is the canonical rd home ($RD_HOME); path must resolve under it
// and, if inside a git work tree, be git-ignored — see requireIgnorableKeyPath.
// This guards against a caller accidentally persisting the secret to a
// git-tracked location. Pass "" for allowedRoot to skip only the under-root
// check (the git-ignore defense-in-depth still runs).
func SaveKeyFile(path string, k *Key, allowedRoot string) error {
	if err := requireIgnorableKeyPath(path, allowedRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("nostr: mkdir for key file: %w", err)
	}
	kf := keyFile{Version: 1, SecretHex: k.SecretHex(), PubKeyHex: k.PubKeyHex()}
	data, err := json.MarshalIndent(kf, "", "  ")
	if err != nil {
		return fmt.Errorf("nostr: marshal key file: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("nostr: write key file: %w", err)
	}
	return nil
}

// LoadKeyFile reads and validates a key previously written by SaveKeyFile. The
// key is reconstructed from the secret; the on-disk pubkey is advisory only.
func LoadKeyFile(path string) (*Key, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("nostr: read key file: %w", err)
	}
	var kf keyFile
	if err := json.Unmarshal(data, &kf); err != nil {
		return nil, fmt.Errorf("nostr: parse key file: %w", err)
	}
	return KeyFromHex(kf.SecretHex)
}

// LoadOrCreatePortfolioKey loads the portfolio nostr key from path, or generates
// and persists a new one if the file does not exist. The nostr secp256k1 key is a
// distinct file (default "nostr-identity.json") under the rd home ($RD_HOME /
// RDHome()), independent of campfire's ed25519 identity. Callers that must
// preserve an existing identity across a home relocation should migrate the key
// forward with WriteKeyFileExclusive BEFORE calling this, since a missing file
// here triggers GenerateKey (a fresh, unrelated identity).
//
// Concurrency: the naive "os.Stat then SaveKeyFile" sequence has a TOCTOU
// race — two concurrent first-time callers can both observe the file missing,
// both generate a *different* key, and the second SaveKeyFile silently
// overwrites the first, leaving the two callers holding mismatched identities.
// To close that race, the create path uses os.O_CREATE|os.O_EXCL: exactly one
// caller (per process, and across processes on POSIX filesystems since
// O_EXCL is atomic at the OS level) wins the exclusive create and writes its
// generated key; every other concurrent caller gets EEXIST and instead reads
// back whatever the winner wrote, so all callers converge on one identical
// key and an existing key is never overwritten.
func LoadOrCreatePortfolioKey(path, allowedRoot string) (*Key, error) {
	if err := requireIgnorableKeyPath(path, allowedRoot); err != nil {
		return nil, err
	}

	// Fast path: the key already exists (the common case after first use).
	if k, err := loadKeyFileIfExists(path); err != nil {
		return nil, err
	} else if k != nil {
		return k, nil
	}

	k, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("nostr: mkdir for key file: %w", err)
	}
	kf := keyFile{Version: 1, SecretHex: k.SecretHex(), PubKeyHex: k.PubKeyHex()}
	data, err := json.MarshalIndent(kf, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("nostr: marshal key file: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			// Another concurrent caller won the race and created the file
			// first (or is in the middle of writing it). Converge on
			// whatever it wrote instead of overwriting it.
			return waitForKeyFile(path)
		}
		return nil, fmt.Errorf("nostr: create key file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return nil, fmt.Errorf("nostr: write key file: %w", err)
	}
	return k, nil
}

// loadKeyFileIfExists returns (nil, nil) when path does not exist, so callers
// can distinguish "not created yet" from a real read/parse error.
func loadKeyFileIfExists(path string) (*Key, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("nostr: stat key file: %w", err)
	}
	k, err := LoadKeyFile(path)
	if err != nil {
		return nil, err
	}
	return k, nil
}

// waitForKeyFile is used by the loser of the O_CREATE|O_EXCL race: the
// winner's file is guaranteed to exist but may not be fully written yet
// (create and write are two separate syscalls), so retry the load briefly
// instead of racing on a partial file.
func waitForKeyFile(path string) (*Key, error) {
	const (
		attempts = 100
		delay    = 2 * time.Millisecond
	)
	var lastErr error
	for i := 0; i < attempts; i++ {
		k, err := LoadKeyFile(path)
		if err == nil {
			return k, nil
		}
		lastErr = err
		time.Sleep(delay)
	}
	return nil, fmt.Errorf("nostr: timed out waiting for concurrently-created key file %q: %w", path, lastErr)
}

// DefaultKeyPath returns the conventional location of the portfolio nostr key
// given a home directory (the rd home, $RD_HOME / RDHome()). Kept separate from
// campfire's identity.json so it composes with existing resolution instead of
// overloading it. This is the OWNER actor's key path — see ActorKeyPath.
func DefaultKeyPath(home string) string {
	return filepath.Join(home, "nostr-identity.json")
}

// OwnerActor is the default durable actor id (selected when $RD_ACTOR is unset).
// It resolves to the LEGACY single-key path (DefaultKeyPath, "nostr-identity.json"),
// so an existing single-key install's key IS the owner key with ZERO migration —
// the owner is the human trust root / 30301 board author (design §2).
const OwnerActor = "owner"

// SanitizeActorID maps an actor id (e.g. "agent:pm") to a safe single filename
// component. ONLY [A-Za-z0-9_-] survive; every other rune — ':', '/', '\\', '.',
// whitespace, control chars — becomes '-'. Because '.' becomes '-' and separators
// become '-', the result can contain neither ".." nor a path separator, so it can
// never traverse out of the keys/ directory (the design's "no path traversal"
// requirement). It returns an error when actor is empty or sanitizes to nothing
// usable (all-invalid runes): silently falling back to some default actor would
// mis-attribute writes to the wrong key, which is exactly what per-actor keys
// exist to prevent. "agent:pm" -> "agent-pm" (matches design §8 BP-4).
func SanitizeActorID(actor string) (string, error) {
	if actor == "" {
		return "", errors.New("nostr: empty actor id")
	}
	out := make([]rune, 0, len(actor))
	for _, r := range actor {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '-':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	s := string(out)
	usable := false
	for _, r := range s {
		if r != '-' {
			usable = true
			break
		}
	}
	if !usable {
		return "", fmt.Errorf("nostr: actor id %q sanitizes to no usable filename", actor)
	}
	return s, nil
}

// ActorKeyPath returns the on-disk signing-key path for a durable actor under the
// rd home. Keys are per DURABLE actor (owner + named agents), NEVER per-process
// (design §2). The OWNER actor (or an empty id) maps to the LEGACY single-key path
// (DefaultKeyPath) so an existing install needs zero migration; every other named
// agent gets its own file at keys/<sanitized-actor>.json, a DISTINCT key with a
// DISTINCT pubkey. The actor id is sanitized against path traversal (see
// SanitizeActorID), so a hostile $RD_ACTOR cannot escape the keys/ directory.
func ActorKeyPath(home, actor string) (string, error) {
	if actor == "" || actor == OwnerActor {
		return DefaultKeyPath(home), nil
	}
	safe, err := SanitizeActorID(actor)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "keys", safe+".json"), nil
}

// WriteKeyFileExclusive writes k to path using O_CREATE|O_EXCL (0600) and is the
// identity-preserving migration primitive: it NEVER regenerates and NEVER
// overwrites. If the file already exists it returns nil without touching it, so
// concurrent first-run migrations converge on the winner's copy exactly like
// LoadOrCreatePortfolioKey. Unlike SaveKeyFile (which truncates), this can never
// clobber an existing identity — the property the never-regenerate migration
// depends on. The same anti-commit guard (requireIgnorableKeyPath) applies.
func WriteKeyFileExclusive(path string, k *Key, allowedRoot string) error {
	if err := requireIgnorableKeyPath(path, allowedRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("nostr: mkdir for key file: %w", err)
	}
	kf := keyFile{Version: 1, SecretHex: k.SecretHex(), PubKeyHex: k.PubKeyHex()}
	data, err := json.MarshalIndent(kf, "", "  ")
	if err != nil {
		return fmt.Errorf("nostr: marshal key file: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			// Another concurrent migration already wrote it; converge silently.
			return nil
		}
		return fmt.Errorf("nostr: create key file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("nostr: write key file: %w", err)
	}
	return nil
}

// StoredPubKeyHex returns the pubkey_hex field recorded in the key file at path.
// LoadKeyFile deliberately re-derives the key from the secret and ignores this
// field, so comparing the stored value against the derived PubKeyHex() is a cheap
// self-consistency tripwire: a mismatch means the file was tampered with or the
// secret was swapped without rewriting the pubkey (a botched/regenerated
// identity). Returns "" (no error) when the field is absent.
func StoredPubKeyHex(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("nostr: read key file: %w", err)
	}
	var kf keyFile
	if err := json.Unmarshal(data, &kf); err != nil {
		return "", fmt.Errorf("nostr: parse key file: %w", err)
	}
	return kf.PubKeyHex, nil
}
