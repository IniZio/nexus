package main

import (
	"fmt"
	"path/filepath"
)

func errNotAbsProjectRoot(what, raw string) error {
	abs, err := filepath.Abs(raw)
	if err != nil {
		return fmt.Errorf("%s must be an absolute path (could not resolve %q: %v)", what, raw, err)
	}
	return fmt.Errorf("%s must be an absolute path (got %q); try: --project-root %q", what, raw, abs)
}
