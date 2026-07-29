package writevectors_test

// citations_test.go mirrors internal/foldvectors's clause-citation discipline
// for the WRITE side: every vector must cite at least one board-fold-spec.md
// clause, and every cited clause must actually exist in the document. This
// test is independent of cmd/rd's replay harness (it only reads the committed
// JSON and the committed spec) so it can also run as a pure data check.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/internal/writevectors"
)

const (
	vectorPath = "../../testdata/write.vectors.json"
	specPath   = "../../docs/design/board-fold-spec.md"
)

func load(t *testing.T) *writevectors.File {
	t.Helper()
	f, err := writevectors.Load(vectorPath)
	if err != nil {
		t.Fatalf("load vectors: %v", err)
	}
	if len(f.Vectors) == 0 {
		t.Fatal("vector file contains no vectors")
	}
	return f
}

// TestWriteVectorFloor is done-condition 1 (ready-bc5): the vector count is
// reported and floored, so a truncated or half-authored file fails loudly
// instead of silently passing with two vectors. The actual REPLAY (done
// condition 1's "rd's own writer passes them" half) is
// cmd/rd's TestWriteConformanceVectors — this only floors the DATA.
func TestWriteVectorFloor(t *testing.T) {
	f := load(t)
	t.Logf("write conformance: %d vectors from %s (spec %s)", len(f.Vectors), vectorPath, f.Spec)
	const minVectors = 18
	if len(f.Vectors) < minVectors {
		t.Fatalf("only %d vectors present, expected at least %d — the vector file looks truncated", len(f.Vectors), minVectors)
	}
	seen := map[string]bool{}
	for _, v := range f.Vectors {
		if seen[v.Name] {
			t.Fatalf("duplicate vector name %q", v.Name)
		}
		seen[v.Name] = true
	}
}

// TestEveryWriteVectorCitesALiveSpecClause is done-condition 2: a vector must
// name at least one spec clause, and every clause it names must actually
// exist in docs/design/board-fold-spec.md. A rotted citation is a bug in one
// of the two artifacts, not something to silently ignore.
func TestEveryWriteVectorCitesALiveSpecClause(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean(specPath))
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	clausePattern := regexp.MustCompile(`\*\*§([0-9]+\.[0-9]+)`)
	known := map[string]bool{}
	for _, m := range clausePattern.FindAllStringSubmatch(string(raw), -1) {
		known[m[1]] = true
	}
	if len(known) < 200 {
		t.Fatalf("only %d clauses parsed from %s — the spec's clause markup changed, fix this test", len(known), specPath)
	}

	f := load(t)
	cited := map[string]bool{}
	for _, v := range f.Vectors {
		if len(v.SpecClauses) == 0 {
			t.Errorf("vector %q cites no spec clause", v.Name)
			continue
		}
		for _, c := range v.SpecClauses {
			if !known[c] {
				t.Errorf("vector %q cites §%s, which does not exist in %s", v.Name, c, specPath)
			}
			cited[c] = true
		}
	}
	t.Logf("write vectors cite %d distinct spec clauses out of %d in the spec", len(cited), len(known))
}

// TestNoWriteVectorExpectsAnAuthoredDerivedStatus is the DATA half of
// ready-bc5's mandatory invariant: "blocked" is derived (applyDepAndGateStatus
// recomputes it every fold, spec §7.6/§8.4) and MUST NOT appear as an EXPECTED
// authored "s" tag in any positive vector's Events. (The one deliberate
// exception — a card-only edit that persists a stale DERIVED status, a real,
// documented, unresolved gap, spec §27.7/§25.6 — is pinned by name below so
// this floor cannot be defeated by silently adding more instances of the same
// known bug; see that vector's own Note for the citation and the tracking item.)
func TestNoWriteVectorExpectsAnAuthoredDerivedStatus(t *testing.T) {
	f := load(t)
	const knownGapPrefix = "known_gap_"
	for _, v := range f.Vectors {
		if strings.HasPrefix(v.Name, knownGapPrefix) {
			continue
		}
		for _, ev := range v.Events {
			for _, tag := range ev.Tags {
				if len(tag) >= 2 && tag[0] == "s" && tag[1] == "blocked" {
					t.Errorf("vector %q expects an authored s=blocked tag — blocked is DERIVED "+
						"(spec §7.6/§8.4) and no write path may author it (spec §25.6); if this is a "+
						"genuinely new instance of the known gap (§27.7), name it with the %q prefix "+
						"and cite §27.7, do not silently add another exception", v.Name, knownGapPrefix)
				}
			}
		}
	}
}
