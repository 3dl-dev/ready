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

	// Generate IDs for each template item.
	ids := make([]string, len(t.Items))
	for i := range t.Items {
		id, err := generateItemID(project)
		if err != nil {
			return nil, fmt.Errorf("generating ID for item[%d]: %w", i, err)
		}
		ids[i] = id
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

// generateItemID generates an item ID of the form "<project>-<random-3-chars>".
// The random part is 3 hex characters (from 1.5 random bytes).
// Retries up to 10 times if the result doesn't match the ID pattern.
func generateItemID(project string) (string, error) {
	for range 10 {
		b := make([]byte, 2)
		if _, err := rand.Read(b); err != nil {
			return "", fmt.Errorf("reading random bytes: %w", err)
		}
		suffix := hex.EncodeToString(b)[:3]
		id := project + "-" + suffix
		if itemIDPattern.MatchString(id) {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not generate valid item ID with project %q after 10 attempts", project)
}
