// Package declarations embeds the work management convention:operation JSON
// declarations and provides functions to post them to a campfire.
package declarations

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
)

//go:embed ops/*.json
var opsFS embed.FS

//go:embed seed_labels.json
var seedLabelsData []byte

// SeedLabel is a label atom that is implicitly defined in every campfire.
type SeedLabel struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// LoadSeedLabels returns the built-in seed label atoms.
func LoadSeedLabels() ([]SeedLabel, error) {
	var labels []SeedLabel
	if err := json.Unmarshal(seedLabelsData, &labels); err != nil {
		return nil, fmt.Errorf("parsing seed_labels.json: %w", err)
	}
	return labels, nil
}

// All returns all convention:operation declaration payloads as raw JSON.
func All() ([][]byte, error) {
	entries, err := fs.ReadDir(opsFS, "ops")
	if err != nil {
		return nil, fmt.Errorf("reading embedded ops: %w", err)
	}
	var payloads [][]byte
	for _, e := range entries {
		data, err := opsFS.ReadFile("ops/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", e.Name(), err)
		}
		payloads = append(payloads, data)
	}
	return payloads, nil
}

// Load returns the raw JSON for a single named declaration.
// The name corresponds to the filename without the .json extension (e.g. "create", "claim").
// Hyphens in operation names map to underscores in filenames (e.g. "gate-resolve" → "gate_resolve.json").
func Load(name string) ([]byte, error) {
	// Normalize: hyphens in operation names map to underscores in filenames.
	filename := strings.ReplaceAll(name, "-", "_") + ".json"
	data, err := opsFS.ReadFile("ops/" + filename)
	if err != nil {
		return nil, fmt.Errorf("loading declaration %q: %w", name, err)
	}
	return data, nil
}
