package main

import (
	"encoding/json"
	"fmt"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// parseBuilderToolRecipe decodes the JSON value from --tool-recipe=<json>
// into a [cred.ToolRecipe]. An empty jsonStr returns a zero-value recipe and
// nil error — the caller treats that as "no recipe requested".
//
// This is the sole decode site for the --tool-recipe flag; cmd/nexus3-agent
// main() delegates here so the parse logic is independently testable without
// booting a VM.
func parseBuilderToolRecipe(jsonStr string) (cred.ToolRecipe, error) {
	if jsonStr == "" {
		return cred.ToolRecipe{}, nil
	}
	var recipe cred.ToolRecipe
	if err := json.Unmarshal([]byte(jsonStr), &recipe); err != nil {
		return cred.ToolRecipe{}, fmt.Errorf("parse --tool-recipe: %w", err)
	}
	return recipe, nil
}
