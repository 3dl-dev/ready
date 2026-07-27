package state

import "testing"

// TestGateProof_DeliberateFailure exists ONLY to prove the required `test`
// status check actually blocks a merge (ready-fe2 done-condition 2).
// It is never merged — the PR carrying it is closed, and this file is
// deleted along with its branch. If you are reading this on main, the gate
// proof leaked and something went wrong.
func TestGateProof_DeliberateFailure(t *testing.T) {
	t.Fatal("deliberate failure proving the required CI check blocks merges (ready-fe2)")
}
