package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/campfire-net/ready/pkg/declarations"
)

// ValidateEnumFlags validates flag values against the enum constraints of the
// named convention operation declaration. It derives the valid values natively
// from the embedded declaration JSON (pkg/declarations) — no cf-convention parse,
// no hardcoded lists.
//
// flagValues is a map of arg name → supplied string value. Only non-empty values
// are checked; absent/empty flags are left for the write path to reject if they
// are required.
//
// This is the validation entry point designed so that a pre-validation rewrite hook
// (ready-b0c, alias rewriting) can normalise values before this function runs.
// The expected call sequence in create's RunE is:
//
//  1. Parse flags into local variables.
//  2. (ready-b0c hook) rewriteTypeAlias(&itemType, &labels) — rewrites
//     human-friendly aliases to canonical enum values before validation.
//  3. ValidateEnumFlags("create", map[string]string{...}) — rejects unknown values.
//  4. Proceed with the nostr-native write path.
//
// Errors are returned as user-visible messages listing the valid values.
func ValidateEnumFlags(operation string, flagValues map[string]string) error {
	args, err := declarations.EnumArgs(operation)
	if err != nil {
		return fmt.Errorf("loading %q declaration for validation: %w", operation, err)
	}
	var errs []string
	for _, arg := range args {
		val, ok := flagValues[arg.Name]
		if !ok || val == "" {
			// Not supplied; the write path handles required-arg checking.
			continue
		}
		if !enumContains(arg.Values, val) {
			// The alias note is scoped to --type errors only: aliases are a type-level
			// concept (e.g. "bug" → task+label), not a priority or level concept.
			note := ""
			if arg.Name == "type" {
				note = " (note: aliases like \"bug\" are rewritten by rd — check rd help create)"
			}
			errs = append(errs, fmt.Sprintf(
				"--%s %q is not valid; accepted values: %s%s",
				arg.Name, val, formatEnumValues(arg.Values), note,
			))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	return nil
}

// enumContains reports whether val appears in values (exact match).
func enumContains(values []string, val string) bool {
	for _, v := range values {
		if v == val {
			return true
		}
	}
	return false
}

// formatEnumValues formats a slice of enum values for display in error messages.
func formatEnumValues(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	return strings.Join(quoted, ", ")
}
