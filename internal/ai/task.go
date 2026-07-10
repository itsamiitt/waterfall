package ai

import (
	"fmt"
	"strings"
)

// TaskType is a specialized AI research agent task (ADR-0026 / 04-ai-pipeline.md). Each task binds
// an input contract, a Prompt Version, and a typed output struct validated by struct-based checks.
// The full task catalog + prompt binding + the orchestrator DAG land with slice 23; this file pins
// the enum and the output-validation pattern that slice 21's cascade exercises.
type TaskType string

const (
	TaskCompanyResearch TaskType = "company_research"
	TaskNews            TaskType = "news"
	TaskIntent          TaskType = "intent"
	TaskTechnology      TaskType = "technology"
	TaskSEO             TaskType = "seo"
	TaskCompetitor      TaskType = "competitor"
	TaskHiring          TaskType = "hiring"
	TaskMarket          TaskType = "market"
	TaskSummarization   TaskType = "summarization"
	TaskJSONValidation  TaskType = "json_validation"
)

// AllTaskTypes lists every task type in stable order (validation + catalog projection).
func AllTaskTypes() []TaskType {
	return []TaskType{
		TaskCompanyResearch, TaskNews, TaskIntent, TaskTechnology, TaskSEO,
		TaskCompetitor, TaskHiring, TaskMarket, TaskSummarization, TaskJSONValidation,
	}
}

// Valid reports whether t is a known task type.
func (t TaskType) Valid() bool {
	for _, k := range AllTaskTypes() {
		if k == t {
			return true
		}
	}
	return false
}

// --- Example typed outputs (the pattern; the full catalog lands with the orchestrator, slice 23).
// All values are AI-proposed (source_type=ai_inference, ADR-0026/0028) and are never fused as
// high-confidence facts by downstream consumers.

// CompanyResearchOutput is the company_research agent's contract: a short factual summary plus
// structured firmographic guesses the pipeline may cross-check against sourced Fields.
type CompanyResearchOutput struct {
	Summary  string   `json:"summary"`
	Industry string   `json:"industry"`
	Products []string `json:"products"`
	Keywords []string `json:"search_keywords"`
}

// Validate enforces the company_research contract.
func (o CompanyResearchOutput) Validate() error {
	if strings.TrimSpace(o.Summary) == "" {
		return fmt.Errorf("company_research: empty summary")
	}
	if len(o.Summary) > 4000 {
		return fmt.Errorf("company_research: summary too long (%d chars)", len(o.Summary))
	}
	return nil
}

// CompetitorListOutput is the competitor agent's contract: a bounded list of competitors. This is
// multi-valued → it lives only in the Dossier JSON, never a canonical Field (ADR-0028).
type CompetitorListOutput struct {
	Competitors []Competitor `json:"competitors"`
}

// Competitor is one competitor entry.
type Competitor struct {
	Name   string `json:"name"`
	Domain string `json:"domain"`
	Reason string `json:"reason"`
}

// Validate enforces the competitor contract.
func (o CompetitorListOutput) Validate() error {
	if len(o.Competitors) == 0 {
		return fmt.Errorf("competitor: empty list")
	}
	if len(o.Competitors) > 50 {
		return fmt.Errorf("competitor: too many (%d)", len(o.Competitors))
	}
	for i, c := range o.Competitors {
		if strings.TrimSpace(c.Name) == "" {
			return fmt.Errorf("competitor[%d]: empty name", i)
		}
	}
	return nil
}
