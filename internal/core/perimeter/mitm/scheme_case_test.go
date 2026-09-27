package mitm_test

import (
	"encoding/base64"
	"testing"

	"github.com/IniZio/nexus/internal/core/perimeter/mitm"
)

func basicHeader(scheme, user, pass string) string {
	return scheme + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func TestExtractPlaceholder_caseInsensitive(t *testing.T) {
	tests := []struct {
		header string
		want   string
	}{
		{"Bearer MYTOKEN", "MYTOKEN"},
		{"bearer MYTOKEN", "MYTOKEN"},
		{"BEARER MYTOKEN", "MYTOKEN"},
		{"token MYTOKEN", "MYTOKEN"},
		{"TOKEN MYTOKEN", "MYTOKEN"},
		{basicHeader("Basic ", "user", "PASS"), "PASS"},
		{basicHeader("basic ", "user", "PASS"), "PASS"},
		{basicHeader("BASIC ", "user", "PASS"), "PASS"},
		{"unknown MYTOKEN", ""},
		{"", ""},
	}
	for _, tc := range tests {
		got := mitm.ExtractPlaceholderForTest(tc.header)
		if got != tc.want {
			t.Errorf("ExtractPlaceholder(%q) = %q; want %q", tc.header, got, tc.want)
		}
	}
}
