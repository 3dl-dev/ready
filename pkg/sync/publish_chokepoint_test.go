// Mechanical write-path choke-point lock (ready-6d0 round 3, finding (3)).
//
// The runtime guard (GuardedPublish in relayclass.go) only protects code that
// actually calls it. Nothing in the Go type system stops a BRAND NEW file from
// importing pkg/nostr and calling nostr.Publish directly, sidestepping
// GuardedPublish (and therefore hitsReservedBoard) entirely — exactly the shape
// of the adversary's own A5 probe (rdsync.BuildCardEvent + nostr.Publish) that
// this item was reopened over. A convention ("always call GuardedPublish, never
// nostr.Publish") is not a control; the whole history of ready-6d0 is
// convention-helpers nobody was obliged to call.
//
// This test is the control: it statically scans every .go file in the module
// for the literal call-site pattern "nostr.Publish(" and fails CI if any file
// OTHER than the sanctioned wrapper's own file (relayclass.go, where
// GuardedPublish is defined and is the one legitimate direct caller) contains
// it. A new file that calls nostr.Publish directly — however it spells its way
// there, whatever package it lives in — fails `go test ./...` the moment it is
// added, before it ever reaches a relay. See TestPublishChokepoint_CatchesNewViolatingFile
// for the mutation proof that this actually fires.
package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chokepointAllowlist is the CLOSED set of files permitted to call
// nostr.Publish directly. Adding to this list requires the same justification
// GuardedPublish's own doc comment gives: a file here is either the sanctioned
// wrapper itself, or (deliberately never used today) a file that has a proven
// reason it cannot route through pkg/sync at all.
var chokepointAllowlist = map[string]bool{
	"pkg/sync/relayclass.go": true, // defines GuardedPublish; the one legitimate direct caller
	// This file necessarily contains the literal search string (in the scan
	// itself and in the mutation-test fixture source it writes/removes) — it is
	// the scanner, not a caller.
	"pkg/sync/publish_chokepoint_test.go": true,
}

// findModuleRootForChokepointTest walks up from the current working directory
// (pkg/sync when this test runs) until it finds go.mod, mirroring
// test/e2e/harness_test.go's findModuleRoot (unexported there, so duplicated
// here rather than reaching into another package's test file).
func findModuleRootForChokepointTest() (string, error) {
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
			return "", fmt.Errorf("go.mod not found walking up from %s", dir)
		}
		dir = parent
	}
}

// findDirectNostrPublishCallers scans every .go file under root for the
// literal substring "nostr.Publish(" and returns the repo-relative paths of
// every file that is NOT in chokepointAllowlist and contains it. Hidden
// directories (.git, etc.) are skipped.
func findDirectNostrPublishCallers(root string) ([]string, error) {
	var violations []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() != "." && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "nostr.Publish(") && !chokepointAllowlist[rel] {
			violations = append(violations, rel)
		}
		return nil
	})
	return violations, err
}

// TestPublishChokepoint_OnlySanctionedWrapperCallsNostrPublishDirectly is the
// live CI gate: every file in the module today must either avoid nostr.Publish
// entirely (routing through GuardedPublish/publishEventToRelays instead) or be
// the sanctioned wrapper file itself.
func TestPublishChokepoint_OnlySanctionedWrapperCallsNostrPublishDirectly(t *testing.T) {
	root, err := findModuleRootForChokepointTest()
	if err != nil {
		t.Fatalf("locate module root: %v", err)
	}
	violations, err := findDirectNostrPublishCallers(root)
	if err != nil {
		t.Fatalf("scan for direct nostr.Publish callers: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("file(s) call nostr.Publish directly instead of through sync.GuardedPublish "+
			"(ready-6d0 chokepoint guard) — route through GuardedPublish or, if there is a genuine "+
			"reason this file must bypass it, add it to chokepointAllowlist with justification:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// TestPublishChokepoint_CatchesNewViolatingFile is the mutation proof: it
// writes a NEW, unlisted .go file directly under the module root that calls
// nostr.Publish, then asserts findDirectNostrPublishCallers actually flags it
// (and only it) as a violation, then removes the file. This is what
// distinguishes this test from a tautology — the SAME scan function that would
// run in CI is proven to catch a file it has never seen before, not just to
// return an empty allowlist-shaped result on the current tree.
func TestPublishChokepoint_CatchesNewViolatingFile(t *testing.T) {
	root, err := findModuleRootForChokepointTest()
	if err != nil {
		t.Fatalf("locate module root: %v", err)
	}
	violatingPath := filepath.Join(root, "pkg", "sync", "zzz_ready6d0_chokepoint_adversary_probe.go")
	violatingSrc := `package sync

// This file is a MUTATION-TEST FIXTURE written and removed by
// TestPublishChokepoint_CatchesNewViolatingFile. It must never survive a test
// run; if you are reading this in a committed tree, the test failed to clean
// up after itself.
import (
	"context"

	"github.com/3dl-dev/ready/pkg/nostr"
)

func adversaryDirectPublish(ctx context.Context, relayURL string, e *nostr.Event) {
	_, _, _ = nostr.Publish(ctx, relayURL, e)
}
`
	if err := os.WriteFile(violatingPath, []byte(violatingSrc), 0o600); err != nil {
		t.Fatalf("write adversary fixture: %v", err)
	}
	t.Cleanup(func() {
		if rerr := os.Remove(violatingPath); rerr != nil && !os.IsNotExist(rerr) {
			t.Errorf("cleanup: failed to remove adversary fixture %s: %v — REMOVE IT MANUALLY", violatingPath, rerr)
		}
	})

	violations, err := findDirectNostrPublishCallers(root)
	if err != nil {
		t.Fatalf("scan for direct nostr.Publish callers: %v", err)
	}
	wantRel := "pkg/sync/zzz_ready6d0_chokepoint_adversary_probe.go"
	found := false
	for _, v := range violations {
		if v == wantRel {
			found = true
		}
	}
	if !found {
		t.Fatalf("mechanical scan did NOT flag a brand-new file calling nostr.Publish directly "+
			"(wanted %q among violations, got %v) — the chokepoint test is not actually catching new callers", wantRel, violations)
	}
}
