// Package e2e provides end-to-end tests for Nexus against the running server.
package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestNexusHealth tests the health endpoint.
func TestNexusHealth(t *testing.T) {
	resp, err := http.Get("http://localhost:3001/api/health")
	if err != nil {
		t.Fatalf("Health check failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Health status = %d, want 200", resp.StatusCode)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	if result["status"] != "healthy" {
		t.Errorf("Status = %v, want healthy", result["status"])
	}
}

// TestFeedbackList tests listing feedback.
func TestFeedbackList(t *testing.T) {
	resp, err := http.Get("http://localhost:3001/api/feedback")
	if err != nil {
		t.Fatalf("Feedback list failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Feedback list status = %d, want 200", resp.StatusCode)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	feedback, ok := result["feedback"].([]interface{})
	if !ok {
		t.Fatal("Response should have feedback array")
	}

	t.Logf("Found %d feedback items", len(feedback))
}

// TestFeedbackStats tests feedback statistics.
func TestFeedbackStats(t *testing.T) {
	resp, err := http.Get("http://localhost:3001/api/feedback/stats")
	if err != nil {
		t.Fatalf("Feedback stats failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Feedback stats status = %d, want 200", resp.StatusCode)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	if result["totalFeedback"] == nil {
		t.Error("Response should have totalFeedback")
	}

	avgSat, ok := result["averageSatisfaction"].(float64)
	if !ok {
		t.Error("Response should have averageSatisfaction")
	}

	t.Logf("Total feedback: %.0f, Avg satisfaction: %.2f", result["totalFeedback"].(float64), avgSat)
}

// TestAnalyticsDashboard tests analytics dashboard.
func TestAnalyticsDashboard(t *testing.T) {
	resp, err := http.Get("http://localhost:3001/api/analytics/dashboard")
	if err != nil {
		t.Fatalf("Dashboard request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Dashboard status = %d, want 200", resp.StatusCode)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	if result["generatedAt"] == nil {
		t.Error("Response should have generatedAt")
	}

	usage, ok := result["usage"].(map[string]interface{})
	if !ok {
		t.Fatal("Response should have usage object")
	}

	t.Logf("Total sessions: %.0f", usage["totalSessions"].(float64))
}

// TestWorkflowAnalytics tests workflow analytics.
func TestWorkflowAnalytics(t *testing.T) {
	resp, err := http.Get("http://localhost:3001/api/analytics/workflow")
	if err != nil {
		t.Fatalf("Workflow analytics failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Workflow status = %d, want 200", resp.StatusCode)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	if result["totalWorkflows"] == nil {
		t.Error("Response should have totalWorkflows")
	}

	t.Logf("Total workflows: %.0f, Success rate: %.2f%%",
		result["totalWorkflows"].(float64),
		result["successRate"].(float64))
}

// TestRecommendations tests recommendations endpoint.
func TestRecommendations(t *testing.T) {
	resp, err := http.Get("http://localhost:3001/api/analytics/recommendations")
	if err != nil {
		t.Fatalf("Recommendations failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Recommendations status = %d, want 200", resp.StatusCode)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	if result["recommendations"] == nil {
		t.Error("Response should have recommendations")
	}

	t.Logf("Generated at: %s", result["generatedAt"])
}

// TestUsageAnalytics tests usage analytics.
func TestUsageAnalytics(t *testing.T) {
	resp, err := http.Get("http://localhost:3001/api/analytics/usage")
	if err != nil {
		t.Fatalf("Usage analytics failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Usage status = %d, want 200", resp.StatusCode)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	if result["totalSessions"] == nil {
		t.Error("Response should have totalSessions")
	}

	skills, ok := result["skillsFrequency"].(map[string]interface{})
	if !ok {
		t.Fatal("Response should have skillsFrequency")
	}

	t.Logf("Total sessions: %.0f, Skills tracked: %d",
		result["totalSessions"].(float64), len(skills))
}

// TestPulseAnalytics tests Pulse analytics.
func TestPulseAnalytics(t *testing.T) {
	resp, err := http.Get("http://localhost:3001/api/analytics/pulse")
	if err != nil {
		t.Fatalf("Pulse analytics failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Pulse status = %d, want 200", resp.StatusCode)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	if result["tasksCreated"] == nil {
		t.Error("Response should have tasksCreated")
	}

	t.Logf("Tasks created: %.0f, Completed: %.0f",
		result["tasksCreated"].(float64),
		result["tasksCompleted"].(float64))
}

// TestAllEndpoints tests all endpoints return valid JSON.
func TestAllEndpoints(t *testing.T) {
	endpoints := []string{
		"/api/health",
		"/api/feedback",
		"/api/feedback/stats",
		"/api/analytics/dashboard",
		"/api/analytics/workflow",
		"/api/analytics/recommendations",
		"/api/analytics/usage",
		"/api/analytics/pulse",
	}

	for _, ep := range endpoints {
		resp, err := http.Get("http://localhost:3001" + ep)
		if err != nil {
			t.Errorf("Endpoint %s failed: %v", ep, err)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode != 200 {
			t.Errorf("Endpoint %s status = %d, want 200", ep, resp.StatusCode)
		}
	}
}

// TestFeedbackByCategory tests feedback filtering by category.
func TestFeedbackByCategory(t *testing.T) {
	resp, err := http.Get("http://localhost:3001/api/feedback?category=workflow")
	if err != nil {
		t.Fatalf("Filtered feedback failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Filtered feedback status = %d, want 200", resp.StatusCode)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	t.Logf("Filtered feedback category=workflow: OK")
}

// TestFeedbackByType tests feedback filtering by type.
func TestFeedbackByType(t *testing.T) {
	resp, err := http.Get("http://localhost:3001/api/feedback?type=praise")
	if err != nil {
		t.Fatalf("Type filtered feedback failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Type filtered feedback status = %d, want 200", resp.StatusCode)
	}

	t.Logf("Filtered feedback type=praise: OK")
}

// TestDashboardPeriod tests dashboard with different periods.
func TestDashboardPeriod(t *testing.T) {
	periods := []string{"7d", "30d", "90d"}

	for _, period := range periods {
		url := "http://localhost:3001/api/analytics/dashboard?period=" + period
		resp, err := http.Get(url)
		if err != nil {
			t.Errorf("Dashboard period %s failed: %v", period, err)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode != 200 {
			t.Errorf("Dashboard period %s status = %d, want 200", period, resp.StatusCode)
		}
	}

	t.Logf("Dashboard periods (7d, 30d, 90d): OK")
}

// TestEmptyQueryParams tests handling of empty/missing query params.
func TestEmptyQueryParams(t *testing.T) {
	// Should not crash on empty params
	resp, err := http.Get("http://localhost:3001/api/feedback?category=")
	if err != nil {
		t.Errorf("Empty param failed: %v", err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("Empty param status = %d, want 200", resp.StatusCode)
		}
	}

	resp, err = http.Get("http://localhost:3001/api/feedback?type=")
	if err != nil {
		t.Errorf("Empty type param failed: %v", err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("Empty type param status = %d, want 200", resp.StatusCode)
		}
	}

	t.Logf("Empty query params: OK")
}

// TestResponseHeaders tests response headers.
func TestResponseHeaders(t *testing.T) {
	resp, err := http.Get("http://localhost:3001/api/health")
	if err != nil {
		t.Fatalf("Health check failed: %v", err)
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		t.Errorf("Content-Type = %s, want application/json", contentType)
	}

	t.Logf("Content-Type: %s", contentType)
}
