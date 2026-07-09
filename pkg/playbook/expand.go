package playbook

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// ExpandedItem is a fully resolved item ready to send as a work:create message.
type ExpandedItem struct {
	// ID is the generated item ID (project-prefixed).
	ID string
	// Title is the template title with variables substituted.
	Title string
	// Type from the template.
	Type string
	// Level from the template.
	Level string
	// Priority from the template.
	Priority string
	// Context with variables substituted.
	Context string
	// Labels are the label atoms to attach to this item.
	// These are the raw template labels — registry membership is enforced at
	// derive time (pkg/state). Engage emits UX warnings for absent atoms.
	Labels []string
	// TemplateIndex is the 0-based index in the template items array.
	TemplateIndex int
	// Deps are the IDs of items that must complete before this one.
	// These map the dep indices to the generated IDs.
	Deps []string
}

// varPattern matches {{variable}} placeholders.
var varPattern = regexp.MustCompile(`\{\{([^}]+)\}\}`)

// Expand instantiates a playbook template into a set of ready-to-create items.
// project is the project prefix for generated IDs (e.g. "myproject").
// variables is a map of template variable substitutions (e.g. {"project": "myproject"}).
func Expand(t *PlaybookTemplate, project string, variables map[string]string) ([]*ExpandedItem, error) {
	if project == "" {
		return nil, fmt.Errorf("project is required for ID generation")
	}

	// Generate IDs for each template item. used tracks IDs already generated
	// within this call so generateItemID can guarantee no duplicates by
	// construction rather than by chance (see ready-d67).
	ids := make([]string, len(t.Items))
	used := make(map[string]bool, len(t.Items))
	for i := range t.Items {
		id, err := generateItemID(project, used)
		if err != nil {
			return nil, fmt.Errorf("generating ID for item[%d]: %w", i, err)
		}
		ids[i] = id
		used[id] = true
	}

	// Build expanded items.
	expanded := make([]*ExpandedItem, len(t.Items))
	for i, item := range t.Items {
		// Resolve dep IDs.
		deps := make([]string, len(item.Deps))
		for j, depIdx := range item.Deps {
			deps[j] = ids[depIdx]
		}

		expanded[i] = &ExpandedItem{
			ID:            ids[i],
			Title:         substitute(item.Title, variables),
			Type:          item.Type,
			Level:         item.Level,
			Priority:      item.Priority,
			Context:       substitute(item.Context, variables),
			Labels:        item.Labels,
			TemplateIndex: i,
			Deps:          deps,
		}
	}

	return expanded, nil
}

// substitute replaces {{variable}} placeholders in s with values from vars.
// Unknown variables are left as-is.
func substitute(s string, vars map[string]string) string {
	if len(vars) == 0 {
		return s
	}
	return varPattern.ReplaceAllStringFunc(s, func(match string) string {
		// Extract variable name from {{name}}.
		name := strings.TrimSpace(match[2 : len(match)-2])
		if val, ok := vars[name]; ok {
			return val
		}
		return match
	})
}

// generateItemID generates an item ID of the form "<project>-<random-hex>".
// used tracks IDs already generated within the current Expand call (fed back
// by the caller after each successful generation). The candidate starts at a
// 3-character hex suffix (matching the historical, common-case id shape) and,
// on collision with used, progressively lengthens the suffix from the same
// random draw until a unique id is found. This mirrors cmd/rd's generateID
// contract (see ready-e7c) and gives generateItemID an actual, deterministic
// collision-avoidance guarantee instead of drawing a fixed 3-char suffix and
// hoping it never collides across calls within the same Expand — which is
// what caused TestExpand_DuplicateIDGeneration to flake under full-suite load
// (ready-d67).
func generateItemID(project string, used map[string]bool) (string, error) {
	b := make([]byte, 8) // 16 hex chars — enough headroom for extension
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("reading random bytes: %w", err)
	}
	full := hex.EncodeToString(b)
	for length := 3; length <= len(full); length++ {
		candidate := project + "-" + full[:length]
		if used[candidate] {
			continue
		}
		if !itemIDPattern.MatchString(candidate) {
			continue
		}
		return candidate, nil
	}
	return "", fmt.Errorf("could not generate unique item ID with project %q after exhausting %d-char suffix", project, len(full))
}
