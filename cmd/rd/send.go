package main

import (
	"os"
	"path/filepath"
	"strings"
)

// projectRoot walks up from cwd looking for a legacy .campfire/root file.
// Returns (campfireID, projectDir, true) if found.
//
// CUTOVER (ready-cb6 I7): campfire is no longer a write/read backend and the
// campfire SDK (incl. the naming alias store) is gone. This detector is retained
// only so the remaining vestigial branches can distinguish a legacy campfire
// directory (one still carrying a 64-hex .campfire/root) from a nostr-native (or
// bare) directory; on a nostr-native (or bare) directory it returns ok=false.
func projectRoot() (campfireID string, projectDir string, ok bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", "", false
	}
	for {
		// Legacy .campfire/root detection (no naming resolution — the campfire
		// naming alias store was removed with the SDK).
		rootFile := filepath.Join(dir, ".campfire", "root")
		data, err := os.ReadFile(rootFile)
		if err == nil {
			id := strings.TrimSpace(string(data))
			if len(id) == 64 {
				return id, dir, true
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", "", false
}

// formatCampfireIDForDisplay converts a hex campfire ID to display form.
//
// CUTOVER (ready-cb6 I7): the campfire naming alias store is gone, so there is
// no hex→project-name resolution to perform; the hex ID is returned as-is.
func formatCampfireIDForDisplay(hexID string) string {
	return hexID
}

// minInt returns the smaller of two ints.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
