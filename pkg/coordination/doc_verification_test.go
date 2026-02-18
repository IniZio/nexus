package coordination

import (
	"errors"
	"testing"
	"time"
)

func TestDocVerificationEngine_Verify_RunsCorrectVerifications(t *testing.T) {
	standards := DefaultDocStandards()
	engine := NewDocVerificationEngine(standards)

	t.Run("tutorial_runs_all_generic_and_tutorial_specific", func(t *testing.T) {
		doc := DocTask{
			Task: Task{
				ID:         "test-1",
				Title:      "Test Tutorial",
				ReviewerID: "reviewer-1",
				Status:     TaskStatusVerification,
			},
			DocType: DocTypeTutorial,
		}

		results, err := engine.Verify(doc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expectedVerifications := []string{
			"markdown-lint", "link-validation", "completeness",
			"peer-review", "steps-tested",
		}

		found := make(map[string]bool)
		for _, r := range results {
			found[r.VerificationName] = true
		}

		for _, name := range expectedVerifications {
			if !found[name] {
				t.Errorf("expected verification %q not found", name)
			}
		}

		if len(results) != 5 {
			t.Errorf("expected 5 verifications, got %d", len(results))
		}
	})

	t.Run("howto_runs_all_generic_and_howto_specific", func(t *testing.T) {
		doc := DocTask{
			Task: Task{
				ID:         "test-2",
				Title:      "Test How-To",
				ReviewerID: "reviewer-1",
				Status:     TaskStatusVerification,
			},
			DocType: DocTypeHowTo,
		}

		results, err := engine.Verify(doc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expectedVerifications := []string{
			"markdown-lint", "link-validation", "completeness",
			"peer-review", "troubleshooting-included",
		}

		found := make(map[string]bool)
		for _, r := range results {
			found[r.VerificationName] = true
		}

		for _, name := range expectedVerifications {
			if !found[name] {
				t.Errorf("expected verification %q not found", name)
			}
		}

		if len(results) != 5 {
			t.Errorf("expected 5 verifications, got %d", len(results))
		}
	})

	t.Run("adr_runs_all_generic_and_adr_specific", func(t *testing.T) {
		doc := DocTask{
			Task: Task{
				ID:         "test-3",
				Title:      "Test ADR",
				ReviewerID: "reviewer-1",
				Status:     TaskStatusVerification,
			},
			DocType: DocTypeADR,
		}

		results, err := engine.Verify(doc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expectedVerifications := []string{
			"markdown-lint", "link-validation", "completeness",
			"peer-review", "adr-format",
		}

		found := make(map[string]bool)
		for _, r := range results {
			found[r.VerificationName] = true
		}

		for _, name := range expectedVerifications {
			if !found[name] {
				t.Errorf("expected verification %q not found", name)
			}
		}

		if len(results) != 5 {
			t.Errorf("expected 5 verifications, got %d", len(results))
		}
	})

	t.Run("reference_only_runs_generic_verifications", func(t *testing.T) {
		doc := DocTask{
			Task: Task{
				ID:         "test-4",
				Title:      "Test Reference",
				ReviewerID: "reviewer-1",
				Status:     TaskStatusVerification,
			},
			DocType: DocTypeReference,
		}

		results, err := engine.Verify(doc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expectedVerifications := []string{
			"markdown-lint", "link-validation", "completeness", "peer-review",
		}

		found := make(map[string]bool)
		for _, r := range results {
			found[r.VerificationName] = true
		}

		for _, name := range expectedVerifications {
			if !found[name] {
				t.Errorf("expected verification %q not found", name)
			}
		}

		if len(results) != 4 {
			t.Errorf("expected 4 verifications, got %d", len(results))
		}
	})

	t.Run("explanation_runs_generic_verifications", func(t *testing.T) {
		doc := DocTask{
			Task: Task{
				ID:         "test-5",
				Title:      "Test Explanation",
				ReviewerID: "reviewer-1",
				Status:     TaskStatusVerification,
			},
			DocType: DocTypeExplanation,
		}

		results, err := engine.Verify(doc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expectedVerifications := []string{
			"markdown-lint", "link-validation", "completeness", "peer-review",
		}

		found := make(map[string]bool)
		for _, r := range results {
			found[r.VerificationName] = true
		}

		for _, name := range expectedVerifications {
			if !found[name] {
				t.Errorf("expected verification %q not found", name)
			}
		}

		if len(results) != 4 {
			t.Errorf("expected 4 verifications, got %d", len(results))
		}
	})
}

func TestDocVerificationEngine_CanPublish_BlocksOnFailedChecks(t *testing.T) {
	standards := DefaultDocStandards()
	engine := NewDocVerificationEngine(standards)

	t.Run("returns_nil_when_all_passed", func(t *testing.T) {
		results := []DocVerificationResult{
			{VerificationName: "markdown-lint", Passed: true},
			{VerificationName: "link-validation", Passed: true},
			{VerificationName: "completeness", Passed: true},
			{VerificationName: "peer-review", Passed: true},
		}

		err := engine.CanPublish(results)
		if err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
	})

	t.Run("returns_error_when_any_failed", func(t *testing.T) {
		results := []DocVerificationResult{
			{VerificationName: "markdown-lint", Passed: true},
			{VerificationName: "link-validation", Passed: false, Error: "broken link found"},
			{VerificationName: "completeness", Passed: true},
			{VerificationName: "peer-review", Passed: true},
		}

		err := engine.CanPublish(results)
		if err == nil {
			t.Error("expected error, got nil")
		}
	})

	t.Run("returns_error_with_verification_name", func(t *testing.T) {
		results := []DocVerificationResult{
			{VerificationName: "markdown-lint", Passed: false, Error: "formatting error"},
		}

		err := engine.CanPublish(results)
		if err == nil {
			t.Error("expected error, got nil")
		}

		expected := "verification 'markdown-lint' failed: formatting error"
		if err.Error() != expected {
			t.Errorf("expected error %q, got %q", expected, err.Error())
		}
	})

	t.Run("returns_error_when_multiple_failed", func(t *testing.T) {
		results := []DocVerificationResult{
			{VerificationName: "markdown-lint", Passed: false, Error: "lint error"},
			{VerificationName: "link-validation", Passed: false, Error: "broken link"},
			{VerificationName: "completeness", Passed: false, Error: "incomplete section"},
		}

		err := engine.CanPublish(results)
		if err == nil {
			t.Error("expected error, got nil")
		}
	})
}

func TestDocVerificationEngine_CustomEngine_AutoFix(t *testing.T) {
	t.Run("auto_fix_verification_succeeds_after_failure", func(t *testing.T) {
		engine := &DocVerificationEngine{
			standards: DefaultDocStandards(),
			verifications: []DocVerification{
				{
					Name:     "markdown-lint",
					Type:     "automated",
					Check:    func(doc DocTask) error { return errors.New("test failure") },
					AutoFix:  true,
					Required: true,
					DocTypes: []DocType{DocTypeTutorial},
				},
			},
		}

		doc := DocTask{
			Task: Task{
				ID:         "test-autofix-doc",
				Title:      "Test Doc",
				ReviewerID: "reviewer-1",
				Status:     TaskStatusVerification,
			},
			DocType: DocTypeTutorial,
		}

		results, err := engine.Verify(doc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}

		result := results[0]
		if !result.AutoFixed {
			t.Errorf("expected verification to be auto-fixed")
		}
		if !result.Passed {
			t.Errorf("expected verification to pass after auto-fix")
		}
	})

	t.Run("non_autofix_verification_fails", func(t *testing.T) {
		engine := &DocVerificationEngine{
			standards: DefaultDocStandards(),
			verifications: []DocVerification{
				{
					Name:     "test-no-autofix",
					Type:     "automated",
					Check:    func(doc DocTask) error { return errors.New("broken link") },
					AutoFix:  false,
					Required: true,
					DocTypes: []DocType{DocTypeTutorial},
				},
			},
		}

		doc := DocTask{
			Task: Task{
				ID:         "test-no-autofix-doc",
				Title:      "Test Doc",
				ReviewerID: "reviewer-1",
				Status:     TaskStatusVerification,
			},
			DocType: DocTypeTutorial,
		}

		results, err := engine.Verify(doc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}

		result := results[0]
		if result.AutoFixed {
			t.Errorf("expected verification to NOT auto-fix")
		}
		if result.Passed {
			t.Errorf("expected verification to fail")
		}
		if result.Error != "broken link" {
			t.Errorf("expected error message, got %q", result.Error)
		}
	})
}

func TestDocVerificationEngine_Verify_DurationTracking(t *testing.T) {
	standards := DefaultDocStandards()
	engine := NewDocVerificationEngine(standards)

	doc := DocTask{
		Task: Task{
			ID:         "test-duration",
			Title:      "Test Doc",
			ReviewerID: "reviewer-1",
			Status:     TaskStatusVerification,
		},
		DocType: DocTypeTutorial,
	}

	results, err := engine.Verify(doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, r := range results {
		if r.Duration < 0 {
			t.Errorf("negative duration for %q: %v", r.VerificationName, r.Duration)
		}
	}
}

func TestCheckPeerReview(t *testing.T) {
	t.Run("fails_when_no_reviewer", func(t *testing.T) {
		doc := DocTask{
			Task: Task{
				ID:     "test-no-reviewer",
				Title:  "Test Doc",
				Status: TaskStatusVerification,
			},
			DocType: DocTypeTutorial,
		}

		err := checkPeerReview(doc)
		if err == nil {
			t.Error("expected error for no reviewer")
		}
	})

	t.Run("fails_when_not_in_verification_or_completed", func(t *testing.T) {
		doc := DocTask{
			Task: Task{
				ID:         "test-wrong-status",
				Title:      "Test Doc",
				ReviewerID: "reviewer-1",
				Status:     TaskStatusInProgress,
			},
			DocType: DocTypeTutorial,
		}

		err := checkPeerReview(doc)
		if err == nil {
			t.Error("expected error for wrong status")
		}
	})

	t.Run("passes_when_reviewer_and_verification_status", func(t *testing.T) {
		doc := DocTask{
			Task: Task{
				ID:         "test-valid",
				Title:      "Test Doc",
				ReviewerID: "reviewer-1",
				Status:     TaskStatusVerification,
			},
			DocType: DocTypeTutorial,
		}

		err := checkPeerReview(doc)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("passes_when_completed_status", func(t *testing.T) {
		doc := DocTask{
			Task: Task{
				ID:         "test-completed",
				Title:      "Test Doc",
				ReviewerID: "reviewer-1",
				Status:     TaskStatusCompleted,
			},
			DocType: DocTypeTutorial,
		}

		err := checkPeerReview(doc)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestContainsDocType(t *testing.T) {
	types := []DocType{DocTypeTutorial, DocTypeHowTo, DocTypeReference}

	if !containsDocType(types, DocTypeTutorial) {
		t.Error("expected containsDocType to return true for DocTypeTutorial")
	}

	if !containsDocType(types, DocTypeHowTo) {
		t.Error("expected containsDocType to return true for DocTypeHowTo")
	}

	if !containsDocType(types, DocTypeReference) {
		t.Error("expected containsDocType to return true for DocTypeReference")
	}

	if containsDocType(types, DocTypeADR) {
		t.Error("expected containsDocType to return false for DocTypeADR")
	}

	if containsDocType(types, DocTypeExplanation) {
		t.Error("expected containsDocType to return false for DocTypeExplanation")
	}

	empty := []DocType{}
	if containsDocType(empty, DocTypeTutorial) {
		t.Error("expected containsDocType to return false for empty slice")
	}
}

func TestDocVerificationEngine_Verify_Integration(t *testing.T) {
	standards := NexusDocStandards()
	engine := NewDocVerificationEngine(standards)

	doc := DocTask{
		Task: Task{
			ID:          "integration-test",
			Title:       "Complete Integration Test Doc",
			ReviewerID:  "reviewer-1",
			Status:      TaskStatusCompleted,
			WorkspaceID: "ws-1",
		},
		DocType:     DocTypeTutorial,
		DraftPath:   ".nexus/workspaces/ws-1/docs/test.md",
		PublishPath: "docs/tutorials/test.md",
	}

	results, err := engine.Verify(doc)
	if err != nil {
		t.Fatalf("unexpected error during verify: %v", err)
	}

	if len(results) == 0 {
		t.Error("expected verifications to run")
	}

	var failed int
	for _, r := range results {
		if !r.Passed {
			failed++
			t.Logf("verification %q failed: %s", r.VerificationName, r.Error)
		}
	}

	if failed > 0 {
		t.Logf("%d verifications failed", failed)
	}

	publishErr := engine.CanPublish(results)
	if failed > 0 {
		if publishErr == nil {
			t.Error("expected CanPublish to fail when verifications failed")
		}
	} else {
		if publishErr != nil {
			t.Errorf("expected CanPublish to pass, got: %v", publishErr)
		}
	}
}

func TestDocVerificationResult_Fields(t *testing.T) {
	result := DocVerificationResult{
		VerificationName: "test-verification",
		Passed:           true,
		Error:            "",
		AutoFixed:        false,
		Duration:         100 * time.Millisecond,
	}

	if result.VerificationName != "test-verification" {
		t.Errorf("expected VerificationName, got %q", result.VerificationName)
	}

	if !result.Passed {
		t.Error("expected Passed to be true")
	}

	if result.Error != "" {
		t.Errorf("expected empty Error, got %q", result.Error)
	}

	if result.AutoFixed {
		t.Error("expected AutoFixed to be false")
	}

	if result.Duration != 100*time.Millisecond {
		t.Errorf("expected Duration, got %v", result.Duration)
	}
}

func TestDocVerificationEngine_SkipNonApplicable(t *testing.T) {
	standards := DefaultDocStandards()
	engine := NewDocVerificationEngine(standards)

	t.Run("tutorial_skips_howto_and_adr_specific", func(t *testing.T) {
		doc := DocTask{
			Task: Task{
				ID:         "test-skip",
				Title:      "Test Tutorial",
				ReviewerID: "reviewer-1",
				Status:     TaskStatusVerification,
			},
			DocType: DocTypeTutorial,
		}

		results, err := engine.Verify(doc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for _, r := range results {
			if r.VerificationName == "troubleshooting-included" {
				t.Error("tutorial should not run troubleshooting-included check")
			}
			if r.VerificationName == "adr-format" {
				t.Error("tutorial should not run adr-format check")
			}
		}
	})

	t.Run("howto_skips_tutorial_and_adr_specific", func(t *testing.T) {
		doc := DocTask{
			Task: Task{
				ID:         "test-skip-howto",
				Title:      "Test How-To",
				ReviewerID: "reviewer-1",
				Status:     TaskStatusVerification,
			},
			DocType: DocTypeHowTo,
		}

		results, err := engine.Verify(doc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for _, r := range results {
			if r.VerificationName == "steps-tested" {
				t.Error("howto should not run steps-tested check")
			}
			if r.VerificationName == "adr-format" {
				t.Error("howto should not run adr-format check")
			}
		}
	})

	t.Run("adr_skips_tutorial_and_howto_specific", func(t *testing.T) {
		doc := DocTask{
			Task: Task{
				ID:         "test-skip-adr",
				Title:      "Test ADR",
				ReviewerID: "reviewer-1",
				Status:     TaskStatusVerification,
			},
			DocType: DocTypeADR,
		}

		results, err := engine.Verify(doc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for _, r := range results {
			if r.VerificationName == "steps-tested" {
				t.Error("adr should not run steps-tested check")
			}
			if r.VerificationName == "troubleshooting-included" {
				t.Error("adr should not run troubleshooting-included check")
			}
		}
	})
}

func TestDocVerificationEngine_Verify_ErrorMessage(t *testing.T) {
	customError := errors.New("custom verification error")

	engine := &DocVerificationEngine{
		standards: DefaultDocStandards(),
		verifications: []DocVerification{
			{
				Name:     "custom-check",
				Type:     "automated",
				Check:    func(doc DocTask) error { return customError },
				AutoFix:  false,
				Required: true,
				DocTypes: []DocType{DocTypeTutorial},
			},
		},
	}

	doc := DocTask{
		Task: Task{
			ID:         "test-error-msg",
			Title:      "Test Doc",
			ReviewerID: "reviewer-1",
			Status:     TaskStatusVerification,
		},
		DocType: DocTypeTutorial,
	}

	results, err := engine.Verify(doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if results[0].Error != "custom verification error" {
		t.Errorf("expected error message, got %q", results[0].Error)
	}
}
