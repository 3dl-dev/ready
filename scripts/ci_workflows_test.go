package scripts

import (
	"os"
	"strings"
	"testing"
)

// TestPagesWorkflow_UsesAssembleScript proves .github/workflows/pages.yml
// calls scripts/assemble-pages.sh instead of the old inline mkdir/cp lines
// that were guarded by nothing but a YAML comment (ready-2f1 rework).
func TestPagesWorkflow_UsesAssembleScript(t *testing.T) {
	yaml := readWorkflow(t, "pages.yml")

	if !strings.Contains(yaml, "scripts/assemble-pages.sh") {
		t.Fatalf("pages.yml no longer calls scripts/assemble-pages.sh")
	}
	if strings.Contains(yaml, "cp -r site/. _site/") {
		t.Fatalf("pages.yml still has the old un-tested inline assemble step")
	}
}

// TestBoardCIWorkflow_GatesPullRequests proves a PR touching web/board runs
// npm ci, npm run typecheck, and npm run build (in that order) BEFORE
// merge, so a TypeScript error is caught on the PR — never first
// discovered on main, inside the same job that deploys the root
// ready.3dl.dev site (ready-2f1 rework).
func TestBoardCIWorkflow_GatesPullRequests(t *testing.T) {
	yaml := readWorkflow(t, "board-ci.yml")

	if !strings.Contains(yaml, "pull_request:") {
		t.Fatalf("board-ci.yml does not trigger on pull_request")
	}
	if !strings.Contains(yaml, "web/board/**") {
		t.Fatalf("board-ci.yml does not scope its path filter to web/board/**")
	}

	ciIdx := strings.Index(yaml, "npm ci")
	typecheckIdx := strings.Index(yaml, "npm run typecheck")
	buildIdx := strings.Index(yaml, "npm run build")
	if ciIdx == -1 || typecheckIdx == -1 || buildIdx == -1 {
		t.Fatalf("board-ci.yml is missing one of: npm ci, npm run typecheck, npm run build")
	}
	if !(ciIdx < typecheckIdx && typecheckIdx < buildIdx) {
		t.Fatalf("board-ci.yml does not run npm ci, then typecheck, then build in that order")
	}
}

func readWorkflow(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../.github/workflows/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
