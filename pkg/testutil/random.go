// Package testutil provides utilities for testing
package testutil

import (
	"fmt"
	"math/rand"
	"time"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

// RandomString generates a random string of given length
func RandomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}

// RandomWorkspaceName generates a unique workspace name for testing
func RandomWorkspaceName() string {
	return fmt.Sprintf("test-ws-%s-%d", RandomString(8), time.Now().UnixNano())
}

// RandomTaskTitle generates a random task title
func RandomTaskTitle() string {
	titles := []string{
		"Implement user authentication",
		"Add database migration",
		"Fix memory leak in worker",
		"Update API documentation",
		"Refactor config loading",
		"Add metrics collection",
		"Optimize query performance",
		"Fix race condition",
		"Add integration tests",
		"Update dependencies",
	}
	return titles[rand.Intn(len(titles))] + " " + RandomString(5)
}

// RandomTaskDescription generates a random task description
func RandomTaskDescription() string {
	descriptions := []string{
		"This task requires careful handling of edge cases",
		"Need to ensure backward compatibility",
		"Performance critical - use benchmarks",
		"Security sensitive - requires review",
		"Documentation must be updated",
		"Tests must be included",
	}
	return descriptions[rand.Intn(len(descriptions))]
}

// RandomPriority returns a random priority level
func RandomPriority() string {
	priorities := []string{"low", "medium", "high", "critical"}
	return priorities[rand.Intn(len(priorities))]
}

// RandomPort returns a random port number between 10000-65000
func RandomPort() int {
	return 10000 + rand.Intn(55000)
}

// RandomAgentName generates a random agent name
func RandomAgentName() string {
	names := []string{"executor", "builder", "tester", "reviewer", "deployer"}
	return fmt.Sprintf("%s-%s", names[rand.Intn(len(names))], RandomString(5))
}

// RandomCapabilities returns a random set of capabilities
func RandomCapabilities() []string {
	allCaps := []string{"go", "python", "nodejs", "docker", "kubernetes", "terraform", "aws", "gcp"}
	numCaps := 1 + rand.Intn(len(allCaps))
	caps := make([]string, numCaps)
	for i := 0; i < numCaps; i++ {
		caps[i] = allCaps[rand.Intn(len(allCaps))]
	}
	return caps
}

// RandomExitCode returns a random exit code (0 or 1-255)
func RandomExitCode() int {
	if rand.Float32() < 0.7 {
		return 0
	}
	return 1 + rand.Intn(255)
}

// RandomMultiLineOutput generates random multi-line text
func RandomMultiLineOutput(lines int) string {
	result := ""
	for i := 0; i < lines; i++ {
		result += fmt.Sprintf("Line %d: %s\n", i+1, RandomString(20))
	}
	return result
}
