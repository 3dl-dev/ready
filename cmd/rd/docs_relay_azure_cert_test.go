package main

// docs_relay_azure_cert_test.go — ready-199 rework: mechanically checks the
// cross-repo claims in docs/relay-runbook.md's "Public relay TLS certificate
// renewal" section against ground truth instead of trusting prose to stay
// accurate. The prior round shipped an az command that does not run, a
// remediation pointer at the wrong section for the wrong domain, a wrong
// bicep line citation, and a browser-blast-radius claim asserted rather than
// checked — none of which any test caught. This file closes that gap:
//
//   - every `§N` reference into nostr-relay/docs/prod/deploy.md must name a
//     heading that actually exists there:
//     TestRelayRunbookAzureSection_SectionReferencesExistInSiblingRepo
//   - every `prod.bicep:N-M` line-range citation must land on the anchor
//     text it claims to cite:
//     TestRelayRunbookAzureSection_BicepLineCitationsAccurate
//   - every az flag used in the section's fenced bash blocks must be a flag
//     the REAL az CLI accepts for that exact subcommand — checked against
//     `az --help`'s own ground truth, not deploy.md's prose, because the
//     specific defect this guards (`--environment` on `az containerapp env
//     certificate list`) is a subcommand deploy.md never mentions at all:
//     TestRelayRunbookAzureSection_AzFlagsAreValidForTheirSubcommand
//   - the "relay.moot.pub has no observable browser blast radius today"
//     claim is checked against the actual runtime relay config the browser
//     board loads, not asserted:
//     TestRelayRunbookAzureSection_BlastRadiusClaimMatchesRelayConfig
//
// GROUND TRUTH IS A CHECKED-IN FIXTURE, NOT A LIVE DEPENDENCY (ready-199
// round 3). The first three tests above need the real `az` CLI and/or a
// sibling `nostr-relay` checkout to establish ground truth, and CI's runner
// (.github/workflows/go-test.yml: a single actions/checkout@v5, no cloud CLI
// install, no sibling repo) has neither. A round-2 version of this file
// self-skipped when either was absent — which is not a guard: it means the
// ONE check that catches the headline defect (an az flag that doesn't exist
// for its subcommand) never ran in CI at all, and the doc could rot back to
// an unrunnable command while every PR stayed green. Instead, the ground
// truth (deploy.md's heading numbers, the two bicep citation snippets, and
// each az subcommand's real flag set) is captured once into
// testdata/relay_azure_cert_fixture.json and the tests below assert the doc
// against THAT — no exec.Command, no sibling-repo read, in the normal test
// run. That makes all three hermetic: they run, and can fail, on any CI
// runner with just this repo checked out.
//
// The fixture is refreshed by TestRegenerateAzureCertFixtureOC, which is
// gated behind the `-regen-azure-cert-fixture` flag: `go test ./cmd/rd/
// -run TestRegenerateAzureCertFixtureOC -regen-azure-cert-fixture -v` on a
// machine with `az` on PATH and a `nostr-relay` checkout next to this repo.
// That gate is opt-in (it requires a human to pass an explicit flag to
// intentionally refresh ground truth), not silent-on-absence (a plain
// `go test ./...` never takes that branch and never needs to) — the
// distinction that matters: an opt-in skip is a maintenance tool a human
// invokes on purpose; a silent-absence skip is a guard quietly not guarding.
// Whoever changes docs/relay-runbook.md's Azure section, deploy.md's
// headings, prod.bicep's cert declarations, or the az CLI's own flags is
// responsible for re-running the regenerator and committing the refreshed
// fixture; a citation the fixture has no recorded data for fails loudly
// (below) rather than silently passing.
//
// UNCOVERED DEFECT CLASS (said explicitly, not left implied): none of these
// tests, nor the regenerator, check that a cited `§N` is the *semantically
// correct* section for the domain being discussed at that point in the doc
// (round 2's actual defect: §5 is a real, existing heading, but it is
// `relay.dontguess.ai`'s section, not the two managed-cert hostnames' — a
// heading-existence check alone passes that. Confirmed by reverting to the
// round-2 doc: TestRelayRunbookAzureSection_SectionReferencesExistInSiblingRepo
// still PASSES because §5 exists.) That right-number-wrong-domain class is
// not mechanically checkable from the heading list or the bicep line
// content alone and is caught only by human prose review at PR time.

import (
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var regenAzureCertFixtureOC = flag.Bool("regen-azure-cert-fixture", false,
	"regenerate cmd/rd/testdata/relay_azure_cert_fixture.json from a live `az` CLI "+
		"and a sibling nostr-relay checkout (see docs_relay_azure_cert_test.go header)")

const azureCertFixturePathOC = "testdata/relay_azure_cert_fixture.json"

// azureCertFixtureOC is the checked-in ground truth these tests assert
// against, in place of a live `az` CLI call or a live sibling-repo read.
type azureCertFixtureOC struct {
	DeployMdHeadings  []string            `json:"deploy_md_headings"`
	BicepCitations    map[string]string   `json:"bicep_citations"`
	AzSubcommandFlags map[string][]string `json:"az_subcommand_flags"`
}

// loadAzureCertFixtureOC reads the checked-in fixture. It never touches
// `az` or a sibling repo, so it always succeeds in CI.
func loadAzureCertFixtureOC(t *testing.T) azureCertFixtureOC {
	t.Helper()
	raw, err := os.ReadFile(azureCertFixturePathOC)
	if err != nil {
		t.Fatalf("could not read golden fixture %s: %v — if this is expected "+
			"(new az subcommand, new §N, new bicep citation), regenerate it: "+
			"go test ./cmd/rd/ -run TestRegenerateAzureCertFixtureOC "+
			"-regen-azure-cert-fixture -v (needs `az` on PATH and a sibling "+
			"nostr-relay checkout)", azureCertFixturePathOC, err)
	}
	var fx azureCertFixtureOC
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("golden fixture %s is not valid JSON: %v", azureCertFixturePathOC, err)
	}
	return fx
}

// azureCertSectionOC returns the "## Public relay TLS certificate renewal"
// section of docs/relay-runbook.md (from its heading to the next top-level
// heading, or EOF).
func azureCertSectionOC(t *testing.T) string {
	t.Helper()
	doc := readDocOrFatalOC(t, "../../docs/relay-runbook.md")
	const heading = "## Public relay TLS certificate renewal"
	idx := strings.Index(doc, heading)
	if idx < 0 {
		t.Fatalf("docs/relay-runbook.md is missing the %q section", heading)
	}
	rest := doc[idx:]
	if next := strings.Index(rest[len(heading):], "\n## "); next >= 0 {
		rest = rest[:len(heading)+next]
	}
	return rest
}

// siblingRepoRootOC locates the checkout of `name` as a sibling of THIS
// repo's main worktree — i.e. sharing ready's parent directory — regardless
// of whether the test happens to be running from a linked worktree.
// `git rev-parse --git-common-dir` always resolves to the MAIN worktree's
// .git dir even from a linked worktree, so this is stable across both.
// Returns ("", false) if it can't be found. Only called from the opt-in
// regenerator, never from the hermetic assertion tests.
func siblingRepoRootOC(t *testing.T, name string) (string, bool) {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", false
	}
	absGitDir, err := filepath.Abs(strings.TrimSpace(string(out)))
	if err != nil {
		return "", false
	}
	readyRoot := filepath.Dir(absGitDir)    // .../ready
	projectsRoot := filepath.Dir(readyRoot) // .../projects
	root := filepath.Join(projectsRoot, name)
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return "", false
	}
	return root, true
}

var sectionRefRe = regexp.MustCompile(`§(\d+)`)

// TestRelayRunbookAzureSection_SectionReferencesExistInSiblingRepo guards
// against citing a `§N` that doesn't exist (or, as shipped two rounds ago,
// exists but is the wrong section for the domain being discussed at that
// point — see the "UNCOVERED DEFECT CLASS" note in the file header; this
// test only catches the number going stale or typo'd). Checked against the
// checked-in fixture's recorded heading list, not a live sibling read.
func TestRelayRunbookAzureSection_SectionReferencesExistInSiblingRepo(t *testing.T) {
	fx := loadAzureCertFixtureOC(t)
	if len(fx.DeployMdHeadings) == 0 {
		t.Fatalf("golden fixture %s has zero recorded deploy_md_headings — regenerate it", azureCertFixturePathOC)
	}
	existing := map[string]bool{}
	for _, n := range fx.DeployMdHeadings {
		existing[n] = true
	}

	section := azureCertSectionOC(t)
	seen := map[string]bool{}
	for _, m := range sectionRefRe.FindAllStringSubmatch(section, -1) {
		seen[m[1]] = true
	}
	if len(seen) == 0 {
		t.Fatalf("docs/relay-runbook.md's Azure cert section cites zero '§N' sections — expected at least one reference into nostr-relay/docs/prod/deploy.md")
	}
	var bad []string
	for n := range seen {
		if !existing[n] {
			bad = append(bad, "§"+n)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		t.Errorf("docs/relay-runbook.md cites %s but the golden fixture's recorded nostr-relay/docs/prod/deploy.md headings are %v — either the doc citation is stale/wrong, or deploy.md has been renumbered and the fixture needs regenerating (see file header)", strings.Join(bad, ", "), fx.DeployMdHeadings)
	}
}

var bicepRangeRe = regexp.MustCompile(`prod\.bicep:(\d+)-(\d+)`)

// TestRelayRunbookAzureSection_BicepLineCitationsAccurate guards against the
// exact defect shipped two rounds ago: a line-range citation that lands on
// the wrong code (`330-370` was inside the containerApp/ingress resource,
// not the managedCertificates `existing` references it was cited for).
// Checked against the checked-in fixture's recorded snippet for that exact
// line range, not a live sibling read.
func TestRelayRunbookAzureSection_BicepLineCitationsAccurate(t *testing.T) {
	fx := loadAzureCertFixtureOC(t)

	section := azureCertSectionOC(t)
	matches := bicepRangeRe.FindAllStringSubmatch(section, -1)
	if len(matches) == 0 {
		t.Fatalf("docs/relay-runbook.md's Azure cert section cites zero 'prod.bicep:N-M' line ranges")
	}
	for _, m := range matches {
		key := m[1] + "-" + m[2]
		snippet, ok := fx.BicepCitations[key]
		if !ok {
			t.Errorf("docs/relay-runbook.md cites prod.bicep:%s but the golden fixture has no recorded content for that exact range — either it's a new citation (regenerate the fixture, see file header) or the line numbers drifted", key)
			continue
		}
		// Require the actual `resource <name> ... existing = {` DECLARATION,
		// not merely a reference to the name (e.g. `certificateId:
		// relayMootPubCert.id` inside containerApp/ingress.customDomains,
		// which is what the wrong `330-370` citation landed on two rounds
		// ago — a bare substring match on the name alone would have passed
		// that too, since the name is mentioned there as well).
		switch {
		case strings.Contains(snippet, "resource relayMootPubCert") && strings.Contains(snippet, "resource relay3dlNetworkCert"):
			// the managed-certificate `existing` resource declarations — ok.
		case strings.Contains(snippet, "relay.dontguess.ai") && strings.Contains(strings.ToLower(snippet), "deliberately"):
			// the dontguess.ai exclusion-rationale citation — ok.
		default:
			t.Errorf("docs/relay-runbook.md cites prod.bicep:%s but the golden fixture's recorded content for that range contains neither expected anchor (managed-cert `existing` resources, or the relay.dontguess.ai exclusion note) — got:\n%s", key, snippet)
		}
	}
}

var fencedBashRe = regexp.MustCompile("(?s)```bash\n(.*?)```")

// azCommandsInSectionOC extracts every literal `az ...` invocation from the
// section's fenced ```bash blocks, joining backslash-continued lines first.
func azCommandsInSectionOC(section string) []string {
	var cmds []string
	for _, m := range fencedBashRe.FindAllStringSubmatch(section, -1) {
		joined := strings.ReplaceAll(m[1], "\\\n", " ")
		for _, line := range strings.Split(joined, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "az ") {
				cmds = append(cmds, line)
			}
		}
	}
	return cmds
}

// azSubcommandAndFlagsOC splits a literal `az ...` invocation into its
// subcommand path (e.g. ["containerapp","env","certificate","list"]) and the
// flag tokens used (e.g. ["-g","-n","-m","-o"]), stopping subcommand
// collection at the first flag-like token.
func azSubcommandAndFlagsOC(cmd string) (subcommand []string, flags []string) {
	fields := strings.Fields(cmd)
	seenFlag := false
	for _, f := range fields[1:] { // fields[0] is "az"
		if strings.HasPrefix(f, "-") {
			seenFlag = true
			flags = append(flags, f)
		} else if !seenFlag {
			subcommand = append(subcommand, f)
		}
	}
	return subcommand, flags
}

// azFlagTokenRe matches a whole whitespace-delimited token that is a flag
// (e.g. "-n", "--name"). Deliberately whole-token, not a substring search:
// az help text lists adjacent flags separated by exactly one space (e.g.
// "--name -n"), and a substring pattern that consumes its own boundary
// whitespace misses the second flag in that pair because FindAll doesn't
// re-consume whitespace already claimed by the previous match.
var azFlagTokenRe = regexp.MustCompile(`^-{1,2}[A-Za-z][A-Za-z-]*$`)

// azHelpFlagsLiveOC returns every flag `az <subcommand> --help` documents
// (short and long forms, from both "Arguments" and "Global Arguments").
// Only called from the opt-in regenerator — never from the hermetic
// assertion test, which reads the recorded fixture instead.
func azHelpFlagsLiveOC(t *testing.T, subcommand []string) []string {
	t.Helper()
	args := append(append([]string{}, subcommand...), "--help")
	out, err := exec.Command("az", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("az %s --help failed: %v\n%s", strings.Join(subcommand, " "), err, out)
	}
	flags := map[string]bool{}
	for _, tok := range strings.Fields(string(out)) {
		tok = strings.TrimRight(tok, ".,:;)")
		if azFlagTokenRe.MatchString(tok) {
			flags[tok] = true
		}
	}
	var sorted []string
	for f := range flags {
		sorted = append(sorted, f)
	}
	sort.Strings(sorted)
	return sorted
}

// TestRelayRunbookAzureSection_AzFlagsAreValidForTheirSubcommand guards
// against the exact defect shipped two rounds ago: `az containerapp env
// certificate list ... --environment ...` does not run because that
// subcommand's environment flag is `-n/--name`, not `--environment`. Checked
// against the checked-in fixture's recorded flag set for that subcommand,
// not a live `az --help` call — this is the guard that most needed to be
// hermetic: it is the one that catches the headline defect, and a bare
// `exec.LookPath("az")` skip meant it never ran in CI at all.
func TestRelayRunbookAzureSection_AzFlagsAreValidForTheirSubcommand(t *testing.T) {
	fx := loadAzureCertFixtureOC(t)

	section := azureCertSectionOC(t)
	cmds := azCommandsInSectionOC(section)
	if len(cmds) == 0 {
		t.Fatalf("docs/relay-runbook.md's Azure cert section has zero literal `az ...` commands in its fenced bash blocks")
	}
	for _, cmd := range cmds {
		sub, flags := azSubcommandAndFlagsOC(cmd)
		if len(sub) == 0 {
			t.Errorf("could not parse a subcommand out of %q", cmd)
			continue
		}
		subKey := strings.Join(sub, " ")
		allowedList, ok := fx.AzSubcommandFlags[subKey]
		if !ok {
			t.Errorf("docs/relay-runbook.md uses %q but the golden fixture has no recorded flag set for `az %s` — regenerate it (see file header)", cmd, subKey)
			continue
		}
		allowed := map[string]bool{}
		for _, f := range allowedList {
			allowed[f] = true
		}
		for _, f := range flags {
			if !allowed[f] {
				t.Errorf("docs/relay-runbook.md's %q uses %q, which the golden fixture's recorded `az %s --help` flags do not include", cmd, f, subKey)
			}
		}
	}
}

// TestRelayRunbookAzureSection_BlastRadiusClaimMatchesRelayConfig guards
// against the exact defect shipped two rounds ago: asserting the blast-radius
// claim is true for both hostnames when it's only true for the one actually
// wired into the browser board's runtime relay config. No external
// dependency (reads only files inside this repo) — always ran, unchanged
// from the prior round.
func TestRelayRunbookAzureSection_BlastRadiusClaimMatchesRelayConfig(t *testing.T) {
	relaysJSON := readDocOrFatalOC(t, "../../web/board/public/relays.json")
	section := azureCertSectionOC(t)

	if !strings.Contains(section, "relays.json") {
		t.Fatalf("docs/relay-runbook.md's Azure cert section no longer cites web/board/public/relays.json for its blast-radius claim — if the claim's source moved, update this test to check the new source")
	}
	if !strings.Contains(relaysJSON, "relay.3dl.network") {
		t.Fatalf("web/board/public/relays.json no longer lists relay.3dl.network — docs/relay-runbook.md's claim that the browser board reaches the relay over its certificate is now UNVERIFIED against this config; update both")
	}
	if strings.Contains(relaysJSON, "relay.moot.pub") {
		t.Errorf("web/board/public/relays.json now lists relay.moot.pub, but docs/relay-runbook.md still claims relay.moot.pub has no observable browser-board blast radius BECAUSE it's absent from this config — update the doc")
	}
}

// TestRegenerateAzureCertFixtureOC refreshes testdata/relay_azure_cert_fixture.json
// from live ground truth: the current doc's §N citations checked against a
// live sibling nostr-relay/docs/prod/deploy.md heading scan, the current
// doc's prod.bicep:N-M citations checked against a live sibling
// nostr-relay/infra/prod.bicep read, and each az subcommand the doc actually
// uses checked against a live `az <subcommand> --help`. Gated behind
// `-regen-azure-cert-fixture` — an OPT-IN flag a human must pass on purpose,
// not a silent skip-on-absence: a plain `go test ./...` (CI's invocation)
// never takes this branch, so it is never mistaken for one of the guards
// above having run. If the tool or repo it needs is missing when the flag
// IS passed, that is a Fatal, not a Skip: the human asked for a refresh and
// didn't get one.
func TestRegenerateAzureCertFixtureOC(t *testing.T) {
	if !*regenAzureCertFixtureOC {
		t.Skip("opt-in only: pass -regen-azure-cert-fixture to refresh testdata/relay_azure_cert_fixture.json from live az/nostr-relay (see file header)")
	}

	if _, err := exec.LookPath("az"); err != nil {
		t.Fatalf("-regen-azure-cert-fixture was passed but `az` is not on PATH: %v", err)
	}
	root, ok := siblingRepoRootOC(t, "nostr-relay")
	if !ok {
		t.Fatalf("-regen-azure-cert-fixture was passed but no sibling nostr-relay checkout was found next to this repo")
	}

	// deploy.md headings.
	deployMdPath := filepath.Join(root, "docs", "prod", "deploy.md")
	deployMdBytes, err := os.ReadFile(deployMdPath)
	if err != nil {
		t.Fatalf("nostr-relay checkout found but %s unreadable: %v", deployMdPath, err)
	}
	headingRe := regexp.MustCompile(`(?m)^## (\d+)\.`)
	var headings []string
	for _, m := range headingRe.FindAllStringSubmatch(string(deployMdBytes), -1) {
		headings = append(headings, m[1])
	}
	if len(headings) == 0 {
		t.Fatalf("found zero numbered '## N.' headings in %s — heading format changed, update this regenerator's parser", deployMdPath)
	}
	sort.Slice(headings, func(i, j int) bool {
		ni, _ := strconv.Atoi(headings[i])
		nj, _ := strconv.Atoi(headings[j])
		return ni < nj
	})

	// bicep citations: capture live content for every range the CURRENT doc
	// cites, and self-check each snippet against the same two known-good
	// anchor patterns the assertion test checks — so a bad citation is
	// caught at regeneration time too, not just silently baked into the
	// fixture.
	bicepPath := filepath.Join(root, "infra", "prod.bicep")
	bicepBytes, err := os.ReadFile(bicepPath)
	if err != nil {
		t.Fatalf("nostr-relay checkout found but %s unreadable: %v", bicepPath, err)
	}
	lines := strings.Split(string(bicepBytes), "\n")
	section := azureCertSectionOC(t)
	bicepCitations := map[string]string{}
	for _, m := range bicepRangeRe.FindAllStringSubmatch(section, -1) {
		start, errS := strconv.Atoi(m[1])
		end, errE := strconv.Atoi(m[2])
		if errS != nil || errE != nil || start < 1 || end > len(lines) || start > end {
			t.Fatalf("docs/relay-runbook.md cites prod.bicep:%s-%s which is out of range for a %d-line live prod.bicep", m[1], m[2], len(lines))
		}
		snippet := strings.Join(lines[start-1:end], "\n")
		switch {
		case strings.Contains(snippet, "resource relayMootPubCert") && strings.Contains(snippet, "resource relay3dlNetworkCert"):
		case strings.Contains(snippet, "relay.dontguess.ai") && strings.Contains(strings.ToLower(snippet), "deliberately"):
		default:
			t.Fatalf("refusing to record prod.bicep:%s-%s into the fixture: live content matches neither expected anchor (managed-cert `existing` resources, or the relay.dontguess.ai exclusion note) — this citation looks wrong, fix docs/relay-runbook.md before regenerating:\n%s", m[1], m[2], snippet)
		}
		bicepCitations[m[1]+"-"+m[2]] = snippet
	}
	if len(bicepCitations) == 0 {
		t.Fatalf("docs/relay-runbook.md's Azure cert section cites zero 'prod.bicep:N-M' line ranges — nothing to record")
	}

	// az subcommand flags: only for subcommands the CURRENT doc actually
	// invokes, from the real CLI.
	azFlags := map[string][]string{}
	for _, cmd := range azCommandsInSectionOC(section) {
		sub, _ := azSubcommandAndFlagsOC(cmd)
		if len(sub) == 0 {
			continue
		}
		key := strings.Join(sub, " ")
		if _, already := azFlags[key]; already {
			continue
		}
		azFlags[key] = azHelpFlagsLiveOC(t, sub)
	}
	if len(azFlags) == 0 {
		t.Fatalf("docs/relay-runbook.md's Azure cert section has zero literal `az ...` commands — nothing to record")
	}

	fx := azureCertFixtureOC{
		DeployMdHeadings:  headings,
		BicepCitations:    bicepCitations,
		AzSubcommandFlags: azFlags,
	}
	out, err := json.MarshalIndent(fx, "", "  ")
	if err != nil {
		t.Fatalf("could not marshal regenerated fixture: %v", err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(azureCertFixturePathOC, out, 0o644); err != nil {
		t.Fatalf("could not write %s: %v", azureCertFixturePathOC, err)
	}
	t.Logf("regenerated %s: %d deploy.md headings, %d bicep citations, %d az subcommands", azureCertFixturePathOC, len(headings), len(bicepCitations), len(azFlags))
}
