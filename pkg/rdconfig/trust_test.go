package rdconfig

import "testing"

// TestTrustSet is the ready-d53 trust-source proof: the read-side allowlist is the
// self pubkey (always trusted) unioned with the configured admitted pubkeys.
func TestTrustSet(t *testing.T) {
	self := "aa" + "00"
	admitted := "bb11"

	t.Run("self always trusted even with empty config", func(t *testing.T) {
		c := &Config{}
		set := c.TrustSet(self)
		if !set[self] {
			t.Errorf("self pubkey %q not trusted", self)
		}
		if len(set) != 1 {
			t.Errorf("trust set = %v, want just {self}", set)
		}
	})

	t.Run("admitted pubkeys unioned with self", func(t *testing.T) {
		c := &Config{TrustedPubkeys: []string{admitted, "", "cc22"}}
		set := c.TrustSet(self)
		if !set[self] || !set[admitted] || !set["cc22"] {
			t.Errorf("trust set missing an expected member: %v", set)
		}
		if set[""] {
			t.Errorf("blank pubkey must not be trusted")
		}
		if len(set) != 3 {
			t.Errorf("trust set = %v, want {self, admitted, cc22}", set)
		}
	})

	t.Run("no self identity yields configured admitted set only", func(t *testing.T) {
		c := &Config{TrustedPubkeys: []string{admitted}}
		set := c.TrustSet("")
		if len(set) != 1 || !set[admitted] {
			t.Errorf("trust set = %v, want {admitted}", set)
		}
	})

	t.Run("returned set is always non-nil", func(t *testing.T) {
		if (&Config{}).TrustSet("") == nil {
			t.Error("TrustSet returned nil; callers rely on non-nil to enforce the gate")
		}
	})
}
