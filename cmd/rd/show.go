package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/ready/pkg/crossdep"
	"github.com/spf13/cobra"
)

var showCmd = &cobra.Command{
	Use:   "show <item-id>",
	Short: "Show a work item",
	Long: `Show full details of a work item — status, context, dependencies, audit trail.

Example:
  rd show ready-a1b
  rd show ready-a1b --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID := args[0]
		auditFlag, _ := cmd.Flags().GetBool("audit")

		autoSyncPull()

		// On a nostr-native project the read + audit authority is the nostr
		// projection, never a campfire store. Skip openStore() entirely so
		// `rd show` (incl. --audit) provisions NO campfire store.db under CFHome
		// and takes the no-.cf audit path below (ready-6ef veracity fix).
		// byIDFromJSONLOrStore short-circuits to the projection when reads are
		// nostr-native, so the nil store is never dereferenced.
		_, native := nostrNativeProject()

		var s store.Store
		if !native {
			var err error
			s, err = openStore()
			if err != nil {
				return err
			}
			defer s.Close()
		}

		item, err := byIDFromJSONLOrStore(s, itemID)
		if err != nil {
			cmd.SilenceUsage = true
			return err
		}

		// Cross-campfire dep resolution is a campfire-federation concern requiring a
		// campfire store; it does not apply on the nostr-native path.
		var crossDeps []crossdep.ResolvedDep
		if !native {
			aliases := naming.NewAliasStore(CFHome())
			crossDeps = crossdep.ResolveDeps(item, s, aliases)
		}

		if jsonOutput {
			// Augment item with resolved cross-campfire dep info.
			type crossDepJSON struct {
				Ref          string `json:"ref"`
				CampfireName string `json:"campfire_name,omitempty"`
				ItemID       string `json:"item_id,omitempty"`
				Status       string `json:"status,omitempty"`
				Warning      string `json:"warning,omitempty"`
			}
			var crossDepsOut []crossDepJSON
			for _, cd := range crossDeps {
				cdj := crossDepJSON{
					Ref:          cd.Ref,
					CampfireName: cd.CampfireName,
					ItemID:       cd.ItemID,
				}
				if cd.Item != nil {
					cdj.Status = cd.Item.Status
				}
				if cd.Warning != "" {
					cdj.Warning = cd.Warning
				}
				crossDepsOut = append(crossDepsOut, cdj)
			}
			// Build augmented output map.
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			// Encode item first, then add cross_campfire_deps.
			itemBytes, err2 := json.Marshal(item)
			if err2 != nil {
				return enc.Encode(item)
			}
			var itemMap map[string]interface{}
			if err2 = json.Unmarshal(itemBytes, &itemMap); err2 != nil {
				return enc.Encode(item)
			}
			if len(crossDepsOut) > 0 {
				itemMap["cross_campfire_deps"] = crossDepsOut
			}
			return enc.Encode(itemMap)
		}

		// Human-readable output.
		fmt.Printf("ID:       %s\n", item.ID)
		fmt.Printf("Title:    %s\n", item.Title)
		fmt.Printf("Status:   %s\n", item.Status)
		fmt.Printf("Type:     %s\n", item.Type)
		fmt.Printf("Priority: %s\n", item.Priority)
		fmt.Printf("For:      %s\n", item.For)
		if item.By != "" {
			fmt.Printf("By:       %s\n", item.By)
		}
		if item.Project != "" {
			fmt.Printf("Project:  %s\n", formatCampfireIDForDisplay(item.Project))
		}
		if item.Level != "" {
			fmt.Printf("Level:    %s\n", item.Level)
		}
		if item.ETA != "" {
			fmt.Printf("ETA:      %s (%s)\n", item.ETA, formatETA(item.ETA))
		}
		if item.Due != "" {
			fmt.Printf("Due:      %s\n", item.Due)
		}
		if item.ParentID != "" {
			fmt.Printf("Parent:   %s\n", item.ParentID)
		}
		if len(item.BlockedBy) > 0 {
			fmt.Printf("Blocked by: %s\n", strings.Join(item.BlockedBy, ", "))
		}
		if len(item.Blocks) > 0 {
			fmt.Printf("Blocks:   %s\n", strings.Join(item.Blocks, ", "))
		}
		if item.WaitingOn != "" {
			fmt.Printf("Waiting on: %s (%s)\n", item.WaitingOn, item.WaitingType)
		}
		if len(item.Labels) > 0 {
			fmt.Printf("Labels:   %s\n", strings.Join(item.Labels, ", "))
		}
		// Cross-campfire dep display: resolved and unresolved.
		for _, cd := range crossDeps {
			if cd.Item != nil {
				fmt.Printf("Cross-dep: %s → %s [%s]\n", cd.Ref, cd.ItemID, cd.Item.Status)
			} else {
				fmt.Fprintf(os.Stderr, "warning: %s\n", cd.Warning)
			}
		}
		if item.Context != "" {
			fmt.Printf("\nContext:\n%s\n", item.Context)
		}
		if len(item.History) > 0 {
			// --audit: annotate each history entry with the cf-authority scope the
			// actor acted under. Silent (no annotation) for non-pubkey actors and
			// items whose actors carry no delegation grant.
			var auth auditLabeler
			if auditFlag {
				if native {
					// Nostr-native: resolve authority from the signed nostr
					// projection. NEVER requireClient()/protocol.Init here — that
					// provisions .cf/identity.json, the exact artifact the cutover
					// eliminates (ready-6ef veracity fix). Assign only a non-nil
					// concrete resolver so the interface nil-check below is sound.
					if r := loadNostrAuthorityResolver(); r != nil {
						auth = r
					}
				} else if client, cErr := requireClient(); cErr == nil {
					if r := loadAuthorityResolver(client, item.CampfireID); r != nil {
						auth = r
					}
				}
			}
			fmt.Printf("\nHistory:\n")
			for _, h := range item.History {
				actor := h.ChangedBy
				ts := h.Timestamp
				note := ""
				if h.Note != "" {
					note = " — " + h.Note
				}
				authNote := ""
				if auth != nil {
					if label := auth.label(actor); label != "" {
						authNote = "  [authority: " + label + "]"
					}
				}
				fmt.Printf("  [%s] %s → %s by %s%s%s\n", ts, h.FromStatus, h.ToStatus, actor, note, authNote)
			}
		}
		fmt.Printf("\nCampfire: %s\n", formatCampfireIDForDisplay(item.CampfireID))
		fmt.Printf("Msg ID:   %s\n", item.MsgID)
		return nil
	},
}

func init() {
	showCmd.Flags().Bool("audit", false, "annotate each history entry with the cf-authority grant scope its actor acted under")
	rootCmd.AddCommand(showCmd)
}
