package durability

import (
	"testing"
)

func TestEvaluateTrustWeights(t *testing.T) {
	validPersistent := &DurabilityResult{
		Valid:         true,
		MaxTTL:        "0",
		LifecycleType: "persistent",
	}

	tests := []struct {
		provenance  string
		wantWeight  string
		wantMinimum bool
	}{
		{ProvenanceHosted, "high", true},
		{ProvenanceVerified, "medium", true},
		{ProvenanceBasic, "low", true},
		{ProvenanceNone, "minimal", false},
		{"", "minimal", false},
	}

	for _, tt := range tests {
		t.Run(tt.provenance, func(t *testing.T) {
			a := EvaluateTrust(validPersistent, tt.provenance)
			if a.Weight != tt.wantWeight {
				t.Errorf("Weight = %q, want %q", a.Weight, tt.wantWeight)
			}
			if a.MeetsMinimum != tt.wantMinimum {
				t.Errorf("MeetsMinimum = %v, want %v", a.MeetsMinimum, tt.wantMinimum)
			}
		})
	}
}

func TestEvaluateTrustMinimumRequirements(t *testing.T) {
	tests := []struct {
		name        string
		durability  *DurabilityResult
		provenance  string
		wantMinimum bool
	}{
		{
			name: "all three met",
			durability: &DurabilityResult{
				Valid:         true,
				MaxTTL:        "0",
				LifecycleType: "persistent",
			},
			provenance:  ProvenanceBasic,
			wantMinimum: true,
		},
		{
			name: "missing keep-forever (has 30d)",
			durability: &DurabilityResult{
				Valid:         true,
				MaxTTL:        "30d",
				LifecycleType: "persistent",
			},
			provenance:  ProvenanceBasic,
			wantMinimum: false,
		},
		{
			name: "missing persistent lifecycle",
			durability: &DurabilityResult{
				Valid:          true,
				MaxTTL:         "0",
				LifecycleType:  "ephemeral",
				LifecycleValue: "10m",
			},
			provenance:  ProvenanceBasic,
			wantMinimum: false,
		},
		{
			name: "provenance too low",
			durability: &DurabilityResult{
				Valid:         true,
				MaxTTL:        "0",
				LifecycleType: "persistent",
			},
			provenance:  ProvenanceNone,
			wantMinimum: false,
		},
		{
			name:        "nil durability",
			durability:  nil,
			provenance:  ProvenanceHosted,
			wantMinimum: false,
		},
		{
			name: "invalid durability result",
			durability: &DurabilityResult{
				Valid: false,
				Error: "malformed tag",
			},
			provenance:  ProvenanceHosted,
			wantMinimum: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := EvaluateTrust(tt.durability, tt.provenance)
			if a.MeetsMinimum != tt.wantMinimum {
				t.Errorf("MeetsMinimum = %v, want %v (Warnings: %v)", a.MeetsMinimum, tt.wantMinimum, a.Warnings)
			}
		})
	}
}

func TestEvaluateTrustWarnings(t *testing.T) {
	// Unverified operator should warn regardless of durability claim.
	d := &DurabilityResult{
		Valid:         true,
		MaxTTL:        "0",
		LifecycleType: "persistent",
	}
	a := EvaluateTrust(d, ProvenanceNone)
	if !containsWarning(a.Warnings, "unverified") {
		t.Errorf("expected unverified warning, got: %v", a.Warnings)
	}

	// No durability tags — should warn.
	empty := &DurabilityResult{Valid: true}
	a2 := EvaluateTrust(empty, ProvenanceBasic)
	if !containsWarning(a2.Warnings, "unknown") {
		t.Errorf("expected unknown retention warning, got: %v", a2.Warnings)
	}

	// Ephemeral lifecycle should warn.
	ephemeral := &DurabilityResult{
		Valid:          true,
		MaxTTL:         "4h",
		LifecycleType:  "ephemeral",
		LifecycleValue: "30m",
	}
	a3 := EvaluateTrust(ephemeral, ProvenanceBasic)
	if !containsWarning(a3.Warnings, "ephemeral") {
		t.Errorf("expected ephemeral warning, got: %v", a3.Warnings)
	}

	// Unknown provenance level should warn and set weight to "unknown".
	a4 := EvaluateTrust(d, "custom-level")
	if a4.Weight != "unknown" {
		t.Errorf("Weight = %q, want unknown", a4.Weight)
	}
	if !containsWarning(a4.Warnings, "unknown provenance level") {
		t.Errorf("expected unknown provenance warning, got: %v", a4.Warnings)
	}
}

func TestEvaluateTrustBoundedPastWarning(t *testing.T) {
	// Bounded date in past should propagate warning through trust assessment.
	d := &DurabilityResult{
		Valid:          true,
		MaxTTL:         "90d",
		LifecycleType:  "bounded",
		LifecycleValue: "2025-01-01T00:00:00Z",
		Warnings:       []string{"durability:lifecycle:bounded date is in the past — campfire lifecycle has elapsed"},
	}
	a := EvaluateTrust(d, ProvenanceVerified)
	if !containsWarning(a.Warnings, "elapsed") {
		t.Errorf("expected elapsed warning propagated, got: %v", a.Warnings)
	}
}

func containsWarning(warnings []string, substr string) bool {
	for _, w := range warnings {
		if contains(w, substr) {
			return true
		}
	}
	return false
}
