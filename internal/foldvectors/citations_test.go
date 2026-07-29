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
// a REAL top-level Go func, found by nearestNamedFunc (see its doc for the
// clause-boundary + closest-real-func-in-source algorithm — NOT a fixed-width
// regex lookback over raw prose punctuation, which is what left most true
// declaration-line citations unanchored before this round; see the coverage
// numbers below). For those, the check requires the citation's start line to
// EXACTLY EQUAL that func's OWN current declaration line (and its end line to
// stay inside the func's [declLine, nextFuncDeclLine) span), unless the
// citation is a documented, by-hand-verified citationExceptions entry (a
// genuine call-site or doc-comment cross-reference, a closest-match
// ambiguity, or a detail citation into the func's body — see that map's
// doc). Exact equality — not containment — is what makes BOTH an insertion
// AND a deletion earlier in a heavily-cited file a CI failure instead of a
// silent rot: containment alone caught insertions (which push the func's
// declaration line PAST the frozen citation) but not deletions (which pull
// it back TOWARD the citation and satisfy `citedLine >= declLine` no matter
// how much was deleted, surviving on `citedEnd < declEnd` tail slack alone).
// PROVEN for pkg/views/views.go and pkg/sync/nostrwire.go: inserting one line
// at views.go:180 (rotting FocusFilter/LabelFilter/Apply/AllNames) and at
// nostrwire.go:570 (rotting ItemDriftScope/GrantDriftScope/itemIDForEvent),
// and separately deleting the blank line above `AllNames` in views.go
// (shifting its declaration from :225 to :224 while the doc still cites
// :225) — all four go red under this test and green again once reverted.
//
// See the inSpan comment in TestNamedCitationsAnchorToRealDeclarations for
// why detail citations (a doc reference to a specific line inside a func's
// body, not its declaration line) can't satisfy equality by construction and
// are instead required to be documented citationExceptions entries — which,
// via the @decl-keyed exception format, still get move detection, just by
// human-verified anchor instead of a line-count heuristic.
//
// Measured coverage as of this round: of the 216 citations in the doc whose
// start line lands EXACTLY on some real top-level func's declaration line
// (the ground truth for "this citation could be named-and-anchored"), 198
// (92%) are correctly bound to that exact func by nearestNamedFunc — up from
// 103 (48%), measured the same way against the same 216-citation ground
// truth, for the PRIOR lookback-regex extractor this round replaced (it
// required a literal `(` immediately before the citation's own backtick and
// so could never match the doc's dominant `(`Name`, path:line)` citation
// form at all — name and citation in separate backtick spans — nor
// `` `Name(args)` `` forms separated from their citation by other
// backtick-quoted text). Coverage is not, and is not claimed to be, 100%:
// nearestNamedFunc only names a citation when its own
// clause contains a backtick-quoted real func name within maxCandidateSkip
// candidates of the citation; a bare `path:line` with no such identifier
// nearby (e.g. citing a struct field or a raw tag name) has nothing to
// anchor to and is still only bounds-checked by TestSpecCitationsResolve.
// cmd/rd/nostrwrite.go is the one file with total, zero-orphan named
// coverage, independent of nearestNamedFunc's heuristics, via §26.2's
// explicit bare-`:N` shorthand naming every func by hand
// (TestSection262CitesEveryNostrwriteFunc).
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
	funcName  string // non-empty if the citation's clause names a real top-level func (see nearestNamedFunc)
}

var (
	fullCiteRe  = regexp.MustCompile("`((?:cmd|pkg)/[\\w./-]+\\.go):(\\d+)(?:-(\\d+))?`")
	bareCiteRe  = regexp.MustCompile("`:(\\d+)(?:-(\\d+))?`")
	shorthandRe = regexp.MustCompile("Citation shorthand for this (section|clause)[^:]*: a bare `:N` means\\s*`([\\w./-]+\\.go):N`")
	sectionHdrs = regexp.MustCompile(`\n## `)
	clauseHdrs  = regexp.MustCompile(`\n\*\*§`)
	boldMarkRe  = regexp.MustCompile(`\*\*`)
	// candidateNameRe matches a backtick-quoted bare identifier, optionally
	// followed by a parenthesized argument list INSIDE the same backticks —
	// `Name` or `Name(args, more)` — and nothing else between the backticks.
	// An expression like `` `item.Gate == gateType` `` or an indexed lookup
	// like `` `idset[By]` `` contains a `.`, ` `, `=` or `[` that this pattern
	// cannot produce, so the whole backtick span fails to match and is never
	// mistaken for a function name.
	candidateNameRe = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]*)(?:\\([^`]*\\))?`")
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

// boldOpenPositions returns the byte offset of every markdown bold-OPEN
// marker ("**") in raw. It assumes "**" occurs in strictly alternating
// open/close pairs — every even-indexed match (0, 2, 4, ...) opens a bold
// run, every odd-indexed one closes the run just opened — and fails loudly
// if that parity doesn't hold, since nearestNamedFunc's clause boundaries
// depend on it. The spec consistently opens each function/section
// description with a bold label (`**`Name`**`, `**§13.11 `Name(...)`**`,
// `**Reason:**`, ...), so these positions double as clause boundaries: text
// between one bold-open and the next belongs to one described subject and is
// where that subject's name is expected to be mentioned.
func boldOpenPositions(t *testing.T, raw string) []int {
	t.Helper()
	all := boldMarkRe.FindAllStringIndex(raw, -1)
	if len(all)%2 != 0 {
		t.Fatalf("found %d `**` bold markers in %s — odd count means an unmatched marker; nearestNamedFunc's clause-boundary detection assumes strict open/close pairs and cannot proceed safely", len(all), specPath)
	}
	opens := make([]int, 0, len(all)/2+1)
	for i, m := range all {
		if i%2 == 0 {
			opens = append(opens, m[0])
		}
	}
	return opens
}

// nearestBoldOpen returns the byte offset of the closest bold-open marker at
// or before pos, or 0 if none precedes it.
func nearestBoldOpen(boldOpens []int, pos int) int {
	start := 0
	for _, b := range boldOpens {
		if b > pos {
			break
		}
		start = b
	}
	return start
}

// maxCandidateSkip bounds how many textually-closer, non-matching candidates
// nearestNamedFunc will look past before giving up on the fallback pass. See
// that function's doc for why an unbounded look-further-back search is
// unsafe in an enumeration clause.
const maxCandidateSkip = 1

// nearestNamedFunc looks backward from a citation at byte offset pos, within
// [clauseStart, pos), for the backtick-quoted identifier (candidateNameRe)
// that names it, validated against path's current source — funcSpans,
// loaded lazily and cached in spansCache.
//
// clauseStart bounds the search and must be the LATER (larger) of: (a) the
// nearest preceding bold-open (nearestBoldOpen / boldOpenPositions — text
// between one bold-open and the next belongs to one described subject), and
// (b) the end of the PREVIOUS citation occurrence, INCLUDING any name
// forwardNamedFunc consumed after it. (b) is required because many clauses
// are enumerations — "`resolveItemID` (`:300-312`), `clearOrSet` /
// `ClearSentinel`\n(`:1014-1023`), `appendUnique` (`:1026-1033`), ...
// `hasTag` (`:272-279`), the replay scratch types `replayState`
// (`:357-372`), ..." — one bold-open clause, many (name, citation) pairs.
// Without bound (b), `replayState`'s citation would search all the way back
// to the clause's bold-open and could walk past several non-func names to
// `hasTag` — a REAL func, but the wrong one, cited separately earlier in the
// same enumeration.
//
// Within that window, candidates are tried closest-to-the-citation first,
// capped at maxCandidateSkip non-matching candidates: this is what makes
// "**§13.12 `LabelFilter(atom)`** — exact match of `atom` against a member of
// `Item.Labels` ... (`pkg/views/views.go:202-211`)" resolve to LabelFilter
// and not `atom` — `atom` is textually closer and well-formed, but names a
// parameter, not a top-level func, so it fails the funcSpans check and the
// search tries one candidate further back (LabelFilter, which passes). The
// cap is what stops a long enumeration or multi-sentence run-on — e.g. "...
// instead of `nostrNextCreatedAt` (§17.2). A republish ... `rd log put`
// additionally builds its `CardSpec` with no `BoardAuthor` (`:714-722`)" —
// from walking all the way back past `BoardAuthor`, `CardSpec` and
// `created_at` (none of them funcs) to seize on `nostrNextCreatedAt`, which
// has nothing to do with this citation.
//
// This closest-wins rule is deliberately NOT overridden by an exact-
// declaration-line check (preferring whichever candidate's own func starts
// exactly at the citation's line, regardless of position): that would fix
// "**§13.11 `FocusFilter(gateType)`** — `ReadyFilter` AND (...)
// (`pkg/views/views.go:185-196`)" (FocusFilter declared at :185, matching;
// ReadyFilter at :60, not) but it would ALSO wrongly override the verified
// "`DelegatedFilter` is the single-identity wrapper via `identitySet`\n
// (`:113-115`)" case, where DelegatedFilter's OWN declaration also happens
// to start at :113 (the cited line) yet the correct, hand-verified answer is
// identitySet (see citationExceptions) — the two cases are structurally
// identical (closer name is the true subject in one, a coincidental
// exact-line match in the other) and no syntactic rule distinguishes them.
// So closest-wins is the ONLY heuristic here, and the FocusFilter/ReadyFilter
// case is instead a documented citationExceptions entry
// ("ReadyFilter|pkg/views/views.go|185").
//
// Returns "" if no candidate in the window is a real top-level func in path
// (the citation is then only bounds-checked, not move-detected), or if
// path's source can't be read.
func nearestNamedFunc(raw string, clauseStart, pos int, path string, spansCache map[string]map[string]funcSpan) string {
	if path == "" || clauseStart >= pos {
		return ""
	}
	spans, cached := spansCache[path]
	if !cached {
		spans, _ = funcSpans(filepath.Join(repoRootFromFoldvectors, path))
		spansCache[path] = spans
	}
	if len(spans) == 0 {
		return ""
	}
	matches := candidateNameRe.FindAllStringSubmatch(raw[clauseStart:pos], -1)
	for i := len(matches) - 1; i >= 0 && i >= len(matches)-1-maxCandidateSkip; i-- {
		if _, ok := spans[matches[i][1]]; ok {
			return matches[i][1]
		}
	}
	return ""
}

// forwardNameRe matches the inverse citation order "(`:N[-M]`, `Name`)" —
// used when a citation is restated after already being introduced in full
// elsewhere. §25.4 recaps §3.4's `grantTrusts(levels, e.PubKey)` (`:235-237`;
// ...)` as "(`:235-237`, `grantTrusts`)" — the name AFTER its own citation
// instead of before. It matches only immediately after the citation's own
// closing backtick (anchored at the start of the search slice), so it can
// never reach into unrelated later prose.
var forwardNameRe = regexp.MustCompile("^,\\s*`([A-Za-z_][A-Za-z0-9_]*)`\\s*\\)")

// forwardNamedFunc is nearestNamedFunc's mirror for the "(`:N`, `Name`)"
// order: it looks immediately AFTER a citation ending at afterPos for a
// restated name, validated the same way against path's real top-level funcs.
// It exists because, without it, a name written after its citation is
// invisible to backward-only search and instead gets picked up (WRONGLY, as
// the nearest backward candidate) by whatever citation comes NEXT — which is
// exactly what happened to §25.4's `:246-248` before this fix: `grantTrusts`
// sat between it and the preceding `:235-237`, so backward search bound
// `grantTrusts` to `:246-248` even though `:235-237` is what it actually
// names. Returns the name and the byte offset just past the match (so the
// caller can advance its clause boundary past the consumed name) when found.
func forwardNamedFunc(raw string, afterPos int, path string, spansCache map[string]map[string]funcSpan) (name string, consumedEnd int, ok bool) {
	if path == "" {
		return "", afterPos, false
	}
	spans, cached := spansCache[path]
	if !cached {
		spans, _ = funcSpans(filepath.Join(repoRootFromFoldvectors, path))
		spansCache[path] = spans
	}
	limit := afterPos + 80
	if limit > len(raw) {
		limit = len(raw)
	}
	m := forwardNameRe.FindStringSubmatchIndex(raw[afterPos:limit])
	if m == nil {
		return "", afterPos, false
	}
	cand := raw[afterPos+m[2] : afterPos+m[3]]
	if _, isFunc := spans[cand]; !isFunc {
		return "", afterPos, false
	}
	return cand, afterPos + m[1], true
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
	boldOpens := boldOpenPositions(t, raw)

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

	spansCache := map[string]map[string]funcSpan{}
	var out []citationOcc
	lastFull := ""
	prevEnd := 0
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
				prevEnd = m.end
				continue
			}
		}
		clauseStart := nearestBoldOpen(boldOpens, m.pos)
		if prevEnd > clauseStart {
			clauseStart = prevEnd
		}
		occ.funcName = nearestNamedFunc(raw, clauseStart, m.pos, occ.pathOrNil, spansCache)
		consumedEnd := m.end
		if occ.funcName == "" {
			if name, end, ok := forwardNamedFunc(raw, m.end, occ.pathOrNil, spansCache); ok {
				occ.funcName = name
				consumedEnd = end
			}
		}
		prevEnd = consumedEnd
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

// citationExceptions lists every (funcName, file, citedStartLine, currentDeclLine)
// where a named citation's cited range legitimately falls outside that func's
// own current span — verified by hand against the source at each key below.
// Three distinct reasons land here, all requiring human judgment a line-count
// checker cannot supply on its own:
//
//   - Genuine cross-reference: the clause is citing where the func is
//     CALLED or documented from, not the func's own body (e.g. grantTrusts,
//     newerGrant, CardCoord, FocusFilter|...|47, boardConfidentialEnvelope
//     |...|82, applyDepAndGateStatus|...|435, publishItemFullCreateNostr,
//     identitySet), or an enumeration citation spans both a const/type
//     declaration and the func's own declaration in one range (clearOrSet,
//     ParseCrossCampfireRef, parseTimestampValue), or the citation documents
//     another (frozen) doc's now-stale line numbers rather than asserting
//     current ones (BuildCardEvent|...|237, BuildStatusEvent|...|319 —
//     §15.8 explicitly discusses drift in confidential-boards-envelope.md's
//     frozen citations; this spec file is not itself citing those lines as
//     current).
//   - nearestNamedFunc closest-wins ambiguity: within a single clause, a
//     textually CLOSER real-func name outranks the clause's actual subject
//     (ReadyFilter|...|185 — FocusFilter's own clause mentions its
//     AND-composed dependency `ReadyFilter` closer to the citation than
//     `FocusFilter` itself; see nearestNamedFunc's doc for why no syntactic
//     rule can prefer FocusFilter here without also breaking the verified
//     identitySet|...|113 case, which has the identical shape with the
//     opposite correct answer). sealStatusPayload|...|230 is the same
//     shape: the clause names the func but the citation is to a struct
//     (statusPayload) documenting its JSON shape, not the func's own body.
//   - Detail citation: the clause names a func (often at its own primary,
//     exactly-anchored citation elsewhere in the doc — e.g. runCloseNostr is
//     cited at its true declaration by §26.2's `:234`) and ALSO, in a
//     different clause, cites a narrower range a few lines INTO that same
//     func's body to point at one specific statement (a field write, a tag,
//     a branch) rather than the function as a whole. TestNamedCitationsAnchor
//     ToRealDeclarations requires EXACT equality between a citation's start
//     line and the func's current declaration line (see inSpan below), so
//     every one of these — verified correct against current source at the
//     time each was added — needs an entry here. This is the majority of
//     the list below (BuildCardEvent|...|291, BuildHistoricalStatusEvent,
//     BuildStatusEventWithIssueRoot,
//     CardSpecFromItem, DeriveBoardKeyring, OverdueFilter|...|95,
//     PendingFilter|...|86, PublishEventsUnique, PublishItemWithReason,
//     PublishStatusChange, applyDepAndGateStatus|...|453, boardConfidential
//     Envelope|...|138, encWellFormed, handleWorkCreate, itemFromCard,
//     itemIDForEvent, publishEngagedItemsNostr, publishEvents,
//     publishItemCardEditNostr, runApproveNostr, runCloseNostr,
//     runCreateNostr, runDepAddNostr, runUpdateNostr — 4 distinct detail
//     ranges). ReadyFilter|...|61 and |...|67 are the same shape but have NO
//     separate exact-declaration citation anywhere in the doc — ReadyFilter
//     is only ever cited via its per-conjunct detail lines.
//
// The key embeds BOTH the doc's cited line AND the func's declaration line
// AT THE TIME each entry was verified. This makes an entry survive a
// legitimate, unrelated doc edit (which changes neither number) but
// invalidate itself the moment the NAMED func's declaration moves in its
// source file (a code move) — the old key stops matching, the citation
// falls through to the normal error path, and the entry must be re-verified
// and re-keyed at the func's new declaration line. Keying on citedStartLine
// alone (the prior scheme) let a code move hide behind a still-valid-looking
// key forever, since a move changes the func's real span but never the
// number frozen in the doc.
//
// TestNamedCitationsAnchorToRealDeclarations fails loudly (via the
// unused-entry check at the end of that test) if a listed exception stops
// being hit, so this list cannot silently accumulate stale entries.
var citationExceptions = map[string]string{
	"refuseRedactedRepublish|cmd/rd/confidential_guard.go|28@decl26": "§16.9: cites the " +
		"guard's own doc comment block; refuseRedactedRepublish is declared at :26",
	"publishItemFullCreateNostr|cmd/rd/nostrwrite.go|156@decl155": "§16.9: the " +
		"refuseRedactedRepublish CALL site, the first statement of the body (:156); " +
		"publishItemFullCreateNostr is declared at :155",
	"publishItemStatusChangeNostr|cmd/rd/nostr.go|356@decl353": "§16.9: the " +
		"refuseRedactedRepublish CALL site inside the body (:356); " +
		"publishItemStatusChangeNostr is declared at :353",
	"publishItemCardEditNostr|cmd/rd/nostr.go|405@decl392": "§16.9: the " +
		"refuseRedactedRepublish CALL site inside the body (:405); " +
		"publishItemCardEditNostr is declared at :392",
	"DeriveBoardKeyring|pkg/sync/keydist.go|174@decl178": "§16.10: quotes " +
		"DeriveBoardKeyring's OWN doc comment (:174-177) — the scan-ALL-grants claim " +
		"the relay's addressable replacement contradicts; the func is declared at :178",
	"newerThan|pkg/sync/nostrproject.go|256@decl572": "§4.5: newerThan is USED for board " +
		"latest-wins ordering at nostrproject.go:256-258; it's declared at :572 (moved " +
		"from :552 by ready-f5f's deterministic-edge-order fix in applyDepAndGateStatus)",
	"identitySet|pkg/views/views.go|113@decl164": "§13.7: identitySet is CALLED inside " +
		"DelegatedFilter's 3-line body (:113-115); identitySet itself is declared at :164",
	"parseTimestampValue|pkg/state/state.go|340@decl355": "§14.6: cites the enumeration " +
		"`parseTimestamp` / `parseTimestampValue` (:324-354) spanning BOTH funcs' " +
		"declarations; parseTimestampValue itself is declared at :339",
	"publishItemFullCreateNostr|cmd/rd/nostrwrite.go|566@decl155": "§20.6: CALL site inside " +
		"runCreateNostr (moved from :553 to :566 by ready-ca3's parent-id " +
		"validation block); publishItemFullCreateNostr is declared at :155",
	"publishItemFullCreateNostr|cmd/rd/nostrwrite.go|641@decl155": "§21.x: second CALL site, " +
		"inside runEngageNostr (moved from :628 to :641 by ready-ca3); " +
		"publishItemFullCreateNostr is declared at :155",
	"applyDepAndGateStatus|pkg/sync/nostrproject.go|435@decl452": "§9.9: CALL site inside " +
		"ProjectItems; applyDepAndGateStatus is declared at :452",
	"applyDepAndGateStatus|pkg/sync/nostrproject.go|439@decl452": "§14.4: cites " +
		"applyDepAndGateStatus's OWN 13-line doc comment (:439-451), not its body; " +
		"the func itself is declared at :452",
	"grantTrusts|pkg/sync/nostrproject.go|235@decl135": "§3.4/§25.4: CALL site inside " +
		"the read-trust gate (both `grantTrusts(levels, e.PubKey)` (:235-237) and " +
		"§25.4's restatement); grantTrusts itself is declared at :135",
	"newerGrant|pkg/sync/rolegrant.go|525@decl643": "§4.4: CALL site — the ascending sort " +
		"is expressed as `newerGrant(grants[j], grants[i])` (:487-489); newerGrant itself " +
		"is declared at :605",
	"FocusFilter|pkg/views/views.go|47@decl185": "§13.2: CALL site inside Named's " +
		"ViewFocus case (`return FocusFilter(\"\")`, :47); FocusFilter itself is " +
		"declared at :185",
	"ReadyFilter|pkg/views/views.go|185@decl60": "§13.11: closest-wins ambiguity — " +
		"FocusFilter's own clause mentions its AND-composed dependency `ReadyFilter` " +
		"closer to the citation (`pkg/views/views.go:185-196`) than `FocusFilter` " +
		"itself; the citation is FocusFilter's own declaration+body, FocusFilter is " +
		"declared at :185, ReadyFilter at :60",
	"clearOrSet|pkg/state/state.go|1030@decl1034": "§14.6: cites the enumeration " +
		"`clearOrSet` / `ClearSentinel` (:1014-1023) spanning both the ClearSentinel " +
		"const's declaration (:1014) and clearOrSet's own body; clearOrSet itself is " +
		"declared at :1018",
	"ParseCrossCampfireRef|pkg/state/state.go|1077@decl1079": "§14.8: cites the enumeration " +
		"`ParseCrossCampfireRef` / `CrossCampfireRef` (:1061-1083) spanning both the " +
		"CrossCampfireRef type's declaration and ParseCrossCampfireRef's own body; " +
		"ParseCrossCampfireRef itself is declared at :1063",
	"BuildCardEvent|pkg/sync/nostrwire.go|237@decl254": "§15.8: documents the FROZEN " +
		"confidential-boards-envelope.md's now-stale citation for BuildCardEvent " +
		"(claimed :237-310 when frozen); BuildCardEvent's CURRENT declaration is :254 " +
		"(moved from :246, ready-a9b) — this entry records drift, not a live citation of :237",
	"BuildStatusEvent|pkg/sync/nostrwire.go|319@decl370": "§15.8: documents the FROZEN " +
		"confidential-boards-envelope.md's now-stale citation for BuildStatusEvent " +
		"(claimed :319-344 when frozen); BuildStatusEvent's CURRENT declaration is " +
		":370 (moved from :362, ready-a9b) — this entry records drift, not a live citation of :319",
	"sealStatusPayload|pkg/sync/envelope.go|253@decl331": "§18.5-area: names " +
		"sealStatusPayload but cites the `statusPayload` struct (:230-234) that " +
		"documents the JSON shape it marshals, not the func's own body; " +
		"sealStatusPayload itself is declared at :308",
	"boardConfidentialEnvelope|cmd/rd/confidential.go|83@decl85": "§27.1-area: cites " +
		"boardConfidentialEnvelope's OWN 6-line doc comment (:79-84, cited range :83-84), " +
		"not its body; the func itself is declared at :85",
	"CardCoord|pkg/sync/nostrwire.go|378@decl201": "§27.8: CALL site inside " +
		"BuildStatusEvent's tag-table build (`CardCoord(k.PubKeyHex(), itemID)`, :378, " +
		"moved from :370 by ready-a9b); CardCoord itself is declared at :201 " +
		"(moved from :196)",

	// Detail citations (see the third bucket above). Each names a func whose
	// OWN declaration is either cited exactly elsewhere in the doc, or (for
	// BuildStatusEventWithIssueRoot and ReadyFilter) never cited at its
	// declaration line at all — only via these narrower ranges.
	"BuildHistoricalStatusEvent|pkg/sync/nostrmigrate.go|61@decl49": "§19.7: the " +
		"`by` tag write (:61-63) inside BuildHistoricalStatusEvent's body; " +
		"BuildHistoricalStatusEvent is never cited at its own declaration line " +
		"(:49) — only via this detail range",
	"BuildCardEvent|pkg/sync/nostrwire.go|299@decl254": "§23.3: the three " +
		"label-emission-mode branch (:299-319, moved from :291-311 by ready-a9b) " +
		"inside BuildCardEvent's body; BuildCardEvent's own declaration+body is " +
		"cited exactly at :254 (§5.1, `pkg/sync/nostrwire.go:254-...`)",
	"BuildStatusEventWithIssueRoot|pkg/sync/nostrwire.go|451@decl424": "§11.5/§19.4: " +
		"the second `a` tag (board coordinate) construction (:451-454, moved from " +
		":443-446 by ready-a9b) inside the func's tag-table build; " +
		"BuildStatusEventWithIssueRoot is never cited at its own declaration line " +
		"(:424) — only via detail ranges like this one",
	"BuildStatusEventWithIssueRoot|pkg/sync/nostrwire.go|455@decl424": "§19.4/§24.1: " +
		"the re-sign-after-tag-mutation step (:455-462, moved from :447-454 by " +
		"ready-a9b); BuildStatusEventWithIssueRoot is never cited at its own " +
		"declaration line (:424) — only via detail ranges",
	"CardSpecFromItem|pkg/sync/nostrmigrate.go|110@decl106": "§27.x: the `s` tag " +
		"write inside CardSpecFromItem's body (:110); CardSpecFromItem's own " +
		"declaration+body is cited exactly at :106 (§5.6, `:106-127`)",
	"OverdueFilter|pkg/views/views.go|95@decl94": "§15.6: the `now := time.Now()` " +
		"construction-time capture (:95) inside OverdueFilter's body; OverdueFilter's " +
		"own declaration+body is cited exactly at :94 (§13.6, `:94-109`)",
	"PendingFilter|pkg/views/views.go|86@decl83": "§15.1 recap: a bare `:86` pointing " +
		"at PendingFilter's `scheduled` case; PendingFilter's own declaration+body is " +
		"cited exactly at :83 (§13.5, `:83-91`)",
	"PublishEventsUnique|pkg/sync/nostroutbound.go|544@decl543": "§16.8: the " +
		"`guardReservedBoard` call site one line into PublishEventsUnique's body " +
		"(:544); PublishEventsUnique itself is declared at :543",
	"PublishItemWithReason|pkg/sync/nostroutbound.go|185@decl179": "§18.x table: " +
		"a bare `:185` inside PublishItemWithReason's body (one of several detail " +
		"lines cited from the same clause); PublishItemWithReason's own " +
		"declaration+body is cited exactly at :179 (`:179-212`)",
	"PublishStatusChange|pkg/sync/nostroutbound.go|225@decl221": "§18.x table: " +
		"a bare `:225` inside PublishStatusChange's body; PublishStatusChange " +
		"itself is declared at :221",
	"ReadyFilter|pkg/views/views.go|61@decl60": "§13.3: the NOT-terminal conjunct " +
		"(:61-63), one line into ReadyFilter's body; ReadyFilter is never cited " +
		"at its own declaration line (:60) — only via its per-conjunct detail lines",
	"ReadyFilter|pkg/views/views.go|67@decl60": "§13.3/§15.1: the " +
		"not-`scheduled` conjunct (:67-69); ReadyFilter is never cited at its " +
		"own declaration line (:60) — only via its per-conjunct detail lines",
	"applyDepAndGateStatus|pkg/sync/nostrproject.go|453@decl452": "§8.1: the " +
		"BlockedBy drain-and-rebuild step (:453-460) inside applyDepAndGateStatus's " +
		"body; its declaration is at :452 (a separate call-site exception already " +
		"covers :435, a doc-comment exception already covers :439)",
	"boardConfidentialEnvelope|cmd/rd/confidential.go|156@decl85": "§11.10: the " +
		"epoch-1 bootstrap step (:139-159) inside boardConfidentialEnvelope's body; " +
		"its declaration is at :85 (a separate doc-comment exception already covers :83)",
	"encWellFormed|pkg/sync/envelope.go|74@decl73": "§25.2: the `enc != \"1\"` " +
		"check one line into encWellFormed's body (:74); encWellFormed's own " +
		"declaration+body is cited exactly at :73 (§11.2, `:73-85`)",
	"handleWorkCreate|pkg/state/state.go|581@decl572": "§15.x: a detail range " +
		"(:565-568) inside handleWorkCreate's body; handleWorkCreate's own " +
		"declaration is cited exactly at :556",
	"itemFromCard|pkg/sync/nostrproject.go|584@decl582": "§4.6: the nanosecond " +
		"multiplication step (:584-586) inside itemFromCard's body; itemFromCard's " +
		"own declaration+body is cited exactly at :582 (§5.1, `:582-648`, moved from " +
		"`:562-628` by ready-f5f's deterministic-edge-order fix in applyDepAndGateStatus)",
	"itemIDForEvent|pkg/sync/nostrwire.go|622@decl618": "§9.x: a detail range " +
		"(:597-608, moved from :589-600 by ready-a9b) inside itemIDForEvent's " +
		"body; itemIDForEvent's own declaration+body is cited exactly at :593 " +
		"(`:593-610`)",
	"publishEngagedItemsNostr|cmd/rd/nostrwrite.go|634@decl621": "§27.x: the " +
		"project-prefix assignment (:634, moved from :621 by ready-ca3) inside " +
		"publishEngagedItemsNostr's body; its declaration is cited exactly at " +
		":621 (§26.3, moved from :608)",
	"publishEvents|pkg/sync/nostroutbound.go|582@decl581": "§16.8: the " +
		"`guardReservedBoard` call site one line into publishEvents's body (:582); " +
		"publishEvents itself is declared at :581",
	"publishItemCardEditNostr|cmd/rd/nostr.go|412@decl392": "§18.10: the " +
		"`setCardEnvelope` call site inside publishItemCardEditNostr's body (:406); " +
		"its declaration+body is cited exactly at :389 (`:389-419`)",
	"runApproveNostr|cmd/rd/nostrwrite.go|307@decl302": "§22.2 recap: a detail " +
		"range (:304-309) inside runApproveNostr's body; its declaration+body is " +
		"cited exactly at :299 (`:299-321`, and by §26.2's bare `:299`)",
	"runCloseNostr|cmd/rd/nostrwrite.go|250@decl237": "§20.5 recap: the implicit " +
		"unblock call site (:247) inside runCloseNostr's body; its declaration is " +
		"cited exactly by §26.2's bare `:234`",
	"runCreateNostr|cmd/rd/nostrwrite.go|556@decl518": "§27.x: the `item.Project` " +
		"assignment (:556, moved from :543 by ready-ca3's parent-id validation " +
		"block) inside runCreateNostr's body; its declaration+body is cited " +
		"exactly at :518 (§18.8, `:518-570`, moved from :510-557)",
	"runDepAddNostr|cmd/rd/nostrwrite.go|354@decl353": "§21.1/§21.3 recap: the " +
		"cross-board/read-trust guard (:351-353) one line into runDepAddNostr's " +
		"body; its declaration is cited exactly by §26.2's bare `:350`",
	"runUpdateNostr|cmd/rd/nostrwrite.go|442@decl433": "§20.4/§24.7 recap: the " +
		"status-only-update detail range (:439-441); runUpdateNostr's declaration " +
		"is cited exactly by §26.2's bare `:433`",
	"runUpdateNostr|cmd/rd/nostrwrite.go|446@decl433": "§24.1 recap: the " +
		"field-rewrite block (:443-479, extended to include the ready-b878 " +
		"ParentID assignment and ready-ca3's parent-id validation, through :476); " +
		"runUpdateNostr's declaration is cited exactly by §26.2's bare `:433`",
	"runUpdateNostr|cmd/rd/nostrwrite.go|476@decl433": "§16.x/§24.1 recap: the " +
		"card-edit-publish detail line (:476, shifted +8 by ready-ca3's parent-id " +
		"validation block, previously :468 shifted +3 by ready-b878's ParentID " +
		"field block); runUpdateNostr's declaration is cited exactly by §26.2's " +
		"bare `:433`",
	"runUpdateNostr|cmd/rd/nostrwrite.go|481@decl433": "§20.4/§24.7 recap: the " +
		"`Status=<statusTo>` assignment block (:481-492, moved from :473-484 by " +
		"ready-ca3); runUpdateNostr's declaration is cited exactly by §26.2's " +
		"bare `:433`",
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
		// inSpan requires EXACT equality between the citation's start line
		// and the func's CURRENT declaration line — not containment. This is
		// what closes the move gap in BOTH directions:
		//
		// An INSERT anywhere earlier in the file raises sp.start past the
		// doc's frozen c.line, so c.line == sp.start fails immediately. A
		// DELETE anywhere earlier LOWERS sp.start below the frozen c.line,
		// which ALSO fails immediately under equality — this is the half of
		// the contract a plain `c.line >= sp.start` containment check missed
		// (a delete only ever raises sp.start's distance BELOW c.line, so
		// `>=` stayed satisfied no matter how much got deleted, and the
		// citation silently pointed at the wrong line). Both directions are
		// now caught for every citation whose start line names the func's
		// OWN declaration — proven by the insertion mutations in this file's
		// package doc comment and by a deletion mutation (removing the blank
		// line above `AllNames` in pkg/views/views.go, shifting its
		// declaration from :225 to :224 while the doc still says :225).
		//
		// For a citation that legitimately cites a line PARTWAY INTO a
		// func's body (e.g. a specific field write or conditional, not the
		// function's own declaration line) equality by construction does not
		// hold today, on an untampered tree, for a citation that was always
		// correct — there is no way to tell, from the frozen line number and
		// the current source alone, whether such a citation is "still
		// correctly offset" or "drifted by exactly the same amount its
		// offset used to be." Rather than accept that as unclosable, every
		// such citation is required to be a documented, by-hand-verified
		// citationExceptions entry (see its doc, "Detail citation" bucket).
		// Because the exception key embeds the func's declaration line AT
		// VERIFICATION TIME, a future move of that SAME func invalidates the
		// entry (the key stops matching) and the citation falls through to
		// this error path for re-verification — so even detail citations get
		// move detection, just via a human-checked anchor instead of a
		// line-count heuristic.
		inSpan := c.line == sp.start && c.endLine < sp.end
		if inSpan {
			continue
		}
		key := fmt.Sprintf("%s|%s|%d@decl%d", c.funcName, c.pathOrNil, c.line, sp.start)
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
