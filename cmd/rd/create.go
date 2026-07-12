package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/ready/pkg/state"
	"github.com/campfire-net/ready/pkg/timeparse"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

var nonAlphanumHyphen = regexp.MustCompile(`[^a-z0-9]+`)

// projectPrefix returns the sanitized folder name of the project directory
// as an ID prefix (e.g. "/home/baron/projects/ready" → "ready").
func projectPrefix(projectDir string) string {
	name := filepath.Base(projectDir)
	name = nonAlphanumHyphen.ReplaceAllString(name, "")
	if len(name) >= 2 {
		return name
	}
	return ""
}

// generateID returns the shortest ID that doesn't collide with any existing
// item ID, with a minimum of 3 hex characters. If a prefix is provided it is
// prepended as "<prefix>-<hex>".
func generateID(prefix string, existingIDs map[string]struct{}) (string, error) {
	b := make([]byte, 8) // 16 hex chars — enough headroom
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating id: %w", err)
	}
	full := hex.EncodeToString(b)
	for length := 3; length <= len(full); length++ {
		var candidate string
		if prefix != "" {
			candidate = prefix + "-" + full[:length]
		} else {
			candidate = full[:length]
		}
		if _, collision := existingIDs[candidate]; !collision {
			return candidate, nil
		}
	}
	if prefix != "" {
		return prefix + "-" + full, nil
	}
	return full, nil
}

// createPayload is the JSON payload for a work:create message.
// Retained for scanning existing create messages in state loading.
type createPayload struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Context  string `json:"context,omitempty"`
	Type     string `json:"type"`
	Level    string `json:"level,omitempty"`
	Project  string `json:"project,omitempty"`
	For      string `json:"for"`
	By       string `json:"by,omitempty"`
	Priority string `json:"priority"`
	ParentID string `json:"parent_id,omitempty"`
	ETA      string `json:"eta,omitempty"`
	Due      string `json:"due,omitempty"`
}

var createCmd = &cobra.Command{
	Use:   "create [title]",
	Short: "Create a new work item",
	Long: `Create a new work item in the project campfire.

Title can be a positional argument or --title flag.
Required: title, --type, --priority

If --eta is omitted, it is derived from priority:
  p0 = now, p1 = +4h, p2 = +24h, p3 = +72h

Example:
  rd create "Fix auth bug" --type task --priority p0
  rd create --title "Fix auth bug" --type task --priority p0
  rd create "Review API design" --type decision --priority p1 --for baron@3dl.dev
  rd create "Ship v2" --type task --priority p1 --context "See spec in docs/v2.md" --json

Note: use --context for descriptions, not --description.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Helpful redirect for --description (agents may use bd's flag name).
		if desc, _ := cmd.Flags().GetString("description"); desc != "" {
			return fmt.Errorf("--description is not a flag on rd create. The field is called 'context' in Ready. Use --context or set context after creation with rd update")
		}

		id, _ := cmd.Flags().GetString("id")
		title, _ := cmd.Flags().GetString("title")
		context, _ := cmd.Flags().GetString("context")
		itemType, _ := cmd.Flags().GetString("type")
		level, _ := cmd.Flags().GetString("level")
		project, _ := cmd.Flags().GetString("project")
		forParty, _ := cmd.Flags().GetString("for")
		by, _ := cmd.Flags().GetString("by")
		priority, _ := cmd.Flags().GetString("priority")
		parentID, _ := cmd.Flags().GetString("parent-id")
		eta, _ := cmd.Flags().GetString("eta")
		due, _ := cmd.Flags().GetString("due")
		labelSlice, _ := cmd.Flags().GetStringArray("label")

		// Title: positional arg or --title flag, not both.
		if len(args) > 0 && title != "" {
			return fmt.Errorf("title provided as both positional argument and --title flag; use one or the other")
		}
		if len(args) > 0 {
			title = strings.Join(args, " ")
		}

		// Validation.
		if title == "" {
			return fmt.Errorf("title is required (positional argument or --title flag)")
		}
		if itemType == "" {
			return fmt.Errorf("--type is required")
		}
		if priority == "" {
			return fmt.Errorf("--priority is required")
		}

		// Alias rewrite: runs BEFORE enum validation so aliased types (e.g. "bug")
		// are rewritten to canonical values (e.g. "task" + label "bug") before the
		// declaration enum check sees them.
		// Call sequence: parse flags → [alias rewrite] → ValidateEnumFlags → withAgentAndStore.
		if _, err := rewriteTypeAlias(&itemType, &labelSlice); err != nil {
			return err
		}

		// Declaration-derived enum validation — runs BEFORE any store open or
		// JSONL write. Alias rewrite has already normalized the type above.
		{
			decl, err := loadDeclaration("create")
			if err != nil {
				return fmt.Errorf("loading create declaration for validation: %w", err)
			}
			enumFlags := map[string]string{
				"type":     itemType,
				"priority": priority,
			}
			if level != "" {
				enumFlags["level"] = level
			}
			if err := ValidateEnumFlags(decl, enumFlags); err != nil {
				return err
			}
		}

		// Normalize ETA to UTC if provided.
		if eta != "" {
			normalized, err := timeparse.Parse(eta, time.Now())
			if err != nil {
				return fmt.Errorf("invalid --eta: %w", err)
			}
			eta = normalized
		}
		// Normalize due to UTC if provided.
		if due != "" {
			normalized, err := timeparse.Parse(due, time.Now())
			if err != nil {
				return fmt.Errorf("invalid --due: %w", err)
			}
			due = normalized
		}

		// nostr-native default write path (ready-6ef): no .cf, secp256k1 signer.
		// The item is materialized as board+card+status(inbox) events; --for
		// defaults to the signing pubkey.
		if dir, native := nostrNativeProject(); native {
			id, err := runCreateNostr(dir, nostrCreateSpec{
				id: id, title: title, context: context, itemType: itemType,
				level: level, project: project, forParty: forParty, by: by,
				priority: priority, parentID: parentID, eta: eta, due: due,
				labels: labelSlice, forChanged: cmd.Flags().Changed("for"),
			})
			if err != nil {
				return err
			}
			if jsonOutput {
				out := map[string]interface{}{
					"id": id, "title": title, "type": itemType, "priority": priority,
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			if isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd()) {
				fmt.Printf("created %s\n", id)
			} else {
				fmt.Println(id)
			}
			return nil
		}

		return withAgentAndStore(func(agentID *identity.Identity, s store.Store) error {
			// Default --for to the current session identity when not explicitly set.
			if !cmd.Flags().Changed("for") {
				forParty = agentID.PublicKeyHex()
			} else if forParty == "" {
				return fmt.Errorf("--for: value cannot be empty")
			}

			// Client-side label validation against the registry (when labels are provided).
			// Registry is derived from the campfire store or JSONL; falls back gracefully
			// when unavailable (fresh checkout, offline) per spec: never hard-fail on missing registry.
			if len(labelSlice) > 0 {
				var registry map[string]state.LabelDef
				campfireID, _, hasCampfire := projectRoot()
				if hasCampfire && campfireID != "" {
					if result, err := state.DeriveAllFromStore(s, campfireID); err == nil {
						registry = result.LabelRegistry()
					}
				} else if path := jsonlPath(); path != "" {
					// JSONL-only mode: load registry from mutations file.
					campfireID, _, _ := projectRoot()
					if result, err := state.DeriveAllFromJSONL(path, campfireID); err == nil {
						registry = result.LabelRegistry()
					}
				}
				if err := validateLabelsAgainstRegistry(labelSlice, registry, agentID.PublicKeyHex()); err != nil {
					return err
				}
			}

			// Load existing IDs for collision detection.
			existingIDs := map[string]struct{}{}
			existingItems, _ := allItemsFromJSONLOrStore(s)
			for _, it := range existingItems {
				existingIDs[it.ID] = struct{}{}
			}

			// Determine ID prefix from project directory.
			_, projectDir, hasCampfire := projectRoot()
			if id == "" {
				prefix := ""
				if hasCampfire {
					prefix = projectPrefix(projectDir)
				} else if dir, ok := readyProjectDir(); ok {
					prefix = projectPrefix(dir)
				}
				generated, err := generateID(prefix, existingIDs)
				if err != nil {
					return err
				}
				id = generated
			} else if _, collision := existingIDs[id]; collision {
				return fmt.Errorf("item %q already exists", id)
			}

			exec, _, err := requireExecutor()
			if err != nil {
				return err
			}
			decl, err := loadDeclaration("create")
			if err != nil {
				return err
			}

			argsMap := map[string]any{
				"id":       id,
				"title":    title,
				"type":     itemType,
				"for":      forParty,
				"priority": priority,
			}
			if context != "" {
				argsMap["context"] = context
			}
			if level != "" {
				argsMap["level"] = level
			}
			if project != "" {
				argsMap["project"] = project
			}
			if by != "" {
				argsMap["by"] = by
			}
			if parentID != "" {
				argsMap["parent_id"] = parentID
			}
			if eta != "" {
				argsMap["eta"] = eta
			}
			if due != "" {
				argsMap["due"] = due
			}
			if len(labelSlice) > 0 {
				// Join multiple --label flags into the comma-scalar format expected by the declaration.
				argsMap["labels"] = strings.Join(labelSlice, ",")
			}

			msg, campfireID, err := executeConventionOp(agentID, s, exec, decl, argsMap)
			if err != nil {
				return err
			}

			// rd->nostr hybrid publish (ready-a13). Gated by RD_NOSTR=1 so the
			// campfire baseline is unaffected. Best-effort: a relay failure never
			// fails create — the events are durable in the local authoritative log.
			if nostrErr := publishItemCreateNostr(id, title, itemType, priority, "", context, forParty); nostrErr != nil {
				warnNostrPublishFailure("item created; local log/campfire durable", nostrErr)
			}

			if jsonOutput {
				out := map[string]interface{}{
					"id":          id,
					"msg_id":      msg.ID,
					"campfire_id": campfireID,
					"title":       title,
					"type":        itemType,
					"priority":    priority,
					"for":         forParty,
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			// Pipe-friendly output: print bare ID when stdout is not a TTY so
			// scripts can do: ITEM=$(rd create 'Title' --type task --priority p1)
			if isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd()) {
				fmt.Printf("created %s (msg: %s)\n", id, msg.ID)
			} else {
				fmt.Println(id)
			}
			return nil
		})
	},
}

func init() {
	createCmd.Flags().String("id", "", "item ID (default: auto-generated)")
	createCmd.Flags().String("title", "", "short title (required)")
	createCmd.Flags().String("context", "", "full context / description")
	createCmd.Flags().String("type", "", "type: task, decision, review, reminder, deadline, prep, message, directive (required)")
	createCmd.Flags().String("for", "", "who needs this outcome (default: current identity)")
	createCmd.Flags().String("priority", "", "priority: p0, p1, p2, p3 (required)")
	createCmd.Flags().String("level", "", "level: epic, task, subtask")
	createCmd.Flags().String("by", "", "who will do the work")
	createCmd.Flags().String("project", "", "project name")
	createCmd.Flags().String("parent-id", "", "parent item ID")
	createCmd.Flags().String("eta", "", "ETA in RFC3339 format (default: derived from priority)")
	createCmd.Flags().String("due", "", "hard deadline in RFC3339 format")
	createCmd.Flags().String("description", "", "")
	_ = createCmd.Flags().MarkHidden("description")
	createCmd.Flags().StringArray("label", nil, "label atom to attach (repeatable, e.g. --label bug --label security)")
	rootCmd.AddCommand(createCmd)
}
