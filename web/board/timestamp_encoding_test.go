package board

import (
	"os/exec"
	"testing"
)

// TestNodeVerifiesTimestampEncoding is ready-414 review finding #6: every
// other proof that an independent JS/TS client can recover
// expect.items[].created_at/updated_at exactly (internal/foldvectors's Go
// tests) is a Go-side PROXY for that claim — Go's float64 and JavaScript's
// Number are both IEEE-754 doubles, so the precision reasoning transfers, but
// nothing actually ran a JS engine over the committed file. This test does:
// it shells out to `node` (already a hard CI/dev requirement — see
// dist_test.go's buildDist, which npm-builds this same package) and runs
// verify-timestamp-encoding.mjs against the real, committed
// testdata/fold.vectors.json. No vitest or other JS test runner is
// introduced; this reuses the node/npm toolchain web/board already requires,
// via the same exec.Command pattern dist_test.go uses.
//
// Fails loudly (not t.Skip) if node is unavailable, exactly like
// dist_test.go's npm check — CI's actions/setup-node step (web/.nvmrc)
// guarantees node is on PATH.
func TestNodeVerifiesTimestampEncoding(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatalf("node not found on PATH — required to verify the vector file's timestamp encoding "+
			"against a real JS engine: %v", err)
	}
	cmd := exec.Command("node", "verify-timestamp-encoding.mjs", "../../testdata/fold.vectors.json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node verify-timestamp-encoding.mjs failed: %v\n%s", err, out)
	}
	t.Log(string(out))
}
