// Live-script run receipts (ready-cc2, epic ready-35a).
//
// M2's load-bearing done conditions are proven by scripts/live-*.mjs against
// wss://relay.3dl.network, and board CI deliberately does not run them — a
// live-network test inside a hermetic suite is a flakiness hazard, and that policy
// is correct. The residual this file closes is that the only record those scripts
// ever passed used to be prose pasted into a commit message, which a reviewer cannot
// distinguish from a recollection.
//
// So each script now writes a structured receipt (scripts/receipt.mjs) and this test
// checks the receipt WITHOUT TOUCHING THE NETWORK. It cannot prove a run happened —
// nothing hermetic can — but it removes every cheap way for a receipt to be wrong:
//
//	commit resolves            a receipt cannot cite a run at a tree that never existed
//	passed == total            a receipt cannot record a failing run as evidence
//	check count == source      a script that quietly STOPPED asserting a clause, or
//	                           gained one, no longer matches its own receipt
//
// THAT LAST CHECK IS THE POINT, and it is deliberately allowed to go red: editing a
// live script's assertion set without re-running it is exactly the drift that made
// the old prose-only record worthless. Re-running the script regenerates the receipt
// and clears it. It is NOT a network gate — it fires on a source edit, offline.
//
// WHAT THIS DELIBERATELY DOES NOT DO: fail when the receipt is old. How far main has
// moved since a run is a judgement for a human reading the PR, and failing every
// change until someone re-runs a network script is the gating board-ci rules out.
// Staleness is REPORTED (t.Logf) so it is visible, never asserted.
package board

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type liveReceipt struct {
	Script     string `json:"script"`
	RanAt      string `json:"ran_at"`
	Commit     string `json:"commit"`
	Dirty      bool   `json:"dirty"`
	Host       string `json:"host"`
	Relay      string `json:"relay"`
	BoardCoord string `json:"board_coord"`
	Mode       string `json:"mode"`
	Checks     []struct {
		Name string `json:"name"`
		OK   bool   `json:"ok"`
	} `json:"checks"`
	Passed int `json:"passed"`
	Total  int `json:"total"`
}

// liveScriptsWithReceipts maps each script that must carry a receipt to the number
// of results it pushes. The count is derived from the source at test time rather
// than hard-coded here, so this table only has to name the scripts.
var liveScriptsWithReceipts = []string{
	"live-write-roundtrip.mjs",
	"live-roundtrip-both-ways.mjs",
}

func TestLiveScriptReceipts_ArePresentWellFormedAndMatchTheirSource(t *testing.T) {
	for _, script := range liveScriptsWithReceipts {
		t.Run(script, func(t *testing.T) {
			name := strings.TrimSuffix(script, ".mjs") + ".json"
			path := filepath.Join("receipts", name)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("no receipt at %s: %v\nRun `node scripts/%s` to produce one. M2's done conditions rest on this script, and without a receipt the only record it ever passed is prose.", path, err, script)
			}
			var r liveReceipt
			if err := json.Unmarshal(raw, &r); err != nil {
				t.Fatalf("%s is not valid JSON: %v", path, err)
			}

			if r.Script != script {
				t.Errorf("receipt names script %q, want %q", r.Script, script)
			}
			for _, f := range []struct{ name, val string }{
				{"ran_at", r.RanAt}, {"commit", r.Commit}, {"host", r.Host},
				{"relay", r.Relay}, {"board_coord", r.BoardCoord},
			} {
				if strings.TrimSpace(f.val) == "" {
					t.Errorf("receipt field %q is empty — a receipt that omits it cannot be checked", f.name)
				}
			}

			// A receipt may only cite a commit that actually exists here. This is what
			// stops a hand-written receipt naming a run at a tree that never was.
			if r.Commit != "" {
				cmd := exec.Command("git", "cat-file", "-e", r.Commit+"^{commit}")
				cmd.Dir = ".."
				if err := cmd.Run(); err != nil {
					t.Errorf("receipt cites commit %s, which does not resolve in this repository — the run it claims has no tree", r.Commit)
				}
			}

			// A failing run is not evidence. Recording one as a receipt would invert
			// the whole point.
			if r.Total == 0 {
				t.Error("receipt records zero checks")
			}
			if r.Passed != r.Total {
				t.Errorf("receipt records %d/%d checks passing — a receipt is only evidence of a PASSING run", r.Passed, r.Total)
			}
			for _, c := range r.Checks {
				if !c.OK {
					t.Errorf("receipt records check %q as failed", c.Name)
				}
			}
			if len(r.Checks) != r.Total {
				t.Errorf("receipt lists %d checks but claims total=%d", len(r.Checks), r.Total)
			}

			// THE DRIFT CHECK. If the script's assertion set changed and nobody re-ran
			// it, the receipt is describing a script that no longer exists.
			src, err := os.ReadFile(filepath.Join("scripts", script))
			if err != nil {
				t.Fatalf("read %s: %v", script, err)
			}
			declared := strings.Count(string(src), "results.push(")
			if declared == 0 {
				t.Fatalf("found no `results.push(` in %s — this test's coupling to the script's assertion set has broken and is no longer checking anything", script)
			}
			if declared != r.Total {
				t.Errorf("%s declares %d checks but its receipt records %d.\nThe script's assertion set changed since the last live run, so the receipt describes a script that no longer exists.\nRe-run `node scripts/%s` to regenerate it.", script, declared, r.Total, script)
			}

			// Staleness is INFORMATION for a reviewer, never a failure — see the file
			// header for why this must not gate.
			if r.Dirty {
				t.Logf("NOTE: receipt was produced on a MODIFIED working tree (dirty=true) at %s — weaker evidence than a run on a clean tree", r.Commit)
			}
			head, err := exec.Command("git", "-C", "..", "rev-parse", "HEAD").Output()
			if err == nil {
				if h := strings.TrimSpace(string(head)); h != r.Commit {
					behind, _ := exec.Command("git", "-C", "..", "rev-list", "--count", r.Commit+"..HEAD").Output()
					t.Logf("NOTE: last live run was at %s, %s commit(s) behind HEAD (%s). Freshness is a reviewer's judgement, not a gate.",
						r.Commit[:min(8, len(r.Commit))], strings.TrimSpace(string(behind)), h[:min(8, len(h))])
				}
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
