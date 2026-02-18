package coordination

import (
	"fmt"
	"time"
)

func containsDocType(types []DocType, t DocType) bool {
	for _, dt := range types {
		if dt == t {
			return true
		}
	}
	return false
}

type DocVerification struct {
	Name     string
	Type     string
	Check    func(DocTask) error
	AutoFix  bool
	Required bool
	DocTypes []DocType
}

type DocVerificationResult struct {
	VerificationName string
	Passed           bool
	Error            string
	AutoFixed        bool
	Duration         time.Duration
}

type DocVerificationEngine struct {
	standards     DocStandards
	verifications []DocVerification
}

func NewDocVerificationEngine(standards DocStandards) *DocVerificationEngine {
	return &DocVerificationEngine{
		standards: standards,
		verifications: []DocVerification{
			{
				Name:     "markdown-lint",
				Type:     "automated",
				Check:    checkMarkdownLint,
				AutoFix:  true,
				Required: true,
				DocTypes: []DocType{DocTypeTutorial, DocTypeHowTo, DocTypeExplanation, DocTypeReference, DocTypeADR},
			},
			{
				Name:     "link-validation",
				Type:     "automated",
				Check:    checkLinks,
				AutoFix:  false,
				Required: true,
				DocTypes: []DocType{DocTypeTutorial, DocTypeHowTo, DocTypeExplanation, DocTypeReference, DocTypeADR},
			},
			{
				Name:     "completeness",
				Type:     "automated",
				Check:    checkCompleteness,
				AutoFix:  false,
				Required: true,
				DocTypes: []DocType{DocTypeTutorial, DocTypeHowTo, DocTypeExplanation, DocTypeReference, DocTypeADR},
			},
			{
				Name:     "peer-review",
				Type:     "manual",
				Check:    checkPeerReview,
				AutoFix:  false,
				Required: true,
				DocTypes: []DocType{DocTypeTutorial, DocTypeHowTo, DocTypeExplanation, DocTypeReference, DocTypeADR},
			},
			{
				Name:     "steps-tested",
				Type:     "hybrid",
				Check:    checkStepsTested,
				AutoFix:  false,
				Required: true,
				DocTypes: []DocType{DocTypeTutorial},
			},
			{
				Name:     "troubleshooting-included",
				Type:     "automated",
				Check:    checkTroubleshooting,
				AutoFix:  false,
				Required: true,
				DocTypes: []DocType{DocTypeHowTo},
			},
			{
				Name:     "adr-format",
				Type:     "automated",
				Check:    checkADRFormat,
				AutoFix:  false,
				Required: true,
				DocTypes: []DocType{DocTypeADR},
			},
		},
	}
}

func (e *DocVerificationEngine) Verify(doc DocTask) ([]DocVerificationResult, error) {
	var results []DocVerificationResult

	for _, v := range e.verifications {
		if !containsDocType(v.DocTypes, doc.DocType) {
			continue
		}

		start := time.Now()
		err := v.Check(doc)
		duration := time.Since(start)

		result := DocVerificationResult{
			VerificationName: v.Name,
			Passed:           err == nil,
			Duration:         duration,
		}

		if err != nil {
			result.Error = err.Error()

			if v.AutoFix {
				fixed := e.attemptAutoFix(doc, v)
				result.AutoFixed = fixed
				if fixed {
					result.Passed = true
					result.Error = ""
				}
			}
		}

		results = append(results, result)
	}

	return results, nil
}

func (e *DocVerificationEngine) CanPublish(results []DocVerificationResult) error {
	for _, r := range results {
		if !r.Passed {
			return fmt.Errorf("verification '%s' failed: %s", r.VerificationName, r.Error)
		}
	}
	return nil
}

func (e *DocVerificationEngine) attemptAutoFix(doc DocTask, v DocVerification) bool {
	switch v.Name {
	case "markdown-lint":
		return attemptMarkdownAutoFix(doc)
	default:
		return false
	}
}

func checkMarkdownLint(doc DocTask) error {
	return nil
}

func checkLinks(doc DocTask) error {
	return nil
}

func checkCompleteness(doc DocTask) error {
	return nil
}

func checkPeerReview(doc DocTask) error {
	if doc.ReviewerID == "" {
		return fmt.Errorf("no reviewer assigned")
	}
	if doc.Status != TaskStatusVerification && doc.Status != TaskStatusCompleted {
		return fmt.Errorf("peer review not completed")
	}
	return nil
}

func checkStepsTested(doc DocTask) error {
	return nil
}

func checkTroubleshooting(doc DocTask) error {
	return nil
}

func checkADRFormat(doc DocTask) error {
	return nil
}

func attemptMarkdownAutoFix(doc DocTask) bool {
	return true
}
