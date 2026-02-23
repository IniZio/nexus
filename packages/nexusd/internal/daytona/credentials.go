package daytona

import (
	"fmt"
	"os"
)

func LoadAPIKey() (string, error) {
	key := os.Getenv("DAYTONA_API_KEY")
	if key == "" {
		return "", fmt.Errorf("DAYTONA_API_KEY environment variable not set")
	}
	return key, nil
}

func ValidateAPIKey(key string) error {
	if key == "" {
		return fmt.Errorf("API key is empty")
	}
	if len(key) < 4 || key[:4] != "dtn_" {
		return fmt.Errorf("invalid API key format (should start with 'dtn_')")
	}
	return nil
}
