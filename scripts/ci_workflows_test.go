package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// TestReleaseWorkflow_MarksPrereleaseTagsAsPrerelease proves the "Create
// GitHub Release" step (softprops/action-gh-release@v3) computes its
// `prerelease` input from the tag itself, so a tag carrying a semver
// prerelease suffix (a hyphen, e.g. v0.17.1-rc1) is never marked as the
// repo's "latest" release.
//
// softprops/action-gh-release defaults `prerelease` to false when the
// input is absent, and GitHub marks the newest published non-prerelease
// release as "latest" — the release `rd upgrade` hands to real users. A
// test tag cut to verify this item's done condition (or any rc/beta tag)
// would silently become that latest release with no `prerelease` input at
// all.
//
// This does not just check the `prerelease:` key exists — a key set to a
// literal `true`, or to a condition on the wrong field, would pass a
// presence check while still shipping every stable release as a
// prerelease, or every prerelease as latest. Instead it extracts the
// actual expression and evaluates it (via a tiny local re-implementation
// of GitHub Actions' `contains()`) against a table of real and synthetic
// tag names, so both directions of the classification are proven: stable
// tags must NOT be prerelease, and suffixed tags MUST be.
func TestReleaseWorkflow_MarksPrereleaseTagsAsPrerelease(t *testing.T) {
	raw := readWorkflow(t, "release.yml")
	wf := parseWorkflow(t, raw)

	release, ok := wf.Jobs["release"]
	if !ok {
		t.Fatalf("release.yml has no `release` job")
	}

	var prereleaseExpr string
	var sawRelease bool
	for _, step := range release.Steps {
		if step.Uses != "" && strings.HasPrefix(step.Uses, "softprops/action-gh-release") {
			sawRelease = true
			prereleaseExpr = step.With["prerelease"]
		}
	}
	if !sawRelease {
		t.Fatalf("release.yml has no softprops/action-gh-release step")
	}
	if prereleaseExpr == "" {
		t.Fatalf("Create GitHub Release step has no `prerelease` input — softprops/action-gh-release defaults this to false, so a test tag like v0.17.1-rc1 would be marked the repo's latest release and handed to `rd upgrade` users (ready-3bd)")
	}

	cases := []struct {
		ref  string
		want bool
	}{
		{"v0.17.0", false},
		{"v1.0.0", false},
		{"v0.17.1-rc1", true},
		{"v1.0.0-beta.2", true},
		{"v2.3.4-test", true},
	}
	for _, c := range cases {
		got, ok := evalContainsGithubRefName(prereleaseExpr, c.ref)
		if !ok {
			t.Fatalf("`prerelease` expression %q is not a recognizable `contains(github.ref_name, '<suffix>')` check this test knows how to evaluate", prereleaseExpr)
		}
		if got != c.want {
			t.Fatalf("`prerelease` expression %q evaluated against tag %q = %v, want %v — a stable tag would ship as prerelease (never becoming `latest`) or a prerelease tag would ship as a normal release and become GitHub's `latest`, which is what `rd upgrade` installs (ready-3bd)", prereleaseExpr, c.ref, got, c.want)
		}
	}
}

// evalContainsGithubRefName evaluates a GitHub Actions expression of the
// shape `contains(github.ref_name, '<substr>')` (optionally wrapped in
// `${{ }}`) against a candidate ref name, using Go's strings.Contains —
// which is exactly the semantics GitHub Actions' own `contains()` function
// implements for two string arguments. ok is false if expr isn't in this
// shape, so a caller can distinguish "evaluated to false" from "this test
// doesn't understand the expression."
func evalContainsGithubRefName(expr, refName string) (result bool, ok bool) {
	e := strings.TrimSpace(expr)
	e = strings.TrimPrefix(e, "${{")
	e = strings.TrimSuffix(e, "}}")
	e = strings.TrimSpace(e)

	m := regexp.MustCompile(`^contains\(\s*github\.ref_name\s*,\s*'([^']*)'\s*\)$`).FindStringSubmatch(e)
	if m == nil {
		return false, false
	}
	return strings.Contains(refName, m[1]), true
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

// TestPagesWorkflow_StampExcludesPrereleaseTags proves the version-stamp
// step's `git describe` invocation resolves the latest STABLE tag only,
// never a prerelease tag (ready-3bd).
//
// `git describe --tags --abbrev=0 --match 'v*'` alone matches ANY
// reachable tag, including a prerelease like v0.17.1-rc1 — if such a tag
// were ever cut (e.g. to verify this very item's done condition), it
// would get stamped onto the live site's softwareVersion as though it
// were the actual release.
//
// This test does not assert the presence of an `--exclude` flag as text —
// that is exactly the trap that produced this item's earlier defect (a
// YAML-key check that stayed green while the underlying event never
// fired). Instead it extracts the literal `git describe ...` command from
// the step, builds a real scratch git repository with a stable tag AND a
// prerelease tag reachable from HEAD, and actually EXECUTES the extracted
// command against it — proving the real behaviour, not the flag's
// documented behaviour.
func TestPagesWorkflow_StampExcludesPrereleaseTags(t *testing.T) {
	raw := readWorkflow(t, "pages.yml")
	wf := parseWorkflow(t, raw)

	deploy, ok := wf.Jobs["deploy"]
	if !ok {
		t.Fatalf("pages.yml has no `deploy` job")
	}

	var stampRun string
	for _, step := range deploy.Steps {
		if strings.Contains(step.Run, "softwareVersion") {
			stampRun = step.Run
		}
	}
	if stampRun == "" {
		t.Fatalf("deploy job has no version-stamping step (expected a step editing softwareVersion)")
	}

	idx := strings.Index(stampRun, "git describe")
	if idx == -1 {
		t.Fatalf("version-stamp step does not invoke `git describe` at all")
	}
	rest := stampRun[idx:]
	end := strings.Index(rest, "2>/dev/null")
	if end == -1 {
		t.Fatalf("version-stamp step's `git describe` invocation %q has no `2>/dev/null` guard this test knows how to isolate the command from", rest)
	}
	describeCmd := strings.TrimSpace(rest[:end])

	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available to execute the real command: %v", err)
	}

	dir := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	runShell := func(command string) string {
		t.Helper()
		cmd := exec.Command("sh", "-c", command)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("running extracted command %q: %v\n%s", command, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init", "-q")
	runGit("config", "user.email", "t@example.com")
	runGit("config", "user.name", "t")

	writeAndCommit := func(msg string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(msg), 0o644); err != nil {
			t.Fatalf("write f.txt: %v", err)
		}
		runGit("add", ".")
		runGit("commit", "-q", "-m", msg)
	}

	// Stable tag only: must resolve to itself.
	writeAndCommit("one")
	runGit("tag", "v0.17.0")
	if got := runShell(describeCmd); got != "v0.17.0" {
		t.Fatalf("extracted command %q returned %q with only a stable tag reachable, want %q", describeCmd, got, "v0.17.0")
	}

	// A prerelease tag lands ahead of the stable tag: must still resolve
	// to the stable tag, not the prerelease.
	writeAndCommit("two")
	runGit("tag", "v0.17.1-rc1")
	if got := runShell(describeCmd); got != "v0.17.0" {
		t.Fatalf("extracted command %q returned %q with prerelease tag v0.17.1-rc1 reachable ahead of stable v0.17.0, want %q (the prerelease must never be stamped onto the live site as the release version)", describeCmd, got, "v0.17.0")
	}

	// A new stable tag then supersedes the prerelease: must track forward.
	writeAndCommit("three")
	runGit("tag", "v0.17.1")
	if got := runShell(describeCmd); got != "v0.17.1" {
		t.Fatalf("extracted command %q returned %q after a new stable tag v0.17.1 was cut past the prerelease, want %q — the exclusion must not get stuck on an old stable tag forever", describeCmd, got, "v0.17.1")
	}
}

// TestPagesWorkflow_RedeploysAfterReleaseCompletes proves pages.yml
// retriggers when the Release workflow finishes, using a trigger that
// ACTUALLY fires under GITHUB_TOKEN semantics (ready-3bd WAVE 1 fix).
//
// A prior version of this fix used `on.release: types: [published]`, and a
// prior version of THIS test only asserted that YAML key existed — which
// stayed green even though the trigger can never fire in this repo.
// release.yml's "Create GitHub Release" step (softprops/action-gh-release@v3)
// runs with no token override, so the release is authored by the default
// GITHUB_TOKEN, and GitHub does not start new workflow runs from events
// authored by GITHUB_TOKEN — this is a hard platform restriction, not
// something any YAML key on the `release` trigger can override. Proof (no
// tag needed): `gh api repos/3dl-dev/ready/releases` shows v0.17.0/v0.16.2/
// v0.16.1 all authored by github-actions[bot], and
// `gh api repos/3dl-dev/ready/actions/runs` has never recorded a
// release-triggered run in this repo (only push/pull_request/dynamic
// events appear).
//
// `workflow_run` has no such restriction: it fires from the Release
// workflow's own run completing, regardless of what token the *steps
// inside that run* used to create the GitHub Release. So this test:
//  1. asserts pages.yml is NOT relying on the `release` trigger anymore
//     (closing off a regression back to the trap), and
//  2. asserts it uses `workflow_run` scoped to the exact workflow name
//     `Release` (must match release.yml's `name:` field, or the platform
//     silently never fires it) with `types: [completed]` (the only type
//     workflow_run supports — there is no "published" analog), and
//  3. asserts the deploy job gates on `conclusion == 'success'` so a
//     FAILED release build doesn't redeploy the site with a stale stamp
//     over a false-green appearance.
func TestPagesWorkflow_RedeploysAfterReleaseCompletes(t *testing.T) {
	raw := readWorkflow(t, "pages.yml")
	wf := parseWorkflow(t, raw)

	if wf.On.Release != nil {
		t.Fatalf("pages.yml still declares an `on.release` trigger (%+v) — this event is never delivered because release.yml creates releases with the default GITHUB_TOKEN, which cannot start new workflow runs (ready-3bd)", wf.On.Release)
	}

	if wf.On.WorkflowRun == nil {
		t.Fatalf("pages.yml has no `on.workflow_run` trigger — nothing retriggers a Pages deploy after a release, so the stamped version goes stale until an unrelated site/web change lands (ready-3bd)")
	}
	releaseWorkflowName := readWorkflowName(t, "release.yml")
	if !containsPath(wf.On.WorkflowRun.Workflows, releaseWorkflowName) {
		t.Fatalf("pages.yml's workflow_run.workflows %v does not include %q (release.yml's actual `name:`) — a mismatched name means the trigger silently never fires", wf.On.WorkflowRun.Workflows, releaseWorkflowName)
	}
	if !containsPath(wf.On.WorkflowRun.Types, "completed") {
		t.Fatalf("pages.yml's workflow_run trigger does not include types: [completed], got %v", wf.On.WorkflowRun.Types)
	}

	deploy, ok := wf.Jobs["deploy"]
	if !ok {
		t.Fatalf("pages.yml has no `deploy` job")
	}
	if !strings.Contains(deploy.If, "workflow_run") || !strings.Contains(deploy.If, "conclusion") || !strings.Contains(deploy.If, "success") {
		t.Fatalf("deploy job's `if:` (%q) does not gate workflow_run redeploys on conclusion == 'success' — a failed Release run would still redeploy the site", deploy.If)
	}
}

func readWorkflowName(t *testing.T, file string) string {
	t.Helper()
	raw := readWorkflow(t, file)
	var meta struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatalf("parse %s name: %v", file, err)
	}
	if meta.Name == "" {
		t.Fatalf("%s has no top-level `name:`", file)
	}
	return meta.Name
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
	WorkflowRun *workflowRunTrigger `yaml:"workflow_run"`
}

type releaseTrigger struct {
	Types []string `yaml:"types"`
}

type workflowRunTrigger struct {
	Workflows []string `yaml:"workflows"`
	Types     []string `yaml:"types"`
}

type pullRequestTrigger struct {
	Paths       []string `yaml:"paths"`
	PathsIgnore []string `yaml:"paths-ignore"`
}

type workflowJob struct {
	If    string         `yaml:"if"`
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
