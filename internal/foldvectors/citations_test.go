package foldvectors_test

// citations_test.go is ready-cee's done-condition test: every `(cmd|pkg)/**.go:N`
// or `:N-M` citation in docs/design/board-fold-spec.md must name a file that
// exists and a line (or line range) inside that file. Without this, moving or
// deleting cited code silently rots up to ~350 citations with no CI signal
// (ready-1bf verified them once with a scratchpad script that nothing re-runs).
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
	precedingID = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]*)`\\s*\\($")
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
		lookback := m.pos - 60
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
	t.Logf("cmd/rd/nostrwrite.go: %d top-level funcs, all cited by name in %s", len(actual), specPath)
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
