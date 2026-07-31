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
//     the REAL az CLI accepts for that exact subcommand — this is checked
//     against `az --help`'s own ground truth, not deploy.md's prose, because
//     the specific defect this guards (`--environment` on `az containerapp
//     env certificate list`) is a subcommand deploy.md never mentions at
//     all, so a deploy.md-text-only diff has nothing to compare against for
//     it:
//     TestRelayRunbookAzureSection_AzFlagsAreValidForTheirSubcommand
//   - the "relay.moot.pub has no observable browser blast radius today"
//     claim is checked against the actual runtime relay config the browser
//     board loads, not asserted:
//     TestRelayRunbookAzureSection_BlastRadiusClaimMatchesRelayConfig
//
// The first three need a sibling `nostr-relay` checkout and/or the `az` CLI
// on PATH, neither of which CI's checkout provides (go-test.yml checks out
// only this repo and installs no cloud CLI) — same self-skip-when-absent
// shape as the pre-existing RD_NOSTR_LIVE_RELAY-gated tests elsewhere in
// this package: CI runs the hermetic suite as-is, this does not broaden any
// skip condition to force green, and does not narrow one either. On a
// machine with both available (this one, per the adversary's own framing),
// every one of these runs for real. The fourth has no external dependency
// and always runs.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

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
// Returns ("", false) if it can't be found, so callers can skip.
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
// against citing a `§N` that doesn't exist (or, as shipped last round,
// exists but is the wrong section for the domain being discussed at that
// point — this at least catches the number going stale or typo'd; the
// domain-match is enforced by the bicep-citation test and human review of
// the surrounding prose).
func TestRelayRunbookAzureSection_SectionReferencesExistInSiblingRepo(t *testing.T) {
	root, ok := siblingRepoRootOC(t, "nostr-relay")
	if !ok {
		t.Skip("sibling nostr-relay checkout not found next to this repo; skipping cross-repo heading check (see file header)")
	}
	deployMdPath := filepath.Join(root, "docs", "prod", "deploy.md")
	deployMdBytes, err := os.ReadFile(deployMdPath)
	if err != nil {
		t.Skipf("nostr-relay checkout found but %s unreadable: %v", deployMdPath, err)
	}
	deployMd := string(deployMdBytes)

	headingRe := regexp.MustCompile(`(?m)^## (\d+)\.`)
	existing := map[string]bool{}
	for _, m := range headingRe.FindAllStringSubmatch(deployMd, -1) {
		existing[m[1]] = true
	}
	if len(existing) == 0 {
		t.Fatalf("found zero numbered '## N.' headings in %s — heading format changed, update this test's parser", deployMdPath)
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
		t.Errorf("docs/relay-runbook.md cites %s but nostr-relay/docs/prod/deploy.md has no matching '## N.' heading", strings.Join(bad, ", "))
	}
}

var bicepRangeRe = regexp.MustCompile(`prod\.bicep:(\d+)-(\d+)`)

// TestRelayRunbookAzureSection_BicepLineCitationsAccurate guards against the
// exact defect shipped last round: a line-range citation that lands on the
// wrong code (`330-370` was inside the containerApp/ingress resource, not
// the managedCertificates `existing` references it was cited for).
func TestRelayRunbookAzureSection_BicepLineCitationsAccurate(t *testing.T) {
	root, ok := siblingRepoRootOC(t, "nostr-relay")
	if !ok {
		t.Skip("sibling nostr-relay checkout not found next to this repo; skipping bicep line-citation check (see file header)")
	}
	bicepPath := filepath.Join(root, "infra", "prod.bicep")
	bicepBytes, err := os.ReadFile(bicepPath)
	if err != nil {
		t.Skipf("nostr-relay checkout found but %s unreadable: %v", bicepPath, err)
	}
	lines := strings.Split(string(bicepBytes), "\n")

	section := azureCertSectionOC(t)
	matches := bicepRangeRe.FindAllStringSubmatch(section, -1)
	if len(matches) == 0 {
		t.Fatalf("docs/relay-runbook.md's Azure cert section cites zero 'prod.bicep:N-M' line ranges")
	}
	for _, m := range matches {
		start, errS := strconv.Atoi(m[1])
		end, errE := strconv.Atoi(m[2])
		if errS != nil || errE != nil || start < 1 || end > len(lines) || start > end {
			t.Errorf("docs/relay-runbook.md cites prod.bicep:%s-%s which is out of range for a %d-line file", m[1], m[2], len(lines))
			continue
		}
		snippet := strings.Join(lines[start-1:end], "\n")
		switch {
		// Require the actual `resource <name> ... existing = {` DECLARATION,
		// not merely a reference to the name (e.g. `certificateId:
		// relayMootPubCert.id` inside containerApp/ingress.customDomains,
		// which is what the wrong `330-370` citation landed on last round —
		// a bare substring match on the name alone would have passed that
		// too, since the name is mentioned there as well).
		case strings.Contains(snippet, "resource relayMootPubCert") && strings.Contains(snippet, "resource relay3dlNetworkCert"):
			// the managed-certificate `existing` resource declarations — ok.
		case strings.Contains(snippet, "relay.dontguess.ai") && strings.Contains(strings.ToLower(snippet), "deliberately"):
			// the dontguess.ai exclusion-rationale citation — ok.
		default:
			t.Errorf("docs/relay-runbook.md cites prod.bicep:%d-%d but that range contains neither expected anchor (managed-cert `existing` resources, or the relay.dontguess.ai exclusion note) — got:\n%s", start, end, snippet)
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

// azHelpFlagsOC returns every flag `az <subcommand> --help` documents
// (short and long forms, from both "Arguments" and "Global Arguments").
func azHelpFlagsOC(t *testing.T, subcommand []string) map[string]bool {
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
	return flags
}

// TestRelayRunbookAzureSection_AzFlagsAreValidForTheirSubcommand guards
// against the exact defect shipped last round: `az containerapp env
// certificate list ... --environment ...` does not run because that
// subcommand's environment flag is `-n/--name`, not `--environment`.
func TestRelayRunbookAzureSection_AzFlagsAreValidForTheirSubcommand(t *testing.T) {
	if _, err := exec.LookPath("az"); err != nil {
		t.Skip("az CLI not on PATH; skipping live flag-validity check (see file header)")
	}
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
		allowed := azHelpFlagsOC(t, sub)
		for _, f := range flags {
			if !allowed[f] {
				t.Errorf("docs/relay-runbook.md's %q uses %q, which `az %s --help` does not list as a valid flag for that subcommand", cmd, f, strings.Join(sub, " "))
			}
		}
	}
}

// TestRelayRunbookAzureSection_BlastRadiusClaimMatchesRelayConfig guards
// against the exact defect shipped last round: asserting the blast-radius
// claim is true for both hostnames when it's only true for the one actually
// wired into the browser board's runtime relay config.
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
