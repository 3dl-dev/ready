package scripts

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestPagesWorkflow_UsesAssembleScript proves .github/workflows/pages.yml
// calls scripts/assemble-pages.sh instead of the old inline mkdir/cp lines
// that were guarded by nothing but a YAML comment (ready-2f1 rework).
//
// It also proves the assemble script's OWN output directory argument is the
// same directory upload-pages-artifact uploads. A prior version of this
// test only checked that assemble-pages.sh was called at all: reverting
// upload-pages-artifact's `path:` back to `site/` (pre-ready-2f1 behavior)
// while leaving the assemble call intact left this test green — the
// assemble step would run, build /board's real content into _site/, and
// then the deploy would upload plain site/ instead, so /board 404s on the
// live site forever with no red test anywhere (ready-2f1 veracity hole).
// Parsing the workflow's actual step list (rather than substring-matching
// the raw text) also catches the assemble script being told to write
// somewhere other than _site (e.g. a typo'd output arg).
func TestPagesWorkflow_UsesAssembleScript(t *testing.T) {
	raw := readWorkflow(t, "pages.yml")
	wf := parseWorkflow(t, raw)

	if !strings.Contains(raw, "scripts/assemble-pages.sh") {
		t.Fatalf("pages.yml no longer calls scripts/assemble-pages.sh")
	}
	if strings.Contains(raw, "cp -r site/. _site/") {
		t.Fatalf("pages.yml still has the old un-tested inline assemble step")
	}

	deploy, ok := wf.Jobs["deploy"]
	if !ok {
		t.Fatalf("pages.yml has no `deploy` job")
	}

	var assembleOutDir, uploadDir string
	var sawAssemble, sawUpload bool
	for _, step := range deploy.Steps {
		if strings.Contains(step.Run, "scripts/assemble-pages.sh") {
			sawAssemble = true
			fields := strings.Fields(step.Run)
			if len(fields) < 4 {
				t.Fatalf("assemble-pages.sh invocation %q does not look like `scripts/assemble-pages.sh <site> <board-dist> <output-dir>`", step.Run)
			}
			assembleOutDir = strings.TrimSuffix(fields[len(fields)-1], "/")
		}
		if step.Uses != "" && strings.HasPrefix(step.Uses, "actions/upload-pages-artifact") {
			sawUpload = true
			uploadDir = strings.TrimSuffix(step.With["path"], "/")
		}
	}
	if !sawAssemble {
		t.Fatalf("deploy job has no step invoking scripts/assemble-pages.sh")
	}
	if !sawUpload {
		t.Fatalf("deploy job has no actions/upload-pages-artifact step")
	}
	if assembleOutDir != uploadDir {
		t.Fatalf("assemble-pages.sh writes to %q but upload-pages-artifact uploads %q — the assembled /board content never reaches the deployed site", assembleOutDir, uploadDir)
	}
}

// TestBoardCIWorkflow_GatesPullRequests proves a PR touching web/board runs
// npm ci, npm run typecheck, and npm run build (in that order) BEFORE
// merge, so a TypeScript error is caught on the PR — never first
// discovered on main, inside the same job that deploys the root
// ready.3dl.dev site (ready-2f1 rework).
//
// The path-filter check parses the workflow's `on.pull_request` block
// rather than substring-matching the raw YAML, so it distinguishes an
// INCLUDE filter (`paths:`) from an EXCLUDE filter (`paths-ignore:`).
// Substring-matching for "web/board/**" alone stays green under
// `paths-ignore: [web/board/**]`, which inverts the gate so a PR touching
// web/board is the one case that never runs this workflow — the opposite
// of what the workflow exists to guarantee (ready-2f1 veracity hole).
func TestBoardCIWorkflow_GatesPullRequests(t *testing.T) {
	raw := readWorkflow(t, "board-ci.yml")
	wf := parseWorkflow(t, raw)

	if wf.On.PullRequest == nil {
		t.Fatalf("board-ci.yml does not trigger on pull_request")
	}
	if len(wf.On.PullRequest.PathsIgnore) > 0 {
		t.Fatalf("board-ci.yml uses paths-ignore %v — an EXCLUDE filter inverts the gate so a PR touching web/board is the only case that does NOT run it", wf.On.PullRequest.PathsIgnore)
	}
	if !containsPath(wf.On.PullRequest.Paths, "web/board/**") {
		t.Fatalf("board-ci.yml's pull_request.paths (INCLUDE filter) does not scope to web/board/**, got %v", wf.On.PullRequest.Paths)
	}

	ciIdx := strings.Index(raw, "npm ci")
	typecheckIdx := strings.Index(raw, "npm run typecheck")
	buildIdx := strings.Index(raw, "npm run build")
	if ciIdx == -1 || typecheckIdx == -1 || buildIdx == -1 {
		t.Fatalf("board-ci.yml is missing one of: npm ci, npm run typecheck, npm run build")
	}
	if !(ciIdx < typecheckIdx && typecheckIdx < buildIdx) {
		t.Fatalf("board-ci.yml does not run npm ci, then typecheck, then build in that order")
	}
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

// --- minimal typed model of the GitHub Actions workflow YAML we care about ---

type workflowFile struct {
	On   workflowOn           `yaml:"on"`
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowOn struct {
	PullRequest *pullRequestTrigger `yaml:"pull_request"`
}

type pullRequestTrigger struct {
	Paths       []string `yaml:"paths"`
	PathsIgnore []string `yaml:"paths-ignore"`
}

type workflowJob struct {
	Steps []workflowStep `yaml:"steps"`
}

type workflowStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
}

func parseWorkflow(t *testing.T, raw string) workflowFile {
	t.Helper()
	var wf workflowFile
	if err := yaml.Unmarshal([]byte(raw), &wf); err != nil {
		t.Fatalf("parse workflow YAML: %v", err)
	}
	return wf
}

func readWorkflow(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../.github/workflows/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
