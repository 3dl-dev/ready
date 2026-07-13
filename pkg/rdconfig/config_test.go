package rdconfig_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/ready/pkg/rdconfig"
)

// --- Path / SyncConfigPath ---

// TestPath_ReturnsRdJSON verifies that Path returns the expected file path
// within the given campfire home directory.
func TestPath_ReturnsRdJSON(t *testing.T) {
	got := rdconfig.Path("/home/user/.campfire")
	want := "/home/user/.campfire/rd.json"
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

// TestSyncConfigPath_ReturnsReadyConfigJSON verifies that SyncConfigPath returns
// the expected .ready/config.json path within the project directory.
func TestSyncConfigPath_ReturnsReadyConfigJSON(t *testing.T) {
	got := rdconfig.SyncConfigPath("/home/user/projects/myproject")
	want := "/home/user/projects/myproject/.ready/config.json"
	if got != want {
		t.Errorf("SyncConfigPath() = %q, want %q", got, want)
	}
}

// --- Config Load / Save ---

// TestLoad_MissingFile_ReturnsZeroConfig verifies that Load returns a zero-value
// Config (not an error) when the config file does not exist.
func TestLoad_MissingFile_ReturnsZeroConfig(t *testing.T) {
	cfHome := t.TempDir()
	c, err := rdconfig.Load(cfHome)
	if err != nil {
		t.Fatalf("Load on missing file: expected nil error, got %v", err)
	}
	if c == nil {
		t.Fatal("Load on missing file: expected non-nil Config, got nil")
	}
	if c.Org != "" || c.HomeCampfireID != "" || c.ReadyCampfireID != "" {
		t.Errorf("Load on missing file: expected zero Config, got %+v", c)
	}
}

// TestLoad_InvalidJSON_ReturnsError verifies that Load returns an error when the
// config file contains invalid JSON.
func TestLoad_InvalidJSON_ReturnsError(t *testing.T) {
	cfHome := t.TempDir()
	path := rdconfig.Path(cfHome)
	if err := os.WriteFile(path, []byte("not valid json {{{"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := rdconfig.Load(cfHome)
	if err == nil {
		t.Fatal("Load with invalid JSON: expected error, got nil")
	}
}

// TestSaveLoad_RoundTrip verifies that Save followed by Load returns the same Config.
func TestSaveLoad_RoundTrip(t *testing.T) {
	cfHome := t.TempDir()
	original := &rdconfig.Config{
		Org:             "acme",
		HomeCampfireID:  "aabbcc001122",
		ReadyCampfireID: "ddeeff334455",
	}
	if err := rdconfig.Save(cfHome, original); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := rdconfig.Load(cfHome)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Org != original.Org {
		t.Errorf("Org: got %q, want %q", loaded.Org, original.Org)
	}
	if loaded.HomeCampfireID != original.HomeCampfireID {
		t.Errorf("HomeCampfireID: got %q, want %q", loaded.HomeCampfireID, original.HomeCampfireID)
	}
	if loaded.ReadyCampfireID != original.ReadyCampfireID {
		t.Errorf("ReadyCampfireID: got %q, want %q", loaded.ReadyCampfireID, original.ReadyCampfireID)
	}
}

// TestSave_PartialConfig verifies that optional fields (empty strings) are omitted
// from the JSON output (omitempty). Only non-zero fields should appear in the file.
func TestSave_PartialConfig(t *testing.T) {
	cfHome := t.TempDir()
	c := &rdconfig.Config{Org: "myorg"} // HomeCampfireID and ReadyCampfireID are empty
	if err := rdconfig.Save(cfHome, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(rdconfig.Path(cfHome))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// Empty fields should be omitted (omitempty).
	content := string(data)
	if containsSubstr(content, "home_campfire_id") {
		t.Errorf("empty HomeCampfireID should be omitted from JSON, got: %s", content)
	}
	if containsSubstr(content, "ready_campfire_id") {
		t.Errorf("empty ReadyCampfireID should be omitted from JSON, got: %s", content)
	}
	// Org should appear.
	if !containsSubstr(content, "myorg") {
		t.Errorf("Org 'myorg' should be present in JSON, got: %s", content)
	}
}

// --- SyncConfig LoadSyncConfig / SaveSyncConfig ---

// TestLoadSyncConfig_MissingFile_ReturnsZeroSyncConfig verifies that LoadSyncConfig
// returns a zero-value SyncConfig (not an error) when the file does not exist.
func TestLoadSyncConfig_MissingFile_ReturnsZeroSyncConfig(t *testing.T) {
	projectDir := t.TempDir()
	c, err := rdconfig.LoadSyncConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadSyncConfig on missing file: expected nil error, got %v", err)
	}
	if c == nil {
		t.Fatal("LoadSyncConfig on missing file: expected non-nil SyncConfig, got nil")
	}
	if c.CampfireID != "" {
		t.Errorf("LoadSyncConfig on missing file: expected zero SyncConfig, got CampfireID=%q", c.CampfireID)
	}
	if c.Durability != nil {
		t.Errorf("LoadSyncConfig on missing file: expected nil Durability, got %+v", c.Durability)
	}
}

// TestLoadSyncConfig_InvalidJSON_ReturnsError verifies that LoadSyncConfig returns
// an error when the config file contains invalid JSON.
func TestLoadSyncConfig_InvalidJSON_ReturnsError(t *testing.T) {
	projectDir := t.TempDir()
	configPath := rdconfig.SyncConfigPath(projectDir)
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("{invalid json"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := rdconfig.LoadSyncConfig(projectDir)
	if err == nil {
		t.Fatal("LoadSyncConfig with invalid JSON: expected error, got nil")
	}
}

// TestSaveSyncConfig_CreatesReadyDir verifies that SaveSyncConfig creates the
// .ready directory when it doesn't exist.
func TestSaveSyncConfig_CreatesReadyDir(t *testing.T) {
	projectDir := t.TempDir()
	c := &rdconfig.SyncConfig{CampfireID: "aabbccdd"}
	if err := rdconfig.SaveSyncConfig(projectDir, c); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	readyDir := filepath.Join(projectDir, ".ready")
	info, err := os.Stat(readyDir)
	if err != nil {
		t.Fatalf("stat .ready dir: %v", err)
	}
	if !info.IsDir() {
		t.Error(".ready should be a directory")
	}
}

// TestSaveSyncConfig_LoadSyncConfig_RoundTrip verifies that SaveSyncConfig followed
// by LoadSyncConfig returns the same SyncConfig, including the DurabilityAssessment.
func TestSaveSyncConfig_LoadSyncConfig_RoundTrip(t *testing.T) {
	projectDir := t.TempDir()
	original := &rdconfig.SyncConfig{
		CampfireID: "deadbeef1234",
		Durability: &rdconfig.DurabilityAssessment{
			MeetsMinimum:    true,
			Weight:          "high",
			MaxTTL:          "0",
			LifecycleType:   "persistent",
			ProvenanceLevel: "basic",
			Warnings:        []string{"advisory: no replication"},
		},
	}
	if err := rdconfig.SaveSyncConfig(projectDir, original); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	loaded, err := rdconfig.LoadSyncConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	if loaded.CampfireID != original.CampfireID {
		t.Errorf("CampfireID: got %q, want %q", loaded.CampfireID, original.CampfireID)
	}
	if loaded.Durability == nil {
		t.Fatal("Durability: got nil, want non-nil")
	}
	d := loaded.Durability
	orig := original.Durability
	if d.MeetsMinimum != orig.MeetsMinimum {
		t.Errorf("Durability.MeetsMinimum: got %v, want %v", d.MeetsMinimum, orig.MeetsMinimum)
	}
	if d.Weight != orig.Weight {
		t.Errorf("Durability.Weight: got %q, want %q", d.Weight, orig.Weight)
	}
	if d.MaxTTL != orig.MaxTTL {
		t.Errorf("Durability.MaxTTL: got %q, want %q", d.MaxTTL, orig.MaxTTL)
	}
	if d.LifecycleType != orig.LifecycleType {
		t.Errorf("Durability.LifecycleType: got %q, want %q", d.LifecycleType, orig.LifecycleType)
	}
	if d.ProvenanceLevel != orig.ProvenanceLevel {
		t.Errorf("Durability.ProvenanceLevel: got %q, want %q", d.ProvenanceLevel, orig.ProvenanceLevel)
	}
	if len(d.Warnings) != len(orig.Warnings) || (len(orig.Warnings) > 0 && d.Warnings[0] != orig.Warnings[0]) {
		t.Errorf("Durability.Warnings: got %v, want %v", d.Warnings, orig.Warnings)
	}
}

// TestSaveSyncConfig_LoadSyncConfig_ProjectName verifies that ProjectName is
// persisted and round-trips correctly.
func TestSaveSyncConfig_LoadSyncConfig_ProjectName(t *testing.T) {
	projectDir := t.TempDir()
	original := &rdconfig.SyncConfig{
		CampfireID:  "deadbeef1234",
		ProjectName: "acme.backend",
	}
	if err := rdconfig.SaveSyncConfig(projectDir, original); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	loaded, err := rdconfig.LoadSyncConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	if loaded.ProjectName != original.ProjectName {
		t.Errorf("ProjectName: got %q, want %q", loaded.ProjectName, original.ProjectName)
	}
}

// TestSaveSyncConfig_ProjectName_OmitEmpty verifies that empty ProjectName is
// omitted from JSON output.
func TestSaveSyncConfig_ProjectName_OmitEmpty(t *testing.T) {
	projectDir := t.TempDir()
	c := &rdconfig.SyncConfig{CampfireID: "cafe"}
	// ProjectName is not set (empty string).
	if err := rdconfig.SaveSyncConfig(projectDir, c); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	data, err := os.ReadFile(rdconfig.SyncConfigPath(projectDir))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if containsSubstr(content, "project_name") {
		t.Errorf("empty ProjectName should be omitted from JSON, got: %s", content)
	}
}

// TestSaveSyncConfig_NilDurability verifies that a SyncConfig with nil Durability
// round-trips correctly — the Durability field stays nil after load.
func TestSaveSyncConfig_NilDurability(t *testing.T) {
	projectDir := t.TempDir()
	original := &rdconfig.SyncConfig{CampfireID: "cafebabe"}
	if err := rdconfig.SaveSyncConfig(projectDir, original); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	loaded, err := rdconfig.LoadSyncConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	if loaded.Durability != nil {
		t.Errorf("Durability: got %+v, want nil (no durability was saved)", loaded.Durability)
	}
}

// TestSaveSyncConfig_Overwrite verifies that saving a second time overwrites the first
// — LoadSyncConfig returns the most recent values.
func TestSaveSyncConfig_Overwrite(t *testing.T) {
	projectDir := t.TempDir()
	first := &rdconfig.SyncConfig{CampfireID: "firstid"}
	if err := rdconfig.SaveSyncConfig(projectDir, first); err != nil {
		t.Fatalf("SaveSyncConfig (first): %v", err)
	}
	second := &rdconfig.SyncConfig{CampfireID: "secondid"}
	if err := rdconfig.SaveSyncConfig(projectDir, second); err != nil {
		t.Fatalf("SaveSyncConfig (second): %v", err)
	}
	loaded, err := rdconfig.LoadSyncConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	if loaded.CampfireID != "secondid" {
		t.Errorf("CampfireID: got %q, want 'secondid' (second save should overwrite)", loaded.CampfireID)
	}
}

// TestSyncConfig_SummaryCampfireID_RoundTrip verifies that SummaryCampfireID and
// Encrypted fields round-trip correctly through SaveSyncConfig / LoadSyncConfig.
// This covers the Wave 2 E2E + shadow summary campfire config fields.
func TestSyncConfig_SummaryCampfireID_RoundTrip(t *testing.T) {
	projectDir := t.TempDir()
	original := &rdconfig.SyncConfig{
		CampfireID:        "aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff6666aaaa1111bbbb2222",
		SummaryCampfireID: "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		Encrypted:         true,
	}
	if err := rdconfig.SaveSyncConfig(projectDir, original); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	loaded, err := rdconfig.LoadSyncConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	if loaded.CampfireID != original.CampfireID {
		t.Errorf("CampfireID: got %q, want %q", loaded.CampfireID, original.CampfireID)
	}
	if loaded.SummaryCampfireID != original.SummaryCampfireID {
		t.Errorf("SummaryCampfireID: got %q, want %q", loaded.SummaryCampfireID, original.SummaryCampfireID)
	}
	if loaded.Encrypted != original.Encrypted {
		t.Errorf("Encrypted: got %v, want %v", loaded.Encrypted, original.Encrypted)
	}
}

// TestSyncConfig_SummaryCampfireID_OmitEmpty verifies that an empty
// SummaryCampfireID is omitted from the JSON output (omitempty).
func TestSyncConfig_SummaryCampfireID_OmitEmpty(t *testing.T) {
	projectDir := t.TempDir()
	c := &rdconfig.SyncConfig{CampfireID: "aabbccdd"}
	if err := rdconfig.SaveSyncConfig(projectDir, c); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	data, err := os.ReadFile(rdconfig.SyncConfigPath(projectDir))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if containsSubstr(content, "summary_campfire_id") {
		t.Errorf("empty SummaryCampfireID should be omitted, got: %s", content)
	}
	if containsSubstr(content, "encrypted") {
		t.Errorf("false Encrypted should be omitted (omitempty), got: %s", content)
	}
}

// containsSubstr is a simple substring check used by tests in this package.
func containsSubstr(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestSave_RestrictivePermissions(t *testing.T) {
	cfHome := t.TempDir()
	c := &rdconfig.Config{Org: "testorg", HomeCampfireID: "abc123"}
	if err := rdconfig.Save(cfHome, c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path := rdconfig.Path(cfHome)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("rd.json permissions: got %04o, want 0600", got)
	}
}

func TestSaveSyncConfig_RestrictivePermissions(t *testing.T) {
	projectDir := t.TempDir()
	c := &rdconfig.SyncConfig{CampfireID: "deadbeef"}
	if err := rdconfig.SaveSyncConfig(projectDir, c); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}

	readyDir := filepath.Join(projectDir, ".ready")

	// Directory must be owner-only (0700).
	dirInfo, err := os.Stat(readyDir)
	if err != nil {
		t.Fatalf("stat .ready: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0700 {
		t.Errorf(".ready dir permissions: got %04o, want 0700", got)
	}

	// config.json must be owner-only (0600).
	configFile := rdconfig.SyncConfigPath(projectDir)
	fileInfo, err := os.Stat(configFile)
	if err != nil {
		t.Fatalf("stat config.json: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0600 {
		t.Errorf("config.json permissions: got %04o, want 0600", got)
	}
}

// --- DC1: Disk Persistence Test ---

// TestDC1_SyncConfig_DiskPersistenceViaLoadSyncConfig verifies that rd init writes
// project_name to disk via SaveSyncConfig and that LoadSyncConfig reads it back.
// DC1 requirement: disk persistence must be verified via rdconfig.LoadSyncConfig(),
// not just by checking an in-memory struct.
func TestDC1_SyncConfig_DiskPersistenceViaLoadSyncConfig(t *testing.T) {
	projectDir := t.TempDir()

	// Create a SyncConfig with ProjectName (simulating what init.go does).
	originalCfg := &rdconfig.SyncConfig{
		CampfireID:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ProjectName: "test-project-persistence",
		Durability: &rdconfig.DurabilityAssessment{
			MeetsMinimum:  true,
			Weight:        "high",
			MaxTTL:        "0",
			LifecycleType: "persistent",
		},
	}

	// Save the config (simulating init.go SaveSyncConfig call).
	if err := rdconfig.SaveSyncConfig(projectDir, originalCfg); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}

	// Load it back from disk (DC1 requirement: verify disk persistence).
	loadedCfg, err := rdconfig.LoadSyncConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}

	// Verify ProjectName was persisted to disk.
	if loadedCfg.ProjectName != originalCfg.ProjectName {
		t.Errorf("ProjectName: got %q, want %q (DC1: value must be read back from disk, not in-memory)",
			loadedCfg.ProjectName, originalCfg.ProjectName)
	}

	// Verify other fields also persisted.
	if loadedCfg.CampfireID != originalCfg.CampfireID {
		t.Errorf("CampfireID: got %q, want %q", loadedCfg.CampfireID, originalCfg.CampfireID)
	}
	if loadedCfg.Durability == nil {
		t.Fatal("Durability: got nil, want non-nil")
	}
	if loadedCfg.Durability.MeetsMinimum != originalCfg.Durability.MeetsMinimum {
		t.Errorf("Durability.MeetsMinimum: got %v, want %v",
			loadedCfg.Durability.MeetsMinimum, originalCfg.Durability.MeetsMinimum)
	}
}
