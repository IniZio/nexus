package coordination

import (
	"math/rand"
	"time"
)

type TemplateVariant struct {
	ID          string
	Name        string
	Description string
	Content     string
	Metrics     TemplateMetrics
}

type TemplateMetrics struct {
	TasksCreated      int
	CompletedCount    int
	FailedCount       int
	AvgTimeToComplete time.Duration
	AvgReviewRounds   float64
}

func (m TemplateMetrics) CompletionRate() float64 {
	if m.TasksCreated == 0 {
		return 0
	}
	return float64(m.CompletedCount) / float64(m.TasksCreated)
}

type DocTemplateRegistry struct {
	Variants map[DocType][]TemplateVariant
}

func (r *DocTemplateRegistry) GetVariants(docType DocType) []TemplateVariant {
	return r.Variants[docType]
}

func (r *DocTemplateRegistry) SelectVariant(docType DocType) string {
	variants := r.GetVariants(docType)
	if len(variants) == 0 {
		return ""
	}

	totalCreated := 0
	for _, v := range variants {
		totalCreated += v.Metrics.TasksCreated
	}

	if totalCreated < 10 {
		idx := rand.Intn(len(variants))
		return variants[idx].ID
	}

	best := variants[0]
	for _, v := range variants[1:] {
		if v.Metrics.CompletionRate() > best.Metrics.CompletionRate() {
			best = v
		}
	}
	return best.ID
}

func NewDocTemplateRegistry() *DocTemplateRegistry {
	return &DocTemplateRegistry{
		Variants: map[DocType][]TemplateVariant{
			DocTypeTutorial: {
				{
					ID:          "tutorial-v1",
					Name:        "Step-by-Step",
					Description: "Traditional step-by-step tutorial",
					Content: `# {{ .Title }}

## Overview
{{ .Overview }}

## Prerequisites
{{ .Prerequisites }}

{{ range .Steps }}
## Step {{ .Number }}: {{ .Title }}
{{ .Content }}
{{ if .Code }}
` + "```" + `{{ .Language }}
{{ .Code }}
` + "```" + `
{{ end }}
{{ end }}

## Summary
{{ .Summary }}

## Next Steps
{{ .NextSteps }}
`,
				},
				{
					ID:          "tutorial-v2",
					Name:        "Goal-First",
					Description: "Show final result first, then explain steps",
					Content: `# {{ .Title }}

## What You'll Build
{{ .FinalResult }}

## Prerequisites
{{ .Prerequisites }}

## Try It
{{ .QuickDemo }}

## How It Works
{{ range .Steps }}
### {{ .Title }}
{{ .Content }}
{{ end }}

## Next Steps
{{ .NextSteps }}
`,
				},
			},
			DocTypeHowTo: {
				{
					ID:          "howto-v1",
					Name:        "Problem-Solution",
					Description: "State the problem clearly, then provide solution",
					Content: `# How to {{ .TaskName }}

## Problem
{{ .ProblemStatement }}

## Solution
{{ .SolutionOverview }}

## Prerequisites
{{ .Prerequisites }}

## Steps
{{ range .Steps }}
### {{ .StepNumber }}. {{ .Title }}
{{ .Instruction }}

{{ if .CodeExample }}
` + "```" + `{{ .Language }}
{{ .CodeExample }}
` + "```" + `
{{ end }}
{{ end }}

## Verification
{{ .VerificationSteps }}

## Troubleshooting
{{ .CommonIssues }}
`,
				},
				{
					ID:          "howto-v2",
					Name:        "Scenario-First",
					Description: "Start with a real-world scenario, then solution",
					Content: `# {{ .ScenarioTitle }}

## When You Need This
{{ .UseCaseDescription }}

## Quick Answer
{{ .QuickSolution }}

## Step-by-Step
{{ range .Steps }}
### {{ .StepNumber }}. {{ .Title }}
{{ .Action }}

{{ if .Example }}
` + "```" + `{{ .Language }}
{{ .Example }}
` + "```" + `
{{ end }}
{{ end }}

## What If
{{ .AlternativeScenarios }}

## Related Tasks
{{ .RelatedTasks }}
`,
				},
			},
			DocTypeExplanation: {
				{
					ID:          "explanation-v1",
					Name:        "Concept-First",
					Description: "Explain the concept before details",
					Content: `# {{ .ConceptName }}

## Overview
{{ .HighLevelDescription }}

## Why It Matters
{{ .Importance }}

## Core Concepts
{{ range .Concepts }}
### {{ .Name }}
{{ .Explanation }}

{{ if .Diagram }}
![{{ .Name }}]({{ .Diagram }})
{{ end }}
{{ end }}

## How It Works
{{ .TechnicalDetails }}

## Trade-offs
{{ .ProsCons }}

## When to Use
{{ .UseCases }}
`,
				},
				{
					ID:          "explanation-v2",
					Name:        "Analogy-First",
					Description: "Use analogies to explain before technical details",
					Content: `# {{ .ConceptName }}

## The Big Picture
{{ .Analogy }}

## In Practice
{{ .RealWorldExample }}

## Under the Hood
{{ .TechnicalExplanation }}

## Key Ideas
{{ range .KeyPoints }}
- **{{ .Title }}**: {{ .Description }}
{{ end }}

## Comparing Options
{{ .Comparisons }}

## Learn More
{{ .FurtherReading }}
`,
				},
			},
			DocTypeReference: {
				{
					ID:          "reference-v1",
					Name:        "Structured-Reference",
					Description: "Full structured reference documentation",
					Content: `# {{ .API_Name }} Reference

## Overview
{{ .Description }}

## Installation
{{ .InstallationInstructions }}

## Configuration
{{ .ConfigurationOptions }}

## API Reference
{{ range .Endpoints }}
### {{ .Method }} {{ .Path }}
**Description:** {{ .Description }}

**Parameters:**
{{ range .Parameters }}
- ` + "`" + `{{ .Name }}` + "`" + ` ({{ .Type }}): {{ .Description }}
{{ end }}

**Response:**
` + "```" + `json
{{ .ResponseExample }}
` + "```" + `

**Errors:**
{{ range .Errors }}
- {{ .Code }}: {{ .Message }}
{{ end }}
{{ end }}

## Examples
{{ range .Examples }}
### {{ .Title }}
` + "```" + `{{ .Language }}
{{ .Code }}
` + "```" + `
{{ end }}

## CLI Commands
{{ range .Commands }}
### {{ .Name }}
{{ .Description }}

Usage: ` + "`" + `{{ .Usage }}` + "`" + `
{{ end }}

## Environment Variables
{{ range .EnvVars }}
- ` + "`" + `{{ .Name }}` + "`" + `: {{ .Description }} (default: {{ .Default }})
{{ end }}
`,
				},
				{
					ID:          "reference-v2",
					Name:        "Quick-Reference",
					Description: "Concise quick reference format",
					Content: `# {{ .API_Name }}

{{ .OneLineDescription }}

## Quick Start
` + "```" + `{{ .Language }}
{{ .QuickStartCode }}
` + "```" + `

## Signatures

| Item | Details |
|------|---------|
{{ range .Items }}
| ` + "`" + `{{ .Name }}` + "`" + ` | {{ .Description }} |
{{ end }}

## Options

{{ range .Options }}
- ` + "`" + `{{ .Flag }}` + "`" + ` {{ .Description }}
{{ end }}

## Examples

{{ range .Examples }}
` + "```" + `{{ .Language }}
{{ .Code }}
` + "```" + `
// {{ .Explanation }}
{{ end }}

## See Also
{{ .RelatedLinks }}
`,
				},
			},
		},
	}
}
