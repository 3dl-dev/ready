// Package playbook implements playbook template parsing, validation, and expansion.
// A playbook is a reusable template that instantiates a set of work items with
// dependency wiring and variable substitution.
package playbook

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/3dl-dev/ready/pkg/declarations"
)

// itemIDPattern is the required pattern for work item IDs.
var itemIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,63}$`)

// labelAtomPattern validates a single label atom.
// Must match the per-atom pattern defined in pkg/state: ^[a-z0-9][a-z0-9-]{0,31}$
var labelAtomPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// maxLabelsPerItem is the maximum number of label atoms permitted on a single
// template item.
const maxLabelsPerItem = 8

// TemplateItem is a single item in a playbook template.
type TemplateItem struct {
	Title    string `json:"title"`
	Type     string `json:"type"`
	Level    string `json:"level,omitempty"`
	Priority string `json:"priority"`
	Context  string `json:"context,omitempty"`
	// Labels is the set of label atoms to attach to the item when engaged.
	// Each atom must match ^[a-z0-9][a-z0-9-]{0,31}$ and at most 8 are allowed.
	// Registry membership is not checked at template-validation time — it is
	// enforced at derive time (pkg/state). Engage emits a UX warning for atoms
	// absent from the target registry.
	Labels []string `json:"labels,omitempty"`
	// Deps is a list of 0-based indices into the template items array.
	// An item at index I with Deps=[J] means item J must be done before item I.
	Deps []int `json:"deps,omitempty"`
}

// PlaybookTemplate is a parsed and validated playbook template.
type PlaybookTemplate struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Items       []TemplateItem `json:"items"`
}

// playbookIDPattern is the required pattern for playbook IDs.
var playbookIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,63}$`)

// Parse parses a PlaybookTemplate from a JSON byte slice (the items array only).
// The id and title are provided separately (from CLI flags / playbook-create message).
func Parse(id, title, description string, itemsJSON []byte) (*PlaybookTemplate, error) {
	if !playbookIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid playbook id %q: must match ^[a-z0-9][a-z0-9-]{2,63}$", id)
	}
	if strings.TrimSpace(title) == "" {
		return nil, fmt.Errorf("title is required")
	}

	var items []TemplateItem
	if err := json.Unmarshal(itemsJSON, &items); err != nil {
		return nil, fmt.Errorf("parsing items JSON: %w", err)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("playbook must have at least one item")
	}

	t := &PlaybookTemplate{
		ID:          id,
		Title:       title,
		Description: description,
		Items:       items,
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return t, nil
}

// ParseFull parses a complete PlaybookTemplate from a JSON byte slice that
// includes all fields (id, title, description, items). Used when reading
// a serialized playbook from a campfire message.
func ParseFull(data []byte) (*PlaybookTemplate, error) {
	var t PlaybookTemplate
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("parsing playbook template: %w", err)
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &t, nil
}

// Validate checks that the template is well-formed.
func (t *PlaybookTemplate) Validate() error {
	if !playbookIDPattern.MatchString(t.ID) {
		return fmt.Errorf("invalid playbook id %q: must match ^[a-z0-9][a-z0-9-]{2,63}$", t.ID)
	}
	if strings.TrimSpace(t.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if len(t.Items) == 0 {
		return fmt.Errorf("playbook must have at least one item")
	}

	// Derive validTypes from the embedded create.json declaration — kills the
	// hardcoded vocabulary duplicate (cascade from ready-a92 design ruling).
	typeValues, err := declarations.ArgEnumValues("create", "type")
	if err != nil {
		return fmt.Errorf("loading type enum from create declaration: %w", err)
	}
	validTypes := make(map[string]bool, len(typeValues))
	for _, v := range typeValues {
		validTypes[v] = true
	}
	validTypesSlice := typeValues // kept for error messages

	validLevels := map[string]bool{"epic": true, "task": true, "subtask": true, "": true}
	validPriorities := map[string]bool{"p0": true, "p1": true, "p2": true, "p3": true}

	for i, item := range t.Items {
		if strings.TrimSpace(item.Title) == "" {
			return fmt.Errorf("item[%d]: title is required", i)
		}
		if !validTypes[item.Type] {
			return fmt.Errorf("item[%d]: invalid type %q: must be one of %s", i, item.Type, strings.Join(validTypesSlice, ", "))
		}
		if !validLevels[item.Level] {
			return fmt.Errorf("item[%d]: invalid level %q: must be one of epic, task, subtask", i, item.Level)
		}
		if !validPriorities[item.Priority] {
			return fmt.Errorf("item[%d]: invalid priority %q: must be one of p0, p1, p2, p3", i, item.Priority)
		}
		// Label validation: per-atom pattern check + max 8 per item.
		// Registry membership is NOT checked here — enforce at derive time (pkg/state).
		if len(item.Labels) > maxLabelsPerItem {
			return fmt.Errorf("item[%d]: too many labels (%d); max is %d", i, len(item.Labels), maxLabelsPerItem)
		}
		for _, atom := range item.Labels {
			if !labelAtomPattern.MatchString(atom) {
				return fmt.Errorf("item[%d]: invalid label %q: must match ^[a-z0-9][a-z0-9-]{0,31}$", i, atom)
			}
		}
		for _, dep := range item.Deps {
			if dep < 0 || dep >= len(t.Items) {
				return fmt.Errorf("item[%d]: dep index %d is out of range (0..%d)", i, dep, len(t.Items)-1)
			}
			if dep == i {
				return fmt.Errorf("item[%d]: self-dependency not allowed", i)
			}
		}
	}

	// Detect cycles via DFS.
	if err := detectCycles(t.Items); err != nil {
		return err
	}

	return nil
}

// detectCycles returns an error if the dep graph contains a cycle.
func detectCycles(items []TemplateItem) error {
	// State: 0=unvisited, 1=in-stack, 2=done
	state := make([]int, len(items))

	var dfs func(i int) error
	dfs = func(i int) error {
		if state[i] == 2 {
			return nil
		}
		if state[i] == 1 {
			return fmt.Errorf("circular dependency detected involving item[%d] %q", i, items[i].Title)
		}
		state[i] = 1
		for _, dep := range items[i].Deps {
			if err := dfs(dep); err != nil {
				return err
			}
		}
		state[i] = 2
		return nil
	}

	for i := range items {
		if err := dfs(i); err != nil {
			return err
		}
	}
	return nil
}

// ItemsJSON returns the items as a JSON byte slice.
func (t *PlaybookTemplate) ItemsJSON() ([]byte, error) {
	return json.Marshal(t.Items)
}
