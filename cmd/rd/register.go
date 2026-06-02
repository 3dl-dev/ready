package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/spf13/cobra"

	"github.com/campfire-net/ready/pkg/rdconfig"
)

// resolveContext holds the shared context needed by resolve functions.
type resolveContext struct {
	cfg       *rdconfig.Config
	aliases   *naming.AliasStore
	agentID   *identity.Identity
	store     store.Store
	client    *protocol.Client
}

var registerCmd = &cobra.Command{
	Use:   "register",
	Short: "Register this project in the naming tree",
	Long: `Register this project's work campfire under a home campfire.

With no flags, discovers an existing home campfire (via aliases, config,
or beacon discovery) and registers under it. If no home is found, prints
guidance and exits successfully — the project works standalone.

  rd register                        discover home, register if found
  rd register --home <campfire-id>   join a specific home, register under it
  rd register --org <name>           create a new home named <name>, register

The project campfire works independently of naming. Names are optional
discoverability. Run this whenever you're ready to add naming.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		org, _ := cmd.Flags().GetString("org")
		homeFlag, _ := cmd.Flags().GetString("home")

		// Must be initialized.
		campfireID, projectDir, ok := projectRoot()
		if !ok {
			return fmt.Errorf("no .campfire/root found — run 'rd init' first")
		}

		return withAgentAndStore(func(agentID *identity.Identity, s store.Store) error {
			client, err := requireClient()
			if err != nil {
				return err
			}

			// Default name from project directory.
			if name == "" {
				name = filepath.Base(projectDir)
			}

			cfg, err := rdconfig.Load(CFHome())
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			aliases := naming.NewAliasStore(CFHome())

			// --- Find or create home campfire ---

			ctx := &resolveContext{
				cfg:     cfg,
				aliases: aliases,
				agentID: agentID,
				store:   s,
				client:  client,
			}
		homeID, orgName, createdHome, err := resolveHome(cmd, homeFlag, org, ctx)
		if err != nil {
			return err
		}

		// No home found and none requested — guide the user.
		if homeID == "" {
			if jsonOutput {
				out := map[string]interface{}{
					"campfire_id": campfireID,
					"name":        name,
					"registered":  false,
					"reason":      "no home campfire found",
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			fmt.Println("no home campfire found — project works standalone via beacons")
			fmt.Println()
			fmt.Println("  to create one:  rd register --org <name>")
			fmt.Println("  to join one:    rd register --home <campfire-id>")
			return nil
		}

		// --- Find or create ready namespace ---

		readyID, createdReady, err := resolveReady(orgName, ctx, homeID)
		if err != nil {
			return err
		}

		// --- Register project under ready ---

		projectDesc := name + " work campfire"
		if m, err := s.GetMembership(campfireID); err == nil && m != nil && m.Description != "" {
			projectDesc = m.Description
		}

		if err := postBeaconRegistration(agentID, s, readyID, campfireID, name, projectDesc); err != nil {
			return fmt.Errorf("registering project: %w", err)
		}

		// --- Save state ---

		cfg.Org = orgName
		cfg.HomeCampfireID = homeID
		cfg.ReadyCampfireID = readyID
		if err := rdconfig.Save(CFHome(), cfg); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not save config: %v\n", err)
		}

		// --- Output ---

			namespace := fmt.Sprintf("cf://%s.ready.%s", orgName, name)
			localURI := fmt.Sprintf("cf://~%s.ready/%s", orgName, name)
			if jsonOutput {
				out := map[string]interface{}{
					"campfire_id":       campfireID,
					"home_campfire_id":  homeID,
					"ready_campfire_id": readyID,
					"name":              name,
					"org":               orgName,
					"namespace":         namespace,
					"local_uri":         localURI,
					"registered":        true,
					"created_home":      createdHome,
					"created_ready":     createdReady,
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			if createdHome {
				fmt.Printf("created home campfire: %s\n", homeID[:12]+"...")
			}
			if createdReady {
				fmt.Printf("created ready namespace: %s\n", readyID[:12]+"...")
			}
			fmt.Printf("registered %s\n", namespace)
			fmt.Printf("  local: %s\n", localURI)
			fmt.Println()
			fmt.Println("  to make globally resolvable later:")
			fmt.Printf("    cf register <root-id> %s %s\n", orgName, homeID)

			return nil
		})
	},
}

// resolveHome finds or creates the home campfire based on flags, config, and aliases.
// Returns (homeID, orgName, createdHome, error).
// Returns empty homeID when no home is found and none was requested (not an error).
func resolveHome(cmd *cobra.Command, homeFlag, org string, ctx *resolveContext) (string, string, bool, error) {
	// Mode 1: explicit --home flag.
	if homeFlag != "" {
		orgName := org
		if orgName == "" {
			orgName = ctx.cfg.Org
		}
		if orgName == "" {
			orgName = "default"
		}
		if err := ctx.aliases.Set("home", homeFlag); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not cache home alias: %v\n", err)
		}
		if err := ctx.aliases.Set(orgName, homeFlag); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not cache org alias %q: %v\n", orgName, err)
		}
		return homeFlag, orgName, false, nil
	}

	// Mode 2: explicit --org — create a new home.
	if cmd.Flags().Changed("org") && org != "" {
		homeDesc := org + " operator root"
		homeID, err := createLocalCampfire(ctx.client, "", "invite-only", []string{"beacon:registration"}, homeDesc)
		if err != nil {
			return "", "", false, fmt.Errorf("creating home campfire: %w", err)
		}
		if err := ctx.aliases.Set("home", homeID); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not cache home alias: %v\n", err)
		}
		if err := ctx.aliases.Set(org, homeID); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not cache org alias %q: %v\n", org, err)
		}
		return homeID, org, true, nil
	}

	// Mode 3: discover existing home.
	orgName := org
	if orgName == "" {
		orgName = ctx.cfg.Org
	}

	// Check alias "home".
	if id, err := ctx.aliases.Get("home"); err == nil && id != "" {
		if orgName == "" {
			orgName = "default"
		}
		return id, orgName, false, nil
	}

	// Check config.
	if ctx.cfg.HomeCampfireID != "" {
		if orgName == "" {
			orgName = "default"
		}
		if err := ctx.aliases.Set("home", ctx.cfg.HomeCampfireID); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not cache home alias: %v\n", err)
		}
		return ctx.cfg.HomeCampfireID, orgName, false, nil
	}

	// Nothing found. Return empty — caller will guide the user.
	return "", "", false, nil
}

// resolveReady finds or creates the ready namespace campfire under the home.
func resolveReady(org string, ctx *resolveContext, homeID string) (string, bool, error) {
	readyAlias := org + ".ready"

	// Check config first.
	if ctx.cfg.ReadyCampfireID != "" {
		if err := ctx.aliases.Set(readyAlias, ctx.cfg.ReadyCampfireID); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not cache ready alias: %v\n", err)
		}
		return ctx.cfg.ReadyCampfireID, false, nil
	}

	// Check alias.
	if id, err := ctx.aliases.Get(readyAlias); err == nil && id != "" {
		return id, false, nil
	}

	// Create ready namespace campfire.
	readyDesc := org + " ready namespace"
	readyID, err := createLocalCampfire(ctx.client, "", "invite-only", []string{"beacon:registration"}, readyDesc)
	if err != nil {
		return "", false, fmt.Errorf("creating ready namespace: %w", err)
	}

	// Register under home.
	if err := postBeaconRegistration(ctx.agentID, ctx.store, homeID, readyID, "ready", readyDesc); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not register ready under home: %v\n", err)
	}

	if err := ctx.aliases.Set(readyAlias, readyID); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not cache ready alias: %v\n", err)
	}
	return readyID, true, nil
}

// postBeaconRegistration sends a beacon-registration with naming:name:<segment>
// to the parent campfire, registering the child campfire under the given name.
//
// It locates the child campfire's published beacon via beacon.Scan on the
// default beacon directory — beacon publishing happens during campfire creation
// (protocol.Client.Create / createLocalCampfire), so the beacon is always
// present before postBeaconRegistration is called.
func postBeaconRegistration(_ *identity.Identity, _ store.Store, parentID, childID, name, description string) error {
	// Scan the default beacon directory for the child campfire's published beacon.
	beacons, err := beacon.Scan(beacon.DefaultBeaconDir())
	if err != nil {
		return fmt.Errorf("scanning beacons for %s: %w", childID[:12], err)
	}
	var innerBeacon *beacon.Beacon
	for i := range beacons {
		if beacons[i].CampfireIDHex() == childID {
			innerBeacon = &beacons[i]
			break
		}
	}
	if innerBeacon == nil {
		return fmt.Errorf("no beacon found for campfire %s — was it created with createLocalCampfire?", childID[:12])
	}

	regPayload := map[string]interface{}{
		"campfire_id": childID,
		"name":        name,
		"description": description,
		"beacon": map[string]interface{}{
			"campfire_id":            childID,
			"description":            innerBeacon.Description,
			"join_protocol":          innerBeacon.JoinProtocol,
			"reception_requirements": innerBeacon.ReceptionRequirements,
			"tags": []string{
				"category:infrastructure",
				"topic:work-management",
				"member_count:1",
				"published_at:" + time.Now().UTC().Format(time.RFC3339),
				"naming:name:" + name,
			},
			"signature": hex.EncodeToString(innerBeacon.Signature),
		},
	}
	payloadBytes, err := json.Marshal(regPayload)
	if err != nil {
		return fmt.Errorf("encoding registration payload: %w", err)
	}

	tags := []string{"beacon:registration", "naming:name:" + name}
	regClient, err := requireClient()
	if err != nil {
		return fmt.Errorf("initializing campfire client: %w", err)
	}
	_, err = regClient.Send(protocol.SendRequest{
		CampfireID: parentID,
		Payload:    payloadBytes,
		Tags:       tags,
	})
	return err
}

func init() {
	registerCmd.Flags().String("name", "", "project name (default: project directory name)")
	registerCmd.Flags().String("org", "", "create a new home campfire with this org name")
	registerCmd.Flags().String("home", "", "join an existing home campfire by ID")
	rootCmd.AddCommand(registerCmd)
}
