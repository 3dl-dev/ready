package foldvectors_test

// citations_test.go is ready-cee's done-condition test: every `(cmd|pkg)/**.go:N`
// or `:N-M` citation in docs/design/board-fold-spec.md must name a file that
// exists and a line (or line range) inside that file (TestSpecCitationsResolve).
// That check alone only catches a DELETE that shrinks a file below a cited line
// — it does NOT catch a MOVE: inserting/deleting lines earlier in a cited file
// shifts every later citation onto the wrong line while every citation stays
// "in bounds" and the check stays green.
//
// TestNamedCitationsAnchorToRealDeclarations closes the MOVE gap wherever the
// doc gives a citation something concrete to anchor to: a `` `FuncName` `` naming
// a REAL top-level Go func, cited close enough to a `path:line` citation that
// the func is plausibly what's being cited. For those, the check requires the
// cited range to fall inside that func's OWN current [declLine, nextFuncDeclLine)
// span — which breaks the instant the file's line numbers shift, because the
// func's real span moves but the cited line number, fixed in prose, does not.
// This covers every top-level func in pkg/sync/nostrproject.go, nostrwire.go,
// and pkg/views/views.go that the doc happens to name-and-cite this way (see
// citationExceptions for the documented cases where the named func is genuinely
// mentioned/called at a DIFFERENT location than its own declaration — those are
// real, correct citations this span check cannot itself validate and are called
// out by name instead of silently accepted).
//
// This is NOT total MOVE coverage: only ~155-210 of the doc's ~712 citations are
// "named" this way (a bare `path:line` with no adjacent identifier, e.g. citing a
// struct field or a raw tag name, has nothing for this check to anchor to and is
// still only bounds-checked). cmd/rd/nostrwrite.go is the one file with total,
// zero-orphan named coverage (TestSection262CitesEveryNostrwriteFunc).
//
// The doc uses a bare `:N` shorthand in two places (§3 and §26.2), each
// explicitly declaring "a bare `:N` means <path>" for a bounded scope. A
// naive "nearest preceding full citation" resolver gets §26.2 wrong: the
// citation immediately before its bare-`:N` run is `cmd/rd/dep.go:65` (§26.1's
// table), not `cmd/rd/nostrwrite.go` — the shorthand declaration overrides
// that, and this test honors the declaration, not proximity.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// shorthandWindow is a span of the raw spec text over which a declared "bare
// `:N` means <path>" convention is in force, overriding whatever file was
// most recently cited in full.
type shorthandWindow struct {
	start, end int
	path       string
}

// citationOcc is one `path:N[-M]` or bare `:N[-M]` occurrence found in the
// spec, in document order.
type citationOcc struct {
	pos       int    // byte offset of the match, for shorthand-window lookup
	pathOrNil string // "" for a bare citation
	line      int
	endLine   int
	raw       string // matched text, for error messages
	funcName  string // non-empty if immediately preceded by `funcName` (
}

var (
	fullCiteRe  = regexp.MustCompile("`((?:cmd|pkg)/[\\w./-]+\\.go):(\\d+)(?:-(\\d+))?`")
	bareCiteRe  = regexp.MustCompile("`:(\\d+)(?:-(\\d+))?`")
	shorthandRe = regexp.MustCompile("Citation shorthand for this (section|clause)[^:]*: a bare `:N` means\\s*`([\\w./-]+\\.go):N`")
	sectionHdrs = regexp.MustCompile(`\n## `)
	clauseHdrs  = regexp.MustCompile(`\n\*\*§`)
	// precedingID matches a backtick-quoted identifier that names the citation
	// immediately following it, allowing up to 50 chars of connective prose
	// ("is the single-identity wrapper via", "then publishes", etc.) between the
	// identifier and the opening paren — but NO other backtick in that gap, so a
	// closer identifier (or the previous citation's own backticks) always wins
	// over one further back. `\s*\(` (zero-gap) alone missed most of the doc's
	// "`Name` is/does X (`path:line`)" phrasing and only caught direct
	// "`Name` (`path:line`)" citations.
	precedingID = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]*)`[^`]{0,50}\\($")
)

func extractShorthandWindows(raw string) []shorthandWindow {
	var windows []shorthandWindow
	for _, m := range shorthandRe.FindAllStringSubmatchIndex(raw, -1) {
		scope := raw[m[2]:m[3]]
		path := raw[m[4]:m[5]]
		start := m[1] // end of the declaration sentence
		end := len(raw)
		if scope == "section" {
			if idx := firstAfter(sectionHdrs.FindAllStringIndex(raw, -1), start); idx >= 0 {
				end = idx
			}
		} else { // "clause": bounded by the next clause header OR section header
			candidates := []int{}
			if idx := firstAfter(clauseHdrs.FindAllStringIndex(raw, -1), start); idx >= 0 {
				candidates = append(candidates, idx)
			}
			if idx := firstAfter(sectionHdrs.FindAllStringIndex(raw, -1), start); idx >= 0 {
				candidates = append(candidates, idx)
			}
			for _, c := range candidates {
				if c < end {
					end = c
				}
			}
		}
		windows = append(windows, shorthandWindow{start: start, end: end, path: path})
	}
	return windows
}

func firstAfter(matches [][]int, pos int) int {
	for _, m := range matches {
		if m[0] > pos {
			return m[0]
		}
	}
	return -1
}

func windowFor(windows []shorthandWindow, pos int) (string, bool) {
	for _, w := range windows {
		if pos >= w.start && pos < w.end {
			return w.path, true
		}
	}
	return "", false
}

// extractCitations walks the raw spec text in document order, resolving every
// bare `:N`/`:N-M` against either an active shorthand window or (failing
// that) the nearest preceding full citation.
func extractCitations(t *testing.T, raw string) []citationOcc {
	t.Helper()
	windows := extractShorthandWindows(raw)
	if len(windows) != 2 {
		t.Fatalf("expected 2 declared bare-`:N` shorthand windows (§3, §26.2), found %d — spec shorthand text changed, fix this test", len(windows))
	}

	type rawMatch struct {
		pos      int
		end      int
		full     bool
		path     string
		line     int
		endLine  int
		matchStr string
	}
	var all []rawMatch
	for _, m := range fullCiteRe.FindAllStringSubmatchIndex(raw, -1) {
		line, _ := strconv.Atoi(raw[m[4]:m[5]])
		endLine := line
		if m[6] >= 0 {
			endLine, _ = strconv.Atoi(raw[m[6]:m[7]])
		}
		all = append(all, rawMatch{pos: m[0], end: m[1], full: true, path: raw[m[2]:m[3]], line: line, endLine: endLine, matchStr: raw[m[0]:m[1]]})
	}
	for _, m := range bareCiteRe.FindAllStringSubmatchIndex(raw, -1) {
		line, _ := strconv.Atoi(raw[m[2]:m[3]])
		endLine := line
		if m[4] >= 0 {
			endLine, _ = strconv.Atoi(raw[m[4]:m[5]])
		}
		all = append(all, rawMatch{pos: m[0], end: m[1], full: false, line: line, endLine: endLine, matchStr: raw[m[0]:m[1]]})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].pos < all[j].pos })

	var out []citationOcc
	lastFull := ""
	for _, m := range all {
		occ := citationOcc{pos: m.pos, line: m.line, endLine: m.endLine, raw: m.matchStr}
		if m.full {
			lastFull = m.path
			occ.pathOrNil = m.path
		} else {
			if p, ok := windowFor(windows, m.pos); ok {
				occ.pathOrNil = p
			} else if lastFull != "" {
				occ.pathOrNil = lastFull
			} else {
				t.Errorf("bare citation %q at byte offset %d has no antecedent file (no prior full citation, no shorthand window)", m.matchStr, m.pos)
				continue
			}
		}
		// Look back a short window for a `funcName` ( immediately before this
		// citation's opening backtick, to support content-level verification.
		// 110 comfortably covers the longest identifier (~35 chars) plus
		// precedingID's 50-char connective-prose allowance plus backticks/parens.
		lookback := m.pos - 110
		if lookback < 0 {
			lookback = 0
		}
		if fm := precedingID.FindStringSubmatch(raw[lookback:m.pos]); fm != nil {
			occ.funcName = fm[1]
		}
		out = append(out, occ)
	}
	return out
}

// TestSpecCitationsResolve is ready-cee's done condition 1: every citation in
// board-fold-spec.md names a file that exists and a line/range inside it.
func TestSpecCitationsResolve(t *testing.T) {
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	citations := extractCitations(t, string(raw))
	const minCitations = 300
	if len(citations) < minCitations {
		t.Fatalf("only %d citations extracted from %s, expected at least %d — extractor or spec regressed", len(citations), specPath, minCitations)
	}
	t.Logf("resolved %d citations in %s", len(citations), specPath)

	lineCounts := map[string]int{}
	checked := 0
	for _, c := range citations {
		if c.pathOrNil == "" {
			continue // already reported by extractCitations
		}
		total, ok := lineCounts[c.pathOrNil]
		if !ok {
			fullPath := filepath.Join(repoRootFromFoldvectors, c.pathOrNil)
			b, err := os.ReadFile(fullPath)
			if err != nil {
				t.Errorf("citation %q -> %s: file does not exist (%v)", c.raw, c.pathOrNil, err)
				lineCounts[c.pathOrNil] = -1
				continue
			}
			total = countLines(string(b))
			lineCounts[c.pathOrNil] = total
		}
		if total < 0 {
			continue // already reported missing file
		}
		checked++
		if c.line < 1 || c.line > total {
			t.Errorf("citation %q -> %s:%d is out of range (file has %d lines)", c.raw, c.pathOrNil, c.line, total)
		}
		if c.endLine < c.line {
			t.Errorf("citation %q -> %s:%d-%d has end before start", c.raw, c.pathOrNil, c.line, c.endLine)
		} else if c.endLine > total {
			t.Errorf("citation %q -> %s:%d-%d end is out of range (file has %d lines)", c.raw, c.pathOrNil, c.line, c.endLine, total)
		}
	}
	if checked < minCitations {
		t.Fatalf("only checked %d citations against real files, expected at least %d", checked, minCitations)
	}
}

// countLines counts lines the way an editor would report them: a trailing
// newline does not create an extra blank final line.
func countLines(content string) int {
	if content == "" {
		return 0
	}
	n := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") {
		n++
	}
	return n
}

const repoRootFromFoldvectors = "../.."

// TestSection262CitesEveryNostrwriteFunc is ready-cee's done condition 4:
// §26.2's bare-`:N` citations name real functions in cmd/rd/nostrwrite.go at
// their real declaration lines, and every top-level func in that file is
// covered — no orphan, no rotted line number.
func TestSection262CitesEveryNostrwriteFunc(t *testing.T) {
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	citations := extractCitations(t, string(raw))

	windows := extractShorthandWindows(string(raw))
	var section262 *shorthandWindow
	for i := range windows {
		if windows[i].path == "cmd/rd/nostrwrite.go" {
			section262 = &windows[i]
		}
	}
	if section262 == nil {
		t.Fatalf("could not locate §26.2's cmd/rd/nostrwrite.go shorthand window — spec structure changed, fix this test")
	}

	actual, err := funcLines(filepath.Join(repoRootFromFoldvectors, "cmd/rd/nostrwrite.go"))
	if err != nil {
		t.Fatalf("scan cmd/rd/nostrwrite.go: %v", err)
	}
	if len(actual) < 20 {
		t.Fatalf("only found %d top-level funcs in cmd/rd/nostrwrite.go, expected at least 20 — func-scan regex broke", len(actual))
	}

	// Only citations inside the §26.2 clause window count toward the
	// "no-orphans" promise — the doc cites cmd/rd/nostrwrite.go by name
	// elsewhere (field names like WaitingType, GateMsgID) with no claim that
	// those are func declarations.
	cited := map[string]int{}
	for _, c := range citations {
		if c.pathOrNil != "cmd/rd/nostrwrite.go" || c.funcName == "" {
			continue
		}
		if c.pos < section262.start || c.pos >= section262.end {
			continue
		}
		cited[c.funcName] = c.line
	}

	for name, wantLine := range actual {
		gotLine, ok := cited[name]
		if !ok {
			t.Errorf("cmd/rd/nostrwrite.go func %q (line %d) is not cited by name anywhere in %s — §26.2 promises no orphans", name, wantLine, specPath)
			continue
		}
		if gotLine != wantLine {
			t.Errorf("func %q: spec cites line %d, actual declaration is at line %d — citation has rotted", name, gotLine, wantLine)
		}
	}
	for name := range cited {
		if _, ok := actual[name]; !ok {
			t.Errorf("spec cites func %q in cmd/rd/nostrwrite.go, but no such top-level func exists", name)
		}
	}
	// This claim ("all cited, none rotted") is exactly what the two loops above
	// just checked — only print it if they found nothing to fail on. Printing it
	// unconditionally previously meant a failing run logged "all cited by name"
	// in the same output as the errors that disproved it.
	if !t.Failed() {
		t.Logf("cmd/rd/nostrwrite.go: %d top-level funcs, all cited by name in %s", len(actual), specPath)
	}
}

var funcDeclRe = regexp.MustCompile(`^func (?:\([^)]*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)\(`)

// funcLines returns top-level func name -> 1-based declaration line for a Go
// source file, matching `grep -n '^func '` semantics.
func funcLines(path string) (map[string]int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for i, line := range strings.Split(string(b), "\n") {
		if m := funcDeclRe.FindStringSubmatch(line); m != nil {
			out[m[1]] = i + 1
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no top-level funcs found in %s", path)
	}
	return out, nil
}

// funcSpan is a top-level func's real line range in its source file, computed
// as [declLine, nextTopLevelDeclLine) — i.e. up to (not including) the next
// top-level func's declaration line, or EOF for the last func in the file.
//
// Deliberately does NOT extend backward over the func's doc comment: doing so
// gives every documented func N lines of free slack (N = comment length), which
// silently absorbs exactly the kind of small, uniform line-shift (e.g. one line
// inserted earlier in the file) this check exists to catch. A citation that
// means "this is the doc comment above the function", not the function's own
// body, is called out explicitly in citationExceptions instead.
type funcSpan struct{ start, end int }

func funcSpans(path string) (map[string]funcSpan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(b), "\n")
	type nameAtLine struct {
		name string
		line int
	}
	var decls []nameAtLine
	for i, line := range lines {
		if m := funcDeclRe.FindStringSubmatch(line); m != nil {
			decls = append(decls, nameAtLine{m[1], i + 1})
		}
	}
	sort.Slice(decls, func(i, j int) bool { return decls[i].line < decls[j].line })
	out := make(map[string]funcSpan, len(decls))
	for i, d := range decls {
		end := len(lines) + 1
		if i+1 < len(decls) {
			end = decls[i+1].line
		}
		out[d.name] = funcSpan{start: d.line, end: end}
	}
	return out, nil
}

// citationExceptions lists every (funcName, file, citedStartLine) where a named
// citation legitimately points OUTSIDE that func's own declared span — because
// the clause is citing where the func is CALLED or documented from, not the
// func's own body — verified by hand against the source at each key below.
// TestNamedCitationsAnchorToRealDeclarations fails loudly (via the unused-entry
// check at the end of that test) if a listed exception stops being hit, so this
// list cannot silently accumulate stale entries.
var citationExceptions = map[string]string{
	"newerThan|pkg/sync/nostrproject.go|256": "§4.5: newerThan is USED for board " +
		"latest-wins ordering at nostrproject.go:256-258; it's declared at :552",
	"identitySet|pkg/views/views.go|113": "§13.7: identitySet is CALLED inside " +
		"DelegatedFilter's 3-line body (:113-115); identitySet itself is declared at :164",
	"parseTimestampValue|pkg/state/state.go|324": "§14.6: cites the enumeration " +
		"`parseTimestamp` / `parseTimestampValue` (:324-354) spanning BOTH funcs' " +
		"declarations; parseTimestampValue itself is declared at :339",
	"publishItemFullCreateNostr|cmd/rd/nostrwrite.go|547": "§20.6: CALL site inside " +
		"runCreateNostr; publishItemFullCreateNostr is declared at :155",
	"publishItemFullCreateNostr|cmd/rd/nostrwrite.go|622": "§21.x: second CALL site, " +
		"inside runEngageNostr; publishItemFullCreateNostr is declared at :155",
	"applyDepAndGateStatus|pkg/sync/nostrproject.go|435": "§9.9: CALL site inside " +
		"ProjectItems; applyDepAndGateStatus is declared at :452",
	"applyDepAndGateStatus|pkg/sync/nostrproject.go|439": "§14.4: cites " +
		"applyDepAndGateStatus's OWN 13-line doc comment (:439-451), not its body; " +
		"the func itself is declared at :452",
}

// TestNamedCitationsAnchorToRealDeclarations is ready-cee's done condition 2
// (extended per the blocking-defect review): every citation in
// board-fold-spec.md that names a real top-level Go func — anywhere in the
// doc, not only inside §26.2's window — must have its cited range fall inside
// that func's actual current span, unless the citation is a documented,
// by-hand-verified exception (citationExceptions). This is what makes
// inserting/deleting a line earlier in a heavily-cited file (nostrproject.go,
// nostrwire.go, views.go — the files with no §26.2-style structured coverage)
// a CI failure instead of a silent rot: the func's real span moves, the cited
// line doesn't, and containment breaks.
func TestNamedCitationsAnchorToRealDeclarations(t *testing.T) {
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	citations := extractCitations(t, string(raw))

	spansByFile := map[string]map[string]funcSpan{}
	usedExceptions := map[string]bool{}
	named := 0
	anchored := 0
	for _, c := range citations {
		if c.funcName == "" {
			continue
		}
		named++
		spans, ok := spansByFile[c.pathOrNil]
		if !ok {
			spans, _ = funcSpans(filepath.Join(repoRootFromFoldvectors, c.pathOrNil))
			spansByFile[c.pathOrNil] = spans
		}
		sp, isFunc := spans[c.funcName]
		if !isFunc {
			continue // not a top-level func in this file — nothing to anchor to
		}
		anchored++
		inSpan := c.line >= sp.start && c.endLine < sp.end
		if inSpan {
			continue
		}
		key := fmt.Sprintf("%s|%s|%d", c.funcName, c.pathOrNil, c.line)
		if _, ok := citationExceptions[key]; ok {
			usedExceptions[key] = true
			continue
		}
		t.Errorf("citation %q names func %q, whose real span in %s is [%d,%d), "+
			"but the citation covers lines %d-%d — citation has rotted or is a new "+
			"cross-reference that needs a citationExceptions entry",
			c.raw, c.funcName, c.pathOrNil, sp.start, sp.end, c.line, c.endLine)
	}

	const minAnchored = 100
	if anchored < minAnchored {
		t.Fatalf("only %d named citations resolved to a real top-level func, expected at least %d — extraction regressed", anchored, minAnchored)
	}
	for key, reason := range citationExceptions {
		if !usedExceptions[key] {
			t.Errorf("citationExceptions entry %q (%s) was never hit — the citation it documents changed or vanished; remove or update this entry", key, reason)
		}
	}
	t.Logf("checked %d named citations across %d files, %d resolved to a real top-level func (%d via documented exception)", named, len(spansByFile), anchored, len(usedExceptions))
}
