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

// argDescriptor mirrors the minimal fields needed to extract enum values
// from a convention:operation declaration arg.
type argDescriptor struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	Values []string `json:"values,omitempty"`
}

// declarationArgs is a minimal parse of a declaration JSON — only what is
// needed to extract arg enum values.
type declarationArgs struct {
	Args []argDescriptor `json:"args"`
}

// ArgEnumValues returns the enum values for the named arg in the named
// operation declaration. Returns nil if the operation or arg is not found,
// or if the arg is not of type "enum".
// The operation name follows the same convention as Load (e.g. "create").
func ArgEnumValues(operation, argName string) ([]string, error) {
	data, err := Load(operation)
	if err != nil {
		return nil, err
	}
	var decl declarationArgs
	if err := json.Unmarshal(data, &decl); err != nil {
		return nil, fmt.Errorf("parsing declaration %q: %w", operation, err)
	}
	for _, arg := range decl.Args {
		if arg.Name == argName {
			if arg.Type != "enum" {
				return nil, fmt.Errorf("arg %q in %q is not an enum (type=%q)", argName, operation, arg.Type)
			}
			return arg.Values, nil
		}
	}
	return nil, fmt.Errorf("arg %q not found in declaration %q", argName, operation)
}

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
