package main

import (
	"fmt"
	"os"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
)

// warnIfIdentityInconsistent is the startup tripwire: it compares the pubkey
// recorded in the key file against the pubkey derived from the loaded secret. A
// mismatch means the file was tampered with or its secret was swapped without
// rewriting pubkey_hex (a botched / regenerated identity). Non-fatal warning —
// the derived key is authoritative — but loud, so a botched identity is visible.
func warnIfIdentityInconsistent(keyPath string, k *nostr.Key) {
	stored, err := nostr.StoredPubKeyHex(keyPath)
	if err != nil || stored == "" {
		return
	}
	if stored != k.PubKeyHex() {
		fmt.Fprintf(os.Stderr,
			"warning: nostr identity at %q is INCONSISTENT — file records pubkey %s but the secret derives %s; the key file may be corrupt or regenerated\n",
			keyPath, stored, k.PubKeyHex())
	}
}

// loadRDConfig loads rd.json from $RD_HOME. Never errors — an unreadable/absent
// config degrades to an empty Config (self-only trust), the safe default.
func loadRDConfig() *rdconfig.Config {
	if cfg, err := rdconfig.Load(RDHome()); err == nil {
		return cfg
	}
	return &rdconfig.Config{}
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}
