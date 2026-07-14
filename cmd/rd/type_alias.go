package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/3dl-dev/ready/pkg/declarations"
)

// rewriteTypeAlias checks whether itemType matches a known type alias and, if so,
// rewrites it in-place (updating *itemType and appending any alias labels to *labelSlice,
// deduplicating). It prints a one-line notice to stderr when a rewrite occurs.
//
// This must be called after flag parsing but before ValidateEnumFlags so that
// aliased input (e.g. "bug") validates cleanly as "task" with label "bug".
//
// Returns false (no rewrite) and no error when the type is not an alias.
// Returns true (rewritten) when the alias is applied.
func rewriteTypeAlias(itemType *string, labelSlice *[]string) (bool, error) {
	aliases, err := declarations.LoadTypeAliases()
	if err != nil {
		// Non-fatal: alias loading failure should not break create.
		return false, nil
	}
	alias, ok := aliases[*itemType]
	if !ok {
		return false, nil
	}

	// Apply rewrite.
	original := *itemType
	*itemType = alias.Type

	// Append alias labels, deduplicating against existing labels.
	existing := make(map[string]struct{}, len(*labelSlice))
	for _, l := range *labelSlice {
		existing[l] = struct{}{}
	}
	for _, l := range alias.Labels {
		if _, dup := existing[l]; !dup {
			*labelSlice = append(*labelSlice, l)
			existing[l] = struct{}{}
		}
	}

	// Notice to stderr: stdout stays pipe-clean.
	fmt.Fprintf(os.Stderr, "notice: --type %s → --type %s --label %s (ready-a92)\n",
		original, alias.Type, joinLabels(alias.Labels))

	return true, nil
}

// joinLabels joins label atoms for display in the alias notice.
func joinLabels(labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	if len(labels) == 1 {
		return labels[0]
	}
	result := labels[0]
	for _, l := range labels[1:] {
		result += " --label " + l
	}
	return result
}

// labelDemandRecord is one entry appended to .ready/label-demand.jsonl.
type labelDemandRecord struct {
	Label       string `json:"label"`
	AttemptedAt string `json:"attempted_at"`
	By          string `json:"by"`
}

// appendLabelDemand appends a demand record to .ready/label-demand.jsonl in the
// ready project directory. The call is best-effort: if the project root cannot
// be found or the write fails, no error is returned (demand is data, not critical).
func appendLabelDemand(label, byKey string) {
	projectDir, ok := readyProjectDir()
	if !ok {
		return
	}
	readyDir := filepath.Join(projectDir, ".ready")
	demandFile := filepath.Join(readyDir, "label-demand.jsonl")

	rec := labelDemandRecord{
		Label:       label,
		AttemptedAt: time.Now().UTC().Format(time.RFC3339),
		By:          byKey,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	data = append(data, '\n')

	f, err := os.OpenFile(demandFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(data)
}
