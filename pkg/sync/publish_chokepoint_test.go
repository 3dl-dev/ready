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
//
// ready-fcf ROUTE 2: import-binding resolution has a blind spot of its own — a
// file living INSIDE pkg/nostr IS package nostr, so it can never import
// nostrImportPath (a self-import) and localNames/dotImport are always empty
// for it, regardless of what it calls. Such a file references Publish as a
// bare in-package identifier instead, and the scan now walks for exactly that
// shape when rel is under pkg/nostr (see fileReferencesNostrPublish).
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

// nostrPackageDir is pkg/nostr's own repo-relative directory. A file living
// there IS package nostr, so it can never import nostrImportPath (that would
// be a self-import) and therefore can never show up in localNames/dotImport
// below — it references Publish/PublishMany as a bare in-package identifier
// instead. This is ready-fcf ROUTE 2, and the reason fileReferencesNostrPublish
// takes rel as well as path: the import-binding resolution above tells us
// nothing about a file that never imports pkg/nostr in the first place.
const nostrPackageDir = "pkg/nostr"

// guardedPublishNames is the set of pkg/nostr identifiers that reach the
// network write and must therefore only ever be referenced through this
// package's sanctioned wrappers. "Publish" is the original per-event write
// (GuardedPublish); "PublishMany" is the ready-260 batch write
// (GuardedPublishMany) that republishes a whole board over one connection. A
// new network-write primitive added to pkg/nostr MUST be added here too,
// otherwise the chokepoint is silently narrower than its own doc claims — the
// scan matches on identifier NAME, so an unlisted name is invisible to it.
var guardedPublishNames = map[string]bool{
	"Publish":     true,
	"PublishMany": true,
}

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
	// ready-fcf ROUTE 2 allowlist: files INSIDE pkg/nostr that legitimately
	// reference Publish/PublishMany as bare in-package identifiers. Each entry
	// below is either the sanctioned definition itself, or a caller with a
	// proven reason it must reference Publish directly and cannot route
	// through sync.GuardedPublish (that would be pkg/nostr importing pkg/sync,
	// an import cycle).
	//
	// client.go DEFINES Publish; publishmany.go DEFINES PublishMany. Neither
	// is a "caller" in the sense this scan polices — flagging a function's own
	// declaration would be nonsensical — but the in-package bare-identifier
	// scan (fileReferencesNostrPublish) cannot tell "func Publish(...) {...}"
	// apart from a same-named local var/decl without doing so, so they're
	// allowlisted explicitly rather than taught a declaration-vs-use
	// distinction the rest of this file doesn't need.
	"pkg/nostr/client.go":      true,
	"pkg/nostr/publishmany.go": true,
	// live_relay_test.go and negentropy_live_relay_test.go call bare Publish
	// against a LIVE relay under RD_NOSTR_LIVE_RELAY=1 (kind 1 / kind 30078,
	// no board coordinate — no exposure today). They cannot route through
	// sync.GuardedPublish (pkg/nostr cannot import pkg/sync). Unlike a random
	// new file in pkg/nostr, they are now covered by pkg/nostr's OWN default
	// guard (publishguard.go, ready-fcf): PublishGuard is armed the instant
	// this package loads, in this package's own test binary, with no
	// production opt-in — so a reserved-coordinate event added to either file
	// would be refused pre-dial regardless of this static scan. Allowlisted
	// rather than migrated: routing a live-relay proof through sync would add
	// a pkg/sync -> pkg/nostr test-only dependency for no additional safety.
	"pkg/nostr/live_relay_test.go":            true,
	"pkg/nostr/negentropy_live_relay_test.go": true,
	// publishmany_test.go is PublishMany's OWN test suite (ready-260): it
	// necessarily calls PublishMany directly, in-package, against a local fake
	// relay (batchRelay, an httptest server) to test the primitive itself —
	// never a real network path, never a board coordinate. Same rationale as
	// this file allowlisting itself above: it is the primitive's test
	// harness, not a production caller trying to bypass GuardedPublishMany.
	"pkg/nostr/publishmany_test.go": true,
	// publishguard_test.go is the ready-fcf default-guard's OWN test harness:
	// it calls bare Publish in-package, deliberately, to prove
	// defaultReservedBoardGuard refuses a reserved coordinate pre-dial (and
	// passes a non-reserved one through) with no other package's init
	// involved. Same rationale as publish_guard_hook_test.go allowlisting
	// itself in pkg/sync above.
	"pkg/nostr/publishguard_test.go": true,
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
// function value, or any other expression use. rel is path's repo-relative
// slash-form location (as computed by the caller's filepath.Rel), needed
// because a file INSIDE pkg/nostr itself (ready-fcf ROUTE 2) is reached
// differently than everywhere else — see below.
//
// EVERYWHERE ELSE, the reference is resolved through that file's own import
// of github.com/3dl-dev/ready/pkg/nostr, under whatever local binding that
// file gave it (its alias, the default "nostr", or "." for a dot-import). A
// file that does not import pkg/nostr at all cannot reference Publish through
// it and would ordinarily be skipped without further inspection.
//
// INSIDE pkg/nostr, that import resolution can never fire — a file there IS
// package nostr and cannot import itself — yet it can still reference Publish
// as a bare in-package identifier (ready-fcf: this was the actual gap; such a
// file used to sail through this scan with localNames/dotImport both empty
// and return false without being inspected at all). So for rel under
// nostrPackageDir, this function ALSO walks for a bare identifier named
// Publish/PublishMany used as a value, regardless of imports.
func fileReferencesNostrPublish(path, rel string) (bool, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}

	inNostrPackage := filepath.ToSlash(filepath.Dir(rel)) == nostrPackageDir

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
	if len(localNames) == 0 && !dotImport && !inNostrPackage {
		return false, nil
	}

	// The in-package shape needs its own walk (below): it has no import
	// binding to key off, and — unlike the dot-import case — it must skip
	// Publish/PublishMany's OWN func-declaration identifiers (this package
	// defines them; declaring them is not a USE of them) rather than every
	// bare Ident named Publish.
	if inNostrPackage {
		v := &inPackageIdentUseVisitor{names: guardedPublishNames}
		ast.Walk(v, f)
		return v.found, nil
	}

	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		switch expr := n.(type) {
		case *ast.SelectorExpr:
			if expr.Sel == nil || !guardedPublishNames[expr.Sel.Name] {
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
			if dotImport && guardedPublishNames[expr.Name] {
				found = true
				return false
			}
		}
		return true
	})
	return found, nil
}

// inPackageIdentUseVisitor walks a file KNOWN to be inside pkg/nostr looking
// for a bare identifier in names (Publish/PublishMany) used as a value —
// exactly the shape a file inside the package itself would use to call its
// own Publish/PublishMany without any import at all (ready-fcf ROUTE 2).
//
// A plain ast.Inspect over *ast.Ident would also match the func declarations
// THEMSELVES (client.go's "func Publish(...)", publishmany.go's "func
// PublishMany(...)") — those are handled by allowlisting the two defining
// files instead of teaching this visitor to tell "declaration" from "use" in
// general, since those are the only in-package declarations the guarded names
// have today. This visitor still exists (rather than reusing the dot-import
// branch above) because it also needs to skip a SelectorExpr's Sel field —
// e.g. some unrelated type's own "Publish" method or field — which the
// dot-import branch never had to worry about (a dot-imported name can't also
// be a package-level declaration in the same file; that would be a
// compile-time redeclaration).
type inPackageIdentUseVisitor struct {
	names map[string]bool
	found bool
}

func (v *inPackageIdentUseVisitor) Visit(n ast.Node) ast.Visitor {
	if v.found || n == nil {
		return nil
	}
	switch node := n.(type) {
	case *ast.SelectorExpr:
		// Walk only the receiver expression; node.Sel is a field/method NAME,
		// not a reference to the package-level Publish/PublishMany, and
		// counting it would false-positive on any unrelated x.Publish.
		ast.Walk(v, node.X)
		return nil
	case *ast.Ident:
		if v.names[node.Name] {
			v.found = true
		}
		return nil
	}
	return v
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
		hit, ferr := fileReferencesNostrPublish(path, rel)
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

// writeChokepointFixtureInDir writes a mutation-test fixture file under
// dir (repo-relative, slash form, e.g. "pkg/sync" or "pkg/nostr"), registers
// its cleanup, and returns its repo-relative path (for asserting it shows up
// in the scan's violations).
func writeChokepointFixtureInDir(t *testing.T, root, dir, filename, src string) string {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(dir), filename)
	if err := os.WriteFile(fullPath, []byte(src), 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", filename, err)
	}
	t.Cleanup(func() {
		if rerr := os.Remove(fullPath); rerr != nil && !os.IsNotExist(rerr) {
			t.Errorf("cleanup: failed to remove fixture %s: %v — REMOVE IT MANUALLY", fullPath, rerr)
		}
	})
	return dir + "/" + filename
}

// writeChokepointFixture writes a mutation-test fixture file under pkg/sync/
// — the shape every ready-6d0/ready-69e fixture in this file uses. See
// writeChokepointFixtureInDir for the ready-fcf pkg/nostr shape.
func writeChokepointFixture(t *testing.T, root, filename, src string) string {
	return writeChokepointFixtureInDir(t, root, "pkg/sync", filename, src)
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

// TestPublishChokepoint_CatchesBatchPublishManyCall locks the ready-260 batch
// write primitive into the SAME chokepoint as the per-event one. PublishMany is
// a second way to reach the network write, so a file calling it directly would
// bypass sync.GuardedPublishMany's reserved-board check exactly as a direct
// nostr.Publish call bypasses GuardedPublish's — and, before guardedPublishNames
// existed, the scan matched the literal identifier "Publish" and could not see
// it at all.
func TestPublishChokepoint_CatchesBatchPublishManyCall(t *testing.T) {
	root, err := findModuleRootForChokepointTest()
	if err != nil {
		t.Fatalf("locate module root: %v", err)
	}
	wantRel := writeChokepointFixture(t, root, "zzz_ready260_publishmany_adversary_probe.go", `package sync

// MUTATION-TEST FIXTURE (TestPublishChokepoint_CatchesBatchPublishManyCall).
// Must never survive a test run.
import (
	"context"

	"github.com/3dl-dev/ready/pkg/nostr"
)

func adversaryDirectPublishMany(ctx context.Context, relayURL string, evs []*nostr.Event) {
	_, _ = nostr.PublishMany(ctx, relayURL, evs)
}
`)
	assertFlagged(t, root, wantRel)
}

// TestPublishChokepoint_CatchesFileInsidePkgNostr is the ready-fcf ROUTE 2
// mutation proof: a brand-new file living INSIDE pkg/nostr itself, calling
// Publish as a bare in-package identifier (no import at all — it can't import
// its own package), must be caught. Before this fix,
// fileReferencesNostrPublish only resolved bindings for files that IMPORT
// pkg/nostr, so localNames/dotImport were both empty here and the scan
// returned false WITHOUT INSPECTING ANYTHING — this exact fixture shape
// reached the live transport in the ready-fcf finding while go build/vet/test
// ./... stayed green.
func TestPublishChokepoint_CatchesFileInsidePkgNostr(t *testing.T) {
	root, err := findModuleRootForChokepointTest()
	if err != nil {
		t.Fatalf("locate module root: %v", err)
	}
	wantRel := writeChokepointFixtureInDir(t, root, "pkg/nostr", "zzz_readyfcf_route2_adversary_probe.go", `package nostr

// MUTATION-TEST FIXTURE (TestPublishChokepoint_CatchesFileInsidePkgNostr).
// Must never survive a test run. Calls Publish as a bare in-package
// identifier — no import of pkg/nostr, because this file IS pkg/nostr.
import "context"

func adversaryRoute2Publish(ctx context.Context, relayURL string, e *Event) {
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
