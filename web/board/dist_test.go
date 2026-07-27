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
		return nil
	})
	if err != nil {
		t.Fatalf("walk dist: %v", err)
	}

	assertSameOriginAttrs(t, filepath.Join(dist, "index.html"))
}

// assertSameOriginAttrs proves every <script src="..."> / <link href="...">
// in the built HTML resolves same-origin: either root-relative
// ("/board/...", required by vite.config.ts's base: "/board/" so assets
// resolve correctly when the page is mounted under a sub-path) or
// document-relative ("./x", "x"). None may carry a scheme or be
// protocol-relative ("//host/x") — that would be a CDN or other
// third-party origin, exactly what this item forbids.
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
	cmd.Env = append(os.Environ(), "VITE_BUILD_STAMP=go-test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("npm run build: %v\n%s", err, out)
	}
	return "dist"
}
