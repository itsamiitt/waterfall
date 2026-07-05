// Package workflows is the Waterfall Configuration module (module 7, doc 07): a thin service over
// the shared configver lifecycle engine with the waterfall_workflow VR validators and the read-only
// dry-run simulator. It owns NO lifecycle logic — draft/validate/publish/rollback live in
// configver — only the waterfall_workflow payload semantics. Every bound here may TIGHTEN, never
// loosen, the G3/G4 engine gates; the validators reject any payload attempting otherwise.
package workflows

// RetryLogic is the optional retry block (doc 07 §4). retry_on is a subset of the retryable error
// classes; the engine CallPolicy applies its own attempt cap regardless (config tightens, never
// loosens — G3).
type RetryLogic struct {
	MaxRetries *int     `json:"max_retries,omitempty"`
	BackoffMS  *int     `json:"backoff_ms,omitempty"`
	RetryOn    []string `json:"retry_on,omitempty"`
}

// Workflow is the parsed waterfall_workflow payload (doc 07 §4 JSON Schema). Pointer fields
// distinguish "absent" from a genuine zero for the range checks.
type Workflow struct {
	SchemaVersion       int         `json:"schema_version"`
	Name                string      `json:"name"`
	Trigger             string      `json:"trigger"`
	Fields              []string    `json:"fields"`
	EntryProvider       string      `json:"entry_provider"`
	ParallelProviders   []string    `json:"parallel_providers,omitempty"`
	SequentialProviders []string    `json:"sequential_providers,omitempty"`
	RetryLogic          *RetryLogic `json:"retry_logic,omitempty"`
	TimeoutMS           *int64      `json:"timeout_ms,omitempty"`
	ConfidenceThreshold *float64    `json:"confidence_threshold,omitempty"`
	MinScore            *float64    `json:"min_score,omitempty"`
	MaxCostCredits      *int64      `json:"max_cost_credits,omitempty"`
	MaxProviders        *int        `json:"max_providers,omitempty"`
	FallbackProvider    string      `json:"fallback_provider,omitempty"`
	StopConditions      []string    `json:"stop_conditions,omitempty"`
}

// stopConditionSet is the closed enum for stop_conditions (doc 07 §4 / VR-11).
var stopConditionSet = map[string]bool{
	"target-met": true, "ceiling": true, "exhausted": true, "timeout": true,
}

// Trigger vocabulary (doc 07 §4).
var triggerSet = map[string]bool{"api": true, "batch": true, "webhook": true}
