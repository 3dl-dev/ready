package durability

// TrustAssessment represents the result of evaluating a durability claim against
// an operator provenance level. See §6.1 of the Campfire Durability Convention v0.1.
type TrustAssessment struct {
	// Weight is the qualitative trust weight for the durability claim.
	// One of: "high", "medium", "low", "minimal", "unknown".
	Weight string

	// Warnings are non-fatal advisory messages about the trust assessment.
	Warnings []string

	// MeetsMinimum indicates whether the campfire meets the minimum requirements
	// for committing persistent state. The minimum default is:
	//   max-ttl:0 + lifecycle:persistent + provenance:basic or higher.
	MeetsMinimum bool
}

// Provenance level constants (Operator Provenance Convention v0.1).
const (
	ProvenanceHosted   = "ready.3dl.dev" // hosted metered platform
	ProvenanceVerified = "operator-verified"
	ProvenanceBasic    = "basic"
	ProvenanceNone     = "unverified"
)

// EvaluateTrust evaluates the durability claim weight and minimum-viability for
// persisting state, given an operator provenance level.
//
// provenanceLevel should be one of:
//   - "ready.3dl.dev" — hosted, metered (highest trust)
//   - "operator-verified" — domain-verified operator
//   - "basic" — email-verified
//   - "unverified" — no verification
//   - "" — unknown (treated as unverified)
func EvaluateTrust(durability *DurabilityResult, provenanceLevel string) TrustAssessment {
	var assessment TrustAssessment

	// Map provenance level to claim weight per §6.1 trust table.
	switch provenanceLevel {
	case ProvenanceHosted:
		assessment.Weight = "high"
	case ProvenanceVerified:
		assessment.Weight = "medium"
	case ProvenanceBasic:
		assessment.Weight = "low"
	case ProvenanceNone, "":
		assessment.Weight = "minimal"
	default:
		assessment.Weight = "unknown"
		assessment.Warnings = append(assessment.Warnings,
			"unknown provenance level '"+provenanceLevel+"' — treating as unverified")
	}

	// Minimum requirements for committing persistent state:
	//   1. max-ttl:0 (keep forever)
	//   2. lifecycle:persistent
	//   3. provenance:basic or higher
	hasKeepForever := durability != nil && durability.Valid && durability.MaxTTL == "0"
	hasPersistentLifecycle := durability != nil && durability.Valid && durability.LifecycleType == "persistent"
	hasMinProvenance := provenanceLevel == ProvenanceHosted ||
		provenanceLevel == ProvenanceVerified ||
		provenanceLevel == ProvenanceBasic

	assessment.MeetsMinimum = hasKeepForever && hasPersistentLifecycle && hasMinProvenance

	// Advisory warnings for sub-optimal configurations.
	if durability == nil || !durability.Valid {
		assessment.Warnings = append(assessment.Warnings,
			"durability claim is invalid — cannot assess retention")
		assessment.MeetsMinimum = false
		return assessment
	}

	if durability.MaxTTL == "" && durability.LifecycleType == "" {
		assessment.Warnings = append(assessment.Warnings,
			"no durability tags declared — retention is unknown")
	}

	if provenanceLevel == ProvenanceNone || provenanceLevel == "" {
		assessment.Warnings = append(assessment.Warnings,
			"operator is unverified — do not commit irreplaceable state regardless of durability claims")
	}

	if durability.LifecycleType == "ephemeral" {
		assessment.Warnings = append(assessment.Warnings,
			"lifecycle:ephemeral — this campfire is short-lived; do not build persistent state on it")
	}

	if durability.LifecycleType == "bounded" && durability.LifecycleValue != "" {
		// Forward any existing bounded-past warning from parse result.
		for _, w := range durability.Warnings {
			if w == "durability:lifecycle:bounded date is in the past — campfire lifecycle has elapsed" {
				assessment.Warnings = append(assessment.Warnings,
					"lifecycle:bounded date has elapsed — campfire may already be offline")
			}
		}
	}

	return assessment
}
