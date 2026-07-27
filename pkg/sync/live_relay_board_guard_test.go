package sync

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestRequireIsolatedBoardD_BlocksReservedProductionBoard is the ready-fce
// anti-tautology proof for the guard added in live_relay_key_test.go: a
// live-relay test pointed at THIS repo's own live board coordinate ("ready" —
// see .ready/config.json's pinned "board") must FAIL before any write is
// attempted, not silently publish to it.
//
// A t.Fatal inside a subtest always propagates FAIL to its parent — the
// testing package gives no way to catch and swallow that in-process — so
// proving the guard fires (rather than just reading its source) needs the
// guard to run somewhere whose failure doesn't fail THIS test. That's a
// subprocess: the same TestHelperProcess technique os/exec's own tests use.
// This test shells out to `go test` for TestHelperProcess_RequireIsolatedBoardD
// (below) and asserts:
//  1. the subprocess exited NON-ZERO — requireIsolatedBoardD really failed it;
//  2. the failure message names the guard, not some unrelated error;
//  3. the "REACHED_WRITE" marker — printed only if execution continued PAST
//     the guard call, standing in for the publish every live-relay test makes
//     next — never appears, proving the guard blocks BEFORE a write, not after.
//
// No network is used. This runs unconditionally in `go test ./...` (not
// gated behind RD_NOSTR_LIVE_RELAY), so the guard is proven on every CI run.
func TestRequireIsolatedBoardD_BlocksReservedProductionBoard(t *testing.T) {
	cmd := exec.Command("go", "test", "-run", "^TestHelperProcess_RequireIsolatedBoardD$", "-v", ".")
	cmd.Env = append(os.Environ(), "RD_TEST_GUARD_SUBPROCESS=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected the subprocess to FAIL (requireIsolatedBoardD must reject boardD=%q), but it exited 0:\n%s", reservedProductionBoardD, out)
	}
	if !strings.Contains(string(out), "reserved production board D-tag") {
		t.Fatalf("subprocess failed, but not with the expected guard message:\n%s", out)
	}
	if strings.Contains(string(out), "REACHED_WRITE") {
		t.Fatalf("guard did NOT block execution — the write marker printed after the guard call, meaning a publish would have been attempted:\n%s", out)
	}
	t.Logf("PROVEN: requireIsolatedBoardD(t, %q) fails the test before reaching the write:\n%s", reservedProductionBoardD, out)
}

// TestHelperProcess_RequireIsolatedBoardD is not a real test on its own — it
// only runs as the subprocess TestRequireIsolatedBoardD_BlocksReservedProductionBoard
// invokes (guarded by RD_TEST_GUARD_SUBPROCESS so `go test ./...` never runs it
// directly). It calls requireIsolatedBoardD with the reserved production board
// D-tag; if the guard failed to block it, REACHED_WRITE would print, standing
// in for the write that would otherwise follow in a real live-relay test.
func TestHelperProcess_RequireIsolatedBoardD(t *testing.T) {
	if os.Getenv("RD_TEST_GUARD_SUBPROCESS") != "1" {
		t.Skip("only runs as a subprocess of TestRequireIsolatedBoardD_BlocksReservedProductionBoard")
	}
	requireIsolatedBoardD(t, reservedProductionBoardD)
	t.Log("REACHED_WRITE") // must never execute — the guard above must t.Fatal first
}

// TestRequireIsolatedBoardD_AllowsIsolatedBoard is the control case: an
// isolated per-run board D-tag (what liveTestBoardD generates) must pass the
// guard, proving it doesn't just reject everything.
func TestRequireIsolatedBoardD_AllowsIsolatedBoard(t *testing.T) {
	requireIsolatedBoardD(t, "ready-livetest-1234567890") // must NOT fail
}

// TestLiveTestBoardD_NeverReturnsReservedBoard proves liveTestBoardD (the
// helper every live-relay test now uses in place of the literal "ready")
// itself always produces a value that passes the guard.
func TestLiveTestBoardD_NeverReturnsReservedBoard(t *testing.T) {
	d := liveTestBoardD(t)
	if d == reservedProductionBoardD {
		t.Fatalf("liveTestBoardD returned the reserved production board D-tag %q", d)
	}
	requireIsolatedBoardD(t, d) // must not fail
}
