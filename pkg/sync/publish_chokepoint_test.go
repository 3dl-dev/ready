// Mechanical write-path choke-point lock (ready-6d0 round 3 / ready-69e round 4,
// finding (3)).
//
// The runtime guard (nostr.PublishGuard, installed in relayclass.go's init;
// GuardedPublish here is the sanctioned wrapper) protects EVERY call to
// pkg/nostr.Publish regardless of call spelling — that is the actual class fix
// (ready-69e). This test is a SECOND, independent layer: a static scan that
// flags a direct caller of pkg/nostr's Publish before the runtime guard would
// even run, so a violation shows up as a named file in `go test ./...` output
// instead of only as a refused-at-runtime error discovered later.
//
// ready-69e replaced the original substring scan (strings.Contains(data,
// "nostr.Publish(")) with this go/parser + go/ast pass because the substring
// form had a proven false negative AND a proven false positive:
//   - FALSE NEGATIVE: an aliased import (`nz "…/pkg/nostr"` then `nz.Publish(`)
//     or a function value (`pub := nostr.Publish; pub(...)`) never produces the
//     literal text "nostr.Publish(" anywhere in the file, so the substring scan
//     missed both — proven on 8637c3f by scripts/adv6d0-newcaller/main.go.
//   - FALSE POSITIVE: any file whose COMMENTS merely quote the literal string
//     (this very file, or pkg/nostr/client.go's doc comment on PublishGuard)
//     was flagged despite containing no call at all.
//
// The AST pass fixes both: it resolves each file's import of
// github.com/3dl-dev/ready/pkg/nostr to its actual LOCAL BINDING (the import's
// alias if any, "nostr" if unaliased, or the dot-import case) and only inspects
// real syntax nodes (ast.SelectorExpr / ast.Ident in expression position), never
// raw file bytes — so a comment can never match, and an alias/dot-import/
// function-value reference is resolved via the same binding a compiler would
// use, not via source text.
package sync

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nostrImportPath is the fully-qualified import path this scan resolves
// bindings for. Matched against ast.ImportSpec.Path.Value (which still
// includes the surrounding quotes from source, hence the trim below).
const nostrImportPath = "github.com/3dl-dev/ready/pkg/nostr"

// chokepointAllowlist is the CLOSED set of files permitted to reference
// pkg/nostr's Publish directly (by any binding). Adding to this list requires
// the same justification GuardedPublish's own doc comment gives: a file here
// is either the sanctioned wrapper itself, or (deliberately never used today)
// a file that has a proven reason it cannot route through pkg/sync at all.
var chokepointAllowlist = map[string]bool{
	"pkg/sync/relayclass.go": true, // defines GuardedPublish; the one legitimate direct caller
	// This file's own mutation-test fixtures reference Publish by every
	// spelling under test (alias/dot-import/function value); it is the
	// scanner and its test harness, not a production caller.
	"pkg/sync/publish_chokepoint_test.go": true,
	// Deliberately calls pkg/nostr.Publish directly, bypassing GuardedPublish,
	// to prove the nostr.PublishGuard runtime hook itself refuses a
	// reserved-coordinate event even when GuardedPublish's own explicit check
	// is skipped entirely (ready-69e class fix) — see its own doc comment.
	"pkg/sync/publish_guard_hook_test.go": true,
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

// fileReferencesNostrPublish parses one Go source file and reports whether it
// contains a REAL syntactic reference to pkg/nostr's Publish — a call, a
// function value, or any other expression use — resolved through that file's
// own import of github.com/3dl-dev/ready/pkg/nostr, under whatever local
// binding that file gave it (its alias, the default "nostr", or "." for a
// dot-import). A file that does not import pkg/nostr at all cannot reference
// Publish through it and is skipped without further inspection.
func fileReferencesNostrPublish(path string) (bool, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}

	// Resolve this file's local binding(s) for the nostr import. A file may in
	// principle import it more than once only via distinct aliases (Go allows
	// at most one unaliased import of a given path), so collect all of them.
	var (
		localNames []string // named/aliased bindings — look for <name>.Publish
		dotImport  bool     // "." import — Publish is referenced bare
	)
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if path != nostrImportPath {
			continue
		}
		switch {
		case imp.Name == nil:
			localNames = append(localNames, "nostr") // default package name
		case imp.Name.Name == "_":
			// Blank import: the package's init() still runs (irrelevant here —
			// GuardedPublish's own init lives in this package, not caller
			// files) but no identifier is bound, so Publish cannot be
			// referenced through it at all.
		case imp.Name.Name == ".":
			dotImport = true
		default:
			localNames = append(localNames, imp.Name.Name)
		}
	}
	if len(localNames) == 0 && !dotImport {
		return false, nil
	}

	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		switch expr := n.(type) {
		case *ast.SelectorExpr:
			if expr.Sel == nil || expr.Sel.Name != "Publish" {
				return true
			}
			if id, ok := expr.X.(*ast.Ident); ok {
				for _, name := range localNames {
					if id.Name == name {
						found = true
						return false
					}
				}
			}
		case *ast.Ident:
			// Only meaningful for a dot-import: "Publish" bound bare into file
			// scope. Skip the identifier when it is itself the Sel of a
			// SelectorExpr (already handled above, and would double-count a
			// qualified reference like foo.Publish on an unrelated package) or
			// when it's a declaration site (func/param/field name), which
			// ast.Inspect also visits but which is not a USE of the import.
			if dotImport && expr.Name == "Publish" {
				found = true
				return false
			}
		}
		return true
	})
	return found, nil
}

// findDirectNostrPublishCallers walks every .go file under root and returns
// the repo-relative paths of every file that is NOT in chokepointAllowlist and
// syntactically references pkg/nostr's Publish through its own import of that
// package (see fileReferencesNostrPublish). Hidden directories (.git, etc.)
// are skipped. Files that fail to parse are reported as scan errors, not
// silently skipped — a file this scan cannot understand is not proof of
// safety.
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
		if chokepointAllowlist[rel] {
			return nil
		}
		hit, ferr := fileReferencesNostrPublish(path)
		if ferr != nil {
			return ferr
		}
		if hit {
			violations = append(violations, rel)
		}
		return nil
	})
	return violations, err
}

// TestPublishChokepoint_OnlySanctionedWrapperCallsNostrPublishDirectly is the
// live CI gate: every file in the module today must either avoid pkg/nostr's
// Publish entirely (routing through GuardedPublish/publishEventToRelays
// instead) or be the sanctioned wrapper file itself.
func TestPublishChokepoint_OnlySanctionedWrapperCallsNostrPublishDirectly(t *testing.T) {
	root, err := findModuleRootForChokepointTest()
	if err != nil {
		t.Fatalf("locate module root: %v", err)
	}
	violations, err := findDirectNostrPublishCallers(root)
	if err != nil {
		t.Fatalf("scan for direct nostr.Publish references: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("file(s) reference pkg/nostr's Publish directly instead of through sync.GuardedPublish "+
			"(ready-6d0/ready-69e chokepoint guard) — route through GuardedPublish or, if there is a genuine "+
			"reason this file must bypass it, add it to chokepointAllowlist with justification:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// writeChokepointFixture writes a mutation-test fixture file under
// pkg/sync/, registers its cleanup, and returns its repo-relative path (for
// asserting it shows up in the scan's violations).
func writeChokepointFixture(t *testing.T, root, filename, src string) string {
	t.Helper()
	fullPath := filepath.Join(root, "pkg", "sync", filename)
	if err := os.WriteFile(fullPath, []byte(src), 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", filename, err)
	}
	t.Cleanup(func() {
		if rerr := os.Remove(fullPath); rerr != nil && !os.IsNotExist(rerr) {
			t.Errorf("cleanup: failed to remove fixture %s: %v — REMOVE IT MANUALLY", fullPath, rerr)
		}
	})
	return "pkg/sync/" + filename
}

// assertFlagged runs the real scan and fails unless wantRel is among the
// violations it returns — the same scan function TestPublishChokepoint_
// OnlySanctionedWrapperCallsNostrPublishDirectly runs in CI.
func assertFlagged(t *testing.T, root, wantRel string) {
	t.Helper()
	violations, err := findDirectNostrPublishCallers(root)
	if err != nil {
		t.Fatalf("scan for direct nostr.Publish references: %v", err)
	}
	for _, v := range violations {
		if v == wantRel {
			return
		}
	}
	t.Fatalf("semantic scan did NOT flag %q (got violations %v) — the chokepoint test is not actually catching this caller shape", wantRel, violations)
}

// TestPublishChokepoint_CatchesNewViolatingFile is the baseline mutation
// proof: an unaliased, non-dot-import direct call — the original ready-6d0
// round-3 fixture shape — must still be caught by the rewritten scan.
func TestPublishChokepoint_CatchesNewViolatingFile(t *testing.T) {
	root, err := findModuleRootForChokepointTest()
	if err != nil {
		t.Fatalf("locate module root: %v", err)
	}
	wantRel := writeChokepointFixture(t, root, "zzz_ready6d0_chokepoint_adversary_probe.go", `package sync

// MUTATION-TEST FIXTURE (TestPublishChokepoint_CatchesNewViolatingFile). Must
// never survive a test run.
import (
	"context"

	"github.com/3dl-dev/ready/pkg/nostr"
)

func adversaryDirectPublish(ctx context.Context, relayURL string, e *nostr.Event) {
	_, _, _ = nostr.Publish(ctx, relayURL, e)
}
`)
	assertFlagged(t, root, wantRel)
}

// TestPublishChokepoint_CatchesAliasedImportCall proves the fix for the
// ready-69e FALSE NEGATIVE: an aliased import produces no "nostr.Publish("
// text anywhere in the file, yet must still be flagged because the scan
// resolves the alias to the import path, not to source text.
func TestPublishChokepoint_CatchesAliasedImportCall(t *testing.T) {
	root, err := findModuleRootForChokepointTest()
	if err != nil {
		t.Fatalf("locate module root: %v", err)
	}
	wantRel := writeChokepointFixture(t, root, "zzz_ready69e_alias_adversary_probe.go", `package sync

// MUTATION-TEST FIXTURE (TestPublishChokepoint_CatchesAliasedImportCall). Must
// never survive a test run.
import (
	"context"

	nz "github.com/3dl-dev/ready/pkg/nostr"
)

func adversaryAliasedPublish(ctx context.Context, relayURL string, e *nz.Event) {
	_, _, _ = nz.Publish(ctx, relayURL, e)
}
`)
	assertFlagged(t, root, wantRel)
}

// TestPublishChokepoint_CatchesFunctionValueCall proves the second ready-69e
// FALSE NEGATIVE shape: Publish taken as a function value, never followed by
// an open paren at the reference site (the call happens through the local
// variable instead), so a substring/regex scan for "nostr.Publish(" cannot see
// it even without an alias.
func TestPublishChokepoint_CatchesFunctionValueCall(t *testing.T) {
	root, err := findModuleRootForChokepointTest()
	if err != nil {
		t.Fatalf("locate module root: %v", err)
	}
	wantRel := writeChokepointFixture(t, root, "zzz_ready69e_funcvalue_adversary_probe.go", `package sync

// MUTATION-TEST FIXTURE (TestPublishChokepoint_CatchesFunctionValueCall). Must
// never survive a test run.
import (
	"context"

	"github.com/3dl-dev/ready/pkg/nostr"
)

func adversaryFuncValuePublish(ctx context.Context, relayURL string, e *nostr.Event) {
	pub := nostr.Publish
	_, _, _ = pub(ctx, relayURL, e)
}
`)
	assertFlagged(t, root, wantRel)
}

// TestPublishChokepoint_CatchesDotImportCall proves the third ready-69e
// caller shape: a dot-import brings Publish into file scope as a bare
// identifier with no qualifier at all — "nostr." never appears in the file.
func TestPublishChokepoint_CatchesDotImportCall(t *testing.T) {
	root, err := findModuleRootForChokepointTest()
	if err != nil {
		t.Fatalf("locate module root: %v", err)
	}
	wantRel := writeChokepointFixture(t, root, "zzz_ready69e_dotimport_adversary_probe.go", `package sync

// MUTATION-TEST FIXTURE (TestPublishChokepoint_CatchesDotImportCall). Must
// never survive a test run.
import (
	"context"

	. "github.com/3dl-dev/ready/pkg/nostr"
)

func adversaryDotImportPublish(ctx context.Context, relayURL string, e *Event) {
	_, _, _ = Publish(ctx, relayURL, e)
}
`)
	assertFlagged(t, root, wantRel)
}

// TestPublishChokepoint_CommentOnlyMentionIsNotFlagged proves the ready-69e
// FALSE POSITIVE is fixed: a file that merely QUOTES the string
// "nostr.Publish(" in a comment or string literal, without importing
// pkg/nostr at all, must not be flagged. The old substring scan flagged this
// exact shape (the round-3 implementer's own probe comment, and this file's
// own doc comment above, both tripped it).
func TestPublishChokepoint_CommentOnlyMentionIsNotFlagged(t *testing.T) {
	root, err := findModuleRootForChokepointTest()
	if err != nil {
		t.Fatalf("locate module root: %v", err)
	}
	fixtureRel := writeChokepointFixture(t, root, "zzz_ready69e_comment_only_probe.go", `package sync

// This comment mentions the literal string "nostr.Publish(" purely as
// documentation prose, e.g. describing the old substring scan's false
// positive (ready-69e). It does not import pkg/nostr and calls nothing.
const commentOnlyMarker = "nostr.Publish( appears here only as a string literal too"
`)
	violations, err := findDirectNostrPublishCallers(root)
	if err != nil {
		t.Fatalf("scan for direct nostr.Publish references: %v", err)
	}
	for _, v := range violations {
		if v == fixtureRel {
			t.Fatalf("semantic scan flagged %q, a file with only a comment/string mention and no pkg/nostr import — false positive not fixed", fixtureRel)
		}
	}
}
