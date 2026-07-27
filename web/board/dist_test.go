package board

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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
// The scan also rejects any bare "//" substring in .html/.js/.css content
// (not just attribute values — see assertSameOriginAttrs below for those),
// so a protocol-relative reference stuffed into a <style> @import or a JS
// string literal (e.g. a fetch() call) is caught even though it never
// appears as a src=/href= attribute.
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
		if idx := strings.Index(content, "http://"); idx != -1 {
			t.Errorf("%s: absolute http:// URL found: ...%s...", path, snippet(content, idx))
		}
		if idx := strings.Index(content, "https://"); idx != -1 {
			t.Errorf("%s: absolute https:// URL found: ...%s...", path, snippet(content, idx))
		}
		if idx := strings.Index(content, "//"); idx != -1 {
			t.Errorf("%s: protocol-relative reference found: ...%s...", path, snippet(content, idx))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk dist: %v", err)
	}

	assertSameOriginAttrs(t, filepath.Join(dist, "index.html"))
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
//     it: `` `build:${STAMP}` `` (tsc/esbuild preserve template-literal
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
	if _, err := exec.LookPath("npm"); err != nil {
		t.Fatalf("npm not found on PATH — required to build the board dist under test: %v", err)
	}
	if _, statErr := os.Stat("node_modules"); statErr != nil {
		cmd := exec.Command("npm", "ci")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("npm ci: %v\n%s", err, out)
		}
	}
	cmd := exec.Command("npm", "run", "build")
	cmd.Env = append(os.Environ(), "VITE_BUILD_STAMP="+buildStampForTest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("npm run build: %v\n%s", err, out)
	}
	return "dist"
}
