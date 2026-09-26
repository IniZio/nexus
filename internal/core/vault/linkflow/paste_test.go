package linkflow_test

import (
	"testing"

	"github.com/IniZio/nexus/internal/core/vault/linkflow"
)

func TestPasteRedirectURLExtractsCode(t *testing.T) {
	raw := "http://localhost:57842/?code=lin_code_xyz&state=expected-state"
	code, err := linkflow.ExtractCode(raw, "expected-state")
	if err != nil {
		t.Fatalf("ExtractCode: %v", err)
	}
	if code != "lin_code_xyz" {
		t.Errorf("code mismatch: got %q want %q", code, "lin_code_xyz")
	}
}

func TestPasteRedirectURLStateMismatch(t *testing.T) {
	raw := "http://localhost:57842/?code=lin_code_xyz&state=wrong"
	_, err := linkflow.ExtractCode(raw, "expected-state")
	if err != linkflow.ErrStateMismatch {
		t.Fatalf("want ErrStateMismatch, got %v", err)
	}
}

func TestPasteRedirectURLMissingCode(t *testing.T) {
	raw := "http://localhost:57842/?state=expected-state"
	_, err := linkflow.ExtractCode(raw, "expected-state")
	if err != linkflow.ErrMissingCode {
		t.Fatalf("want ErrMissingCode, got %v", err)
	}
}
