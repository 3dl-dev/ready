package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAssemblePages_ProducesExpectedArtifact runs the actual Pages assemble
// script (scripts/assemble-pages.sh — the same script
// .github/workflows/pages.yml invokes) against the real repo site/ tree and
// a fixture board dist, into a t.TempDir(), and proves the produced
// artifact is what ready.3dl.dev needs: the real CNAME content, every real
// site/ file (except *_test.go, which must NOT ship), and board/index.html.
//
// Before this test the assemble step was inline mkdir/cp shell lines in
// pages.yml guarded by nothing but a comment (ready-2f1 rework). Before
// this branch at all, the workflow used
// `upload-pages-artifact path: site/` directly with no assembly step and
// so could never break the root site; this branch introduced that
// capability, and this test is what guards against it regressing.
func TestAssemblePages_ProducesExpectedArtifact(t *testing.T) {
	repoRoot := repoRootDir(t)
	scriptPath := filepath.Join(repoRoot, "scripts", "assemble-pages.sh")
	siteDir := filepath.Join(repoRoot, "site")

	boardDist := t.TempDir()
	writeFile(t, filepath.Join(boardDist, "index.html"), "<html>board placeholder</html>")
	writeFile(t, filepath.Join(boardDist, "assets", "index-ABC123.js"), "console.log('board');")

	outDir := filepath.Join(t.TempDir(), "_site")

	cmd := exec.Command(scriptPath, siteDir, boardDist, outDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("assemble-pages.sh failed: %v\n%s", err, out)
	}

	// site/CNAME must survive with its real production content — this is
	// what tells GitHub Pages the custom domain, so a broken copy silently
	// reverts the site to the github.io default domain.
	cname, err := os.ReadFile(filepath.Join(outDir, "CNAME"))
	if err != nil {
		t.Fatalf("artifact missing CNAME: %v", err)
	}
	if got := strings.TrimSpace(string(cname)); got != "ready.3dl.dev" {
		t.Fatalf("CNAME = %q, want %q", got, "ready.3dl.dev")
	}

	// Every real site/ file must be present in the artifact, EXCEPT
	// *_test.go source files, which must be excluded (pre-existing issue:
	// site/content_test.go was being shipped as public content).
	siteEntries, err := os.ReadDir(siteDir)
	if err != nil {
		t.Fatalf("read site dir: %v", err)
	}
	sawTestGo := false
	for _, e := range siteEntries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), "_test.go") {
			sawTestGo = true
			if _, statErr := os.Stat(filepath.Join(outDir, e.Name())); statErr == nil {
				t.Fatalf("artifact must not ship %s (Go test source, not site content)", e.Name())
			}
			continue
		}
		if _, statErr := os.Stat(filepath.Join(outDir, e.Name())); statErr != nil {
			t.Fatalf("artifact missing site file %s: %v", e.Name(), statErr)
		}
	}
	if !sawTestGo {
		t.Fatalf("test assumption broken: site/ no longer contains a *_test.go file to exclude — update this test")
	}

	// board/ dist is layered in at /board/, alongside the untouched root.
	if _, err := os.Stat(filepath.Join(outDir, "board", "index.html")); err != nil {
		t.Fatalf("artifact missing board/index.html: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// repoRootDir returns the repository root, derived from this test file's
// own location (<repoRoot>/scripts) rather than assuming a fixed absolute
// path or the invoking shell's working directory.
func repoRootDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Dir(wd)
}
