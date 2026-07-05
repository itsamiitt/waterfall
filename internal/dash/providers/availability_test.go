package providers

import "testing"

// TestEffectiveAvailability_TruthTable exhaustively pins the conjunction of the two state axes.
// It walks every inclusion-status value (including an unknown one) crossed with every op_state
// value (including an unknown one), with compliance varied where DEPRIORITIZED makes it matter.
func TestEffectiveAvailability_TruthTable(t *testing.T) {
	type row struct {
		status     string
		compliance string
		opState    string
		want       AvailState
		reason     string
	}
	// Inclusion status FIRST: a blocked status dominates regardless of op_state.
	rows := []row{
		// EXCLUDED — never usable, whatever the op_state or compliance.
		{StatusExcluded, "approved", OpEnabled, Unavailable, ReasonStatusExcluded},
		{StatusExcluded, "", OpDisabled, Unavailable, ReasonStatusExcluded},
		{StatusExcluded, "approved", OpPaused, Unavailable, ReasonStatusExcluded},
		{StatusExcluded, "approved", OpMaintenance, Unavailable, ReasonStatusExcluded},

		// DEPRIORITIZED, NOT compliance-approved — blocked (VR-3) regardless of op_state.
		{StatusDeprioritized, "", OpEnabled, Unavailable, ReasonStatusDeprioritized},
		{StatusDeprioritized, "pending", OpEnabled, Unavailable, ReasonStatusDeprioritized},
		{StatusDeprioritized, "rejected", OpMaintenance, Unavailable, ReasonStatusDeprioritized},
		{StatusDeprioritized, "pending", OpPaused, Unavailable, ReasonStatusDeprioritized},

		// DEPRIORITIZED + approved — passes the inclusion conjunct; op_state then decides.
		{StatusDeprioritized, "approved", OpEnabled, Available, ReasonNone},
		{StatusDeprioritized, "APPROVED", OpEnabled, Available, ReasonNone}, // case-insensitive
		{StatusDeprioritized, "approved", OpMaintenance, Degraded, ReasonOpStateMaintenance},
		{StatusDeprioritized, "approved", OpDisabled, Unavailable, ReasonOpStateDisabled},
		{StatusDeprioritized, "approved", OpPaused, Unavailable, ReasonOpStatePaused},

		// ACTIVE-CANDIDATE — passes the inclusion conjunct; op_state decides. Compliance
		// is irrelevant here.
		{StatusActiveCandidate, "", OpEnabled, Available, ReasonNone},
		{StatusActiveCandidate, "pending", OpEnabled, Available, ReasonNone},
		{StatusActiveCandidate, "", OpMaintenance, Degraded, ReasonOpStateMaintenance},
		{StatusActiveCandidate, "", OpDisabled, Unavailable, ReasonOpStateDisabled},
		{StatusActiveCandidate, "", OpPaused, Unavailable, ReasonOpStatePaused},

		// Unknown values on either axis fail closed.
		{"WAT", "approved", OpEnabled, Unavailable, ReasonStatusUnknown},
		{StatusActiveCandidate, "", "sideways", Unavailable, ReasonOpStateUnknown},
	}

	for _, r := range rows {
		got := EffectiveAvailability(Provider{
			Status:                 r.status,
			ComplianceReviewStatus: r.compliance,
			OpState:                r.opState,
		})
		if got.State != r.want || got.Reason != r.reason {
			t.Errorf("status=%q compliance=%q op_state=%q => {%s %q}, want {%s %q}",
				r.status, r.compliance, r.opState, got.State, got.Reason, r.want, r.reason)
		}
		if got.Available() != (r.want == Available) {
			t.Errorf("status=%q op_state=%q Available()=%v, want %v",
				r.status, r.opState, got.Available(), r.want == Available)
		}
	}
}

// TestEffectiveAvailability_ExhaustiveCoverage asserts every (status × op_state) cell is decided
// (no panic, always a defined state) so no combination is left unhandled.
func TestEffectiveAvailability_ExhaustiveCoverage(t *testing.T) {
	statuses := []string{StatusActiveCandidate, StatusDeprioritized, StatusExcluded, "unknown"}
	opStates := []string{OpEnabled, OpDisabled, OpPaused, OpMaintenance, "unknown"}
	compliances := []string{"", "pending", "approved"}
	for _, s := range statuses {
		for _, o := range opStates {
			for _, c := range compliances {
				a := EffectiveAvailability(Provider{Status: s, OpState: o, ComplianceReviewStatus: c})
				switch a.State {
				case Available, Degraded, Unavailable:
				default:
					t.Fatalf("undefined state for status=%q op_state=%q compliance=%q: %q", s, o, c, a.State)
				}
				if a.State == Available && a.Reason != ReasonNone {
					t.Errorf("available cell carried a reason: status=%q op_state=%q reason=%q", s, o, a.Reason)
				}
				if a.State != Available && a.Reason == ReasonNone {
					t.Errorf("non-available cell missing a reason: status=%q op_state=%q", s, o)
				}
			}
		}
	}
}
