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

// TestReleaseWorkflow_DoesNotPushToMain proves release.yml never commits or
// pushes back into the repo (ready-3bd). It used to have an `update-site`
// job that pushed a version-bump commit straight to main; branch
// protection (ready-fe2: required 'test' check, no direct pushes)
// correctly rejected that push, so every release run reported FAILURE
// even when the actual release (binaries, checksums, signatures) was
// fine. The fix removes the write entirely rather than authorizing it, so
// this test guards against that job (or an equivalent `git push`) coming
// back.
func TestReleaseWorkflow_DoesNotPushToMain(t *testing.T) {
	raw := readWorkflow(t, "release.yml")

	if strings.Contains(raw, "git push") {
		t.Fatalf("release.yml runs `git push` — this is what branch protection (ready-fe2) rejects and caused every release run to report FAILURE (ready-3bd)")
	}
	wf := parseWorkflow(t, raw)
	if _, ok := wf.Jobs["update-site"]; ok {
		t.Fatalf("release.yml still has an update-site job that writes the version bump back to main")
	}
}

// TestPagesWorkflow_StampsVersionFromLatestTag proves the Pages deploy
// derives site/index.html's softwareVersion from the latest release tag at
// BUILD time (ready-3bd fix (c)/(d)) rather than release.yml committing a
// version string to main. The stamp must:
//   - run AFTER assemble-pages.sh (so it edits the assembled _site/ output,
//     not the pre-assembly site/ source — editing the source would be
//     silently discarded by the next assemble run and never verified here)
//   - run BEFORE upload-pages-artifact (so the stamped value actually ships)
//   - derive the version via `git describe --tags`, not a hardcoded/copied
//     string (a derived version cannot drift from the actual latest
//     release; a hardcoded one reintroduces the drift risk the fix exists
//     to remove)
//   - never commit or push (the whole point is to stop writing to the repo)
func TestPagesWorkflow_StampsVersionFromLatestTag(t *testing.T) {
	raw := readWorkflow(t, "pages.yml")
	wf := parseWorkflow(t, raw)

	deploy, ok := wf.Jobs["deploy"]
	if !ok {
		t.Fatalf("pages.yml has no `deploy` job")
	}

	assembleIdx, stampIdx, uploadIdx := -1, -1, -1
	for i, step := range deploy.Steps {
		switch {
		case strings.Contains(step.Run, "scripts/assemble-pages.sh"):
			assembleIdx = i
		case strings.Contains(step.Run, "softwareVersion"):
			stampIdx = i
		}
		if step.Uses != "" && strings.HasPrefix(step.Uses, "actions/upload-pages-artifact") {
			uploadIdx = i
		}
	}

	if stampIdx == -1 {
		t.Fatalf("deploy job has no version-stamping step (expected a step editing softwareVersion)")
	}
	stampRun := deploy.Steps[stampIdx].Run
	if strings.Contains(stampRun, "git commit") || strings.Contains(stampRun, "git push") {
		t.Fatalf("pages.yml version-stamp step commits/pushes — it must only edit the built _site/ artifact, never write back to the repo (ready-3bd)")
	}
	if !strings.Contains(stampRun, "git describe") {
		t.Fatalf("pages.yml version-stamp step does not derive the version via `git describe` — a non-derived version can drift from the actual latest release")
	}
	if !strings.Contains(stampRun, "_site/index.html") {
		t.Fatalf("pages.yml version-stamp step does not target the assembled _site/index.html")
	}

	if assembleIdx == -1 {
		t.Fatalf("deploy job has no assemble-pages.sh step")
	}
	if stampIdx < assembleIdx {
		t.Fatalf("version-stamp step (index %d) runs before assemble-pages.sh (index %d) — it would edit the pre-assembly site/ source, which the next assemble step overwrites unstamped", stampIdx, assembleIdx)
	}
	if uploadIdx == -1 {
		t.Fatalf("deploy job has no upload-pages-artifact step")
	}
	if stampIdx > uploadIdx {
		t.Fatalf("version-stamp step (index %d) runs after upload-pages-artifact (index %d) — the stamped version never reaches the deployed site", stampIdx, uploadIdx)
	}

	if !strings.Contains(raw, "fetch-depth: 0") {
		t.Fatalf("pages.yml checkout has no fetch-depth: 0 — `git describe --tags` needs full history and tags to find the latest release tag")
	}
}

// TestPagesWorkflow_TriggersOnReleasePublished proves pages.yml redeploys
// when a release is published (ready-3bd). Since release.yml no longer
// pushes a version-bump commit to main, a release would otherwise never
// retrigger this path-filtered (site/**, web/**) workflow, and the
// deployed site's stamped version would go stale until an unrelated
// site/web change happened to redeploy it.
func TestPagesWorkflow_TriggersOnReleasePublished(t *testing.T) {
	raw := readWorkflow(t, "pages.yml")
	wf := parseWorkflow(t, raw)

	if wf.On.Release == nil {
		t.Fatalf("pages.yml does not trigger on `release` events at all")
	}
	found := false
	for _, typ := range wf.On.Release.Types {
		if typ == "published" {
			found = true
		}
	}
	if !found {
		t.Fatalf("pages.yml `release` trigger does not include `published`, got types=%v", wf.On.Release.Types)
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
	Release     *releaseTrigger     `yaml:"release"`
}

type releaseTrigger struct {
	Types []string `yaml:"types"`
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
