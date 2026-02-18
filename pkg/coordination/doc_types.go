package coordination

import (
	"time"
)

// DocType represents the type of documentation
type DocType string

const (
	DocTypeTutorial    DocType = "tutorial"    // docs/tutorials/
	DocTypeHowTo       DocType = "how-to"      // docs/how-to/
	DocTypeReference   DocType = "reference"   // docs/reference/
	DocTypeExplanation DocType = "explanation" // docs/explanation/
	DocTypeADR         DocType = "adr"         // .nexus/decisions/ + docs/dev/decisions/
	DocTypeResearch    DocType = "research"    // Task workspace only
)

// DocTask extends Task with documentation-specific fields
type DocTask struct {
	Task
	DocType         DocType
	TemplateVariant string // A/B test variant ID (e.g., "tutorial-v1")
	DraftPath       string // .nexus/workspaces/<ws>/docs/
	PublishPath     string // docs/{type}/final-name.md
	ADRNumber       int    // Sequential: 001, 002, 003...

	Metrics DocMetrics
}

// DocMetrics tracks A/B test performance
type DocMetrics struct {
	TimeToComplete    time.Duration
	ReviewRounds      int
	VerificationScore float64
	TemplateVariant   string
}

// DocStandards defines quality standards
type DocStandards struct {
	MaxReadingTime         time.Duration
	MaxSections            int
	RequireCodeExamples    bool
	RequireTroubleshooting bool
	MaxLineLength          int
	RequireSummary         bool
	DiataxisCompliance     bool
	RequireDiagrams        bool
}

// Default standards (sane defaults for all projects)
func DefaultDocStandards() DocStandards {
	return DocStandards{
		MaxReadingTime:         10 * time.Minute,
		MaxSections:            7,
		RequireCodeExamples:    true,
		RequireTroubleshooting: true,
		MaxLineLength:          100,
		RequireSummary:         true,
	}
}

// Nexus-specific standards
func NexusDocStandards() DocStandards {
	s := DefaultDocStandards()
	s.DiataxisCompliance = true
	s.RequireDiagrams = true
	return s
}
