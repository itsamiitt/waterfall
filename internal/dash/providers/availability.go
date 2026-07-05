package providers

import "strings"

// AvailState is the three-valued effective-availability enum. It is COMPUTED from a provider's
// two state axes and never persisted.
type AvailState string

const (
	// Available: usable now — inclusion status permits it and op_state is enabled.
	Available AvailState = "available"
	// Degraded: usable but impaired — op_state=maintenance (VR-4 "warn"): the router may still
	// route to it with a warning, so it is neither fully available nor hard-unavailable.
	Degraded AvailState = "degraded"
	// Unavailable: not usable — an inclusion-status or op_state conjunct blocks it.
	Unavailable AvailState = "unavailable"
)

// Machine reason codes (stable identifiers; doc 04 §2.3 example uses "op_state_paused" /
// "status_deprioritized"). Empty means "available, no failing conjunct".
const (
	ReasonNone                = ""
	ReasonStatusExcluded      = "status_excluded"
	ReasonStatusDeprioritized = "status_deprioritized"
	ReasonStatusUnknown       = "status_unknown"
	ReasonOpStateDisabled     = "op_state_disabled"
	ReasonOpStatePaused       = "op_state_paused"
	ReasonOpStateMaintenance  = "op_state_maintenance"
	ReasonOpStateUnknown      = "op_state_unknown"
)

// Availability is the result of the single availability computation: the three-valued state
// plus the machine reason of the first failing conjunct ("" when available).
type Availability struct {
	State  AvailState
	Reason string
}

// Available reports whether the provider is fully available (State == Available).
func (a Availability) Available() bool { return a.State == Available }

// EffectiveAvailability is THE one function computing effective availability (doc 04 §2.3;
// migration 0005 header). It is the conjunction of two axes, evaluated inclusion-status FIRST
// so a blocked inclusion status dominates the reason (doc 04 create example: a DEPRIORITIZED +
// disabled provider reports "status_deprioritized", not the op_state):
//
//	inclusion status (ADR-0009 / doc 07 VR-2,VR-3):
//	    EXCLUDED                              -> unavailable            (never usable)
//	    DEPRIORITIZED & compliance!=approved  -> unavailable            (VR-3)
//	    DEPRIORITIZED & compliance==approved  -> pass through
//	    ACTIVE-CANDIDATE                      -> pass through
//	op_state (doc 07 VR-4):
//	    enabled      -> available
//	    maintenance  -> degraded    (VR-4 warn: still routable)
//	    disabled     -> unavailable
//	    paused       -> unavailable
//
// Unknown values on either axis fail closed to unavailable.
func EffectiveAvailability(p Provider) Availability {
	// Conjunct 1 — inclusion status.
	switch p.Status {
	case StatusExcluded:
		return Availability{Unavailable, ReasonStatusExcluded}
	case StatusDeprioritized:
		if !strings.EqualFold(strings.TrimSpace(p.ComplianceReviewStatus), "approved") {
			return Availability{Unavailable, ReasonStatusDeprioritized}
		}
		// approved: usable — fall through to op_state.
	case StatusActiveCandidate:
		// usable — fall through to op_state.
	default:
		return Availability{Unavailable, ReasonStatusUnknown}
	}

	// Conjunct 2 — runtime op_state.
	switch p.OpState {
	case OpEnabled:
		return Availability{Available, ReasonNone}
	case OpMaintenance:
		return Availability{Degraded, ReasonOpStateMaintenance}
	case OpDisabled:
		return Availability{Unavailable, ReasonOpStateDisabled}
	case OpPaused:
		return Availability{Unavailable, ReasonOpStatePaused}
	default:
		return Availability{Unavailable, ReasonOpStateUnknown}
	}
}
