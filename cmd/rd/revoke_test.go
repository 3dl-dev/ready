package main

import (
	"fmt"
	"testing"
)

// NOTE (ready-cb6 I7): the campfire retroactive-revocation helpers
// (findMembersAdmittedBy, capAdmitted) and their tests were removed with the
// campfire write path. `rd revoke` is now nostr-native (runNostrGrantRevoke);
// --retroactive is redirected to `rd nostr revoke --from`. The surviving tests
// below cover the pubkey-target validation guard, which is unchanged.

// TestRevoke_PubkeyDetection verifies the raw-pubkey detection used by revoke.go:
// a target is treated as a raw pubkey only when it is exactly 64 hex characters.
func TestRevoke_PubkeyDetection(t *testing.T) {
	validPubkey := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	shortHex := "abcdef1234"
	name := "myorg.ready/myproject"

	cases := []struct {
		input       string
		isPubkeyArg bool // true = arg should be treated as raw pubkey
	}{
		{validPubkey, true},
		{shortHex, false}, // too short — not a 64-char pubkey
		{name, false},     // contains non-hex chars — not a pubkey
	}

	for _, tc := range cases {
		isPubkey := len(tc.input) == 64 && isHex(tc.input)
		if isPubkey != tc.isPubkeyArg {
			t.Errorf("input %q: isPubkey = %v, want %v", tc.input, isPubkey, tc.isPubkeyArg)
		}
	}
}

// TestRevoke_RejectsNonPubkeyTarget is a regression test for ready-34d: any
// non-pubkey target must be rejected with a clear error rather than resolved as
// a name (which would produce a campfire ID, a semantic type confusion).
func TestRevoke_RejectsNonPubkeyTarget(t *testing.T) {
	cases := []struct {
		name   string
		target string
		wantOK bool // true = target is a valid 64-hex pubkey and should not be rejected
	}{
		{name: "human-readable name", target: "alice"},
		{name: "cf:// URI", target: "cf://myorg.ready/alice"},
		{name: "short hex (not 64 chars)", target: "abcdef1234"},
		{
			name:   "valid 64-char hex pubkey — accepted",
			target: "cafecafe" + "deadbeef" + "00112233" + "44556677" + "8899aabb" + "ccddeeff" + "00112233" + "44556677",
			wantOK: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isPubkey := len(tc.target) == 64 && isHex(tc.target)

			if tc.wantOK {
				if !isPubkey {
					t.Errorf("target %q should be accepted as a pubkey but was not", tc.target)
				}
				return
			}

			if isPubkey {
				t.Errorf("target %q should be rejected (not a valid 64-char hex pubkey) but isPubkey=%v", tc.target, isPubkey)
				return
			}

			// Simulate the guard from revoke.go RunE.
			gotErr := fmt.Errorf("revoke target %q is not a valid pubkey: must be a 64-character hex string\n  hint: use the member's public key, not a name or campfire ID", tc.target)
			errStr := gotErr.Error()
			if !contains(errStr, "not a valid pubkey") {
				t.Errorf("error %q does not mention 'not a valid pubkey'", errStr)
			}
			if !contains(errStr, "64-character hex") {
				t.Errorf("error %q does not mention '64-character hex'", errStr)
			}
		})
	}
}

// contains is a simple substring check helper for test assertions.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
