package feedback

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FeedbackStore contains all feedback entries
type FeedbackStore struct {
	Feedbacks []Feedback  `json:"feedbacks"`
	Metadata  StoreMetadata `json:"metadata"`
}

// StoreMetadata contains store metadata
type StoreMetadata struct {
	Version     string `json:"version"`
	LastUpdated string `json:"lastUpdated"`
}

// FeedbackCollector manages feedback collection and storage
type FeedbackCollector struct {
	filePath string
	store    *FeedbackStore
	mu       sync.RWMutex
}

// NewCollector creates a new FeedbackCollector
func NewCollector(basePath string) *FeedbackCollector {
	return &FeedbackCollector{
		filePath: filepath.Join(basePath, ".nexus", "feedback.json"),
		store:    &FeedbackStore{},
	}
}

// load loads the feedback store from disk
func (c *FeedbackCollector) load() error {
	data, err := os.ReadFile(c.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			c.store = &FeedbackStore{
				Feedbacks: []Feedback{},
				Metadata: StoreMetadata{
					Version:     "1.0",
					LastUpdated: time.Now().Format(time.RFC3339),
				},
			}
			return nil
		}
		return fmt.Errorf("failed to read feedback file: %w", err)
	}

	if err := json.Unmarshal(data, c.store); err != nil {
		return fmt.Errorf("failed to unmarshal feedback store: %w", err)
	}

	return nil
}

// save saves the feedback store to disk
func (c *FeedbackCollector) save() error {
	if err := os.MkdirAll(filepath.Dir(c.filePath), 0755); err != nil {
		return fmt.Errorf("failed to create feedback directory: %w", err)
	}

	c.store.Metadata.LastUpdated = time.Now().Format(time.RFC3339)

	data, err := json.MarshalIndent(c.store, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal feedback store: %w", err)
	}

	if err := os.WriteFile(c.filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write feedback file: %w", err)
	}

	return nil
}

// Submit adds a new feedback entry
func (c *FeedbackCollector) Submit(feedback Feedback) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.load(); err != nil {
		return err
	}

	if feedback.ID == "" {
		feedback.ID = generateID()
	}

	if feedback.Timestamp == "" {
		feedback.Timestamp = time.Now().Format(time.RFC3339)
	}

	if feedback.Status == "" {
		feedback.Status = FeedbackStatusNew
	}

	c.store.Feedbacks = append(c.store.Feedbacks, feedback)

	return c.save()
}

// Get retrieves a feedback entry by ID
func (c *FeedbackCollector) Get(id string) (*Feedback, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.load(); err != nil {
		return nil, err
	}

	for _, fb := range c.store.Feedbacks {
		if fb.ID == id {
			return &fb, nil
		}
	}

	return nil, fmt.Errorf("feedback not found: %s", id)
}

// List retrieves feedback entries matching the filter
func (c *FeedbackCollector) List(filter FeedbackFilter) ([]Feedback, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.load(); err != nil {
		return nil, err
	}

	var results []Feedback
	for _, fb := range c.store.Feedbacks {
		if c.matches(fb, filter) {
			results = append(results, fb)
		}
	}

	// Apply pagination
	if filter.Offset > 0 {
		if filter.Offset >= len(results) {
			return []Feedback{}, nil
		}
		results = results[filter.Offset:]
	}

	if filter.Limit > 0 && filter.Limit < len(results) {
		results = results[:filter.Limit]
	}

	return results, nil
}

// matches checks if a feedback matches the filter
func (c *FeedbackCollector) matches(fb Feedback, filter FeedbackFilter) bool {
	// Filter by types
	if len(filter.Types) > 0 {
		match := false
		for _, t := range filter.Types {
			if fb.FeedbackType == t {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}

	// Filter by satisfaction levels
	if len(filter.Satisfaction) > 0 {
		match := false
		for _, s := range filter.Satisfaction {
			if int(fb.Satisfaction) == s {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}

	// Filter by categories
	if len(filter.Categories) > 0 {
		match := false
		for _, cat := range filter.Categories {
			if fb.Category == cat {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}

	// Filter by status
	if filter.Status != "" && fb.Status != filter.Status {
		return false
	}

	// Filter by start time
	if filter.StartTime != "" {
		fbTime, err := time.Parse(time.RFC3339, fb.Timestamp)
		if err != nil {
			return false
		}
		startTime, err := time.Parse(time.RFC3339, filter.StartTime)
		if err != nil {
			return false
		}
		if fbTime.Before(startTime) {
			return false
		}
	}

	// Filter by end time
	if filter.EndTime != "" {
		fbTime, err := time.Parse(time.RFC3339, fb.Timestamp)
		if err != nil {
			return false
		}
		endTime, err := time.Parse(time.RFC3339, filter.EndTime)
		if err != nil {
			return false
		}
		if fbTime.After(endTime) {
			return false
		}
	}

	return true
}

// UpdateStatus updates the status of a feedback entry
func (c *FeedbackCollector) UpdateStatus(id string, status FeedbackStatus) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.load(); err != nil {
		return err
	}

	for i, fb := range c.store.Feedbacks {
		if fb.ID == id {
			c.store.Feedbacks[i].Status = status
			return c.save()
		}
	}

	return fmt.Errorf("feedback not found: %s", id)
}

// GetStats retrieves aggregated statistics for feedback
func (c *FeedbackCollector) GetStats(days int) (*FeedbackStats, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.load(); err != nil {
		return nil, err
	}

	stats := &FeedbackStats{
		ByType:                  make(map[string]int),
		ByCategory:              make(map[string]int),
		ByStatus:                make(map[string]int),
		SatisfactionDistribution: make(map[int]int),
	}

	cutoff := time.Now().AddDate(0, 0, -days)
	var totalSatisfaction int64
	var recentCount int

	for _, fb := range c.store.Feedbacks {
		stats.TotalFeedback++

		// Parse timestamp
		fbTime, err := time.Parse(time.RFC3339, fb.Timestamp)
		if err != nil {
			continue
		}

		isRecent := fbTime.After(cutoff)
		if isRecent {
			recentCount++
			totalSatisfaction += int64(fb.Satisfaction)
		}

		// Count by type
		stats.ByType[string(fb.FeedbackType)]++

		// Count by category
		if fb.Category != "" {
			stats.ByCategory[fb.Category]++
		}

		// Count by status
		stats.ByStatus[string(fb.Status)]++

		// Count satisfaction distribution
		stats.SatisfactionDistribution[int(fb.Satisfaction)]++
	}

	// Calculate average satisfaction
	if recentCount > 0 {
		stats.AverageSatisfaction = float64(totalSatisfaction) / float64(recentCount)
	}

	// Build recent trend
	stats.RecentTrend = c.buildTrend(days, cutoff)

	return stats, nil
}

// buildTrend builds daily statistics for the trend
func (c *FeedbackCollector) buildTrend(days int, cutoff time.Time) []DailyStat {
	trend := make([]DailyStat, 0, days)

	for i := days - 1; i >= 0; i-- {
		date := time.Now().AddDate(0, 0, -i)
		dateStr := date.Format("2006-01-02")

		dayStart := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
		dayEnd := dayStart.Add(24 * time.Hour)

		var dayCount int
		var daySatisfaction int64

		for _, fb := range c.store.Feedbacks {
			fbTime, err := time.Parse(time.RFC3339, fb.Timestamp)
			if err != nil {
				continue
			}

			if fbTime.After(dayStart) && fbTime.Before(dayEnd) {
				dayCount++
				daySatisfaction += int64(fb.Satisfaction)
			}
		}

		var avgSatisfaction float64
		if dayCount > 0 {
			avgSatisfaction = float64(daySatisfaction) / float64(dayCount)
		}

		trend = append(trend, DailyStat{
			Date:            dateStr,
			Count:           dayCount,
			AvgSatisfaction: avgSatisfaction,
		})
	}

	return trend
}

// generateID generates a unique ID
func generateID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// Adapter methods to satisfy FeedbackCollector interface used by coordination

// Collect implements the FeedbackCollector interface
func (c *FeedbackCollector) Collect(fb *Feedback) error {
	return c.Submit(*fb)
}

// GetFeedback implements the FeedbackCollector interface
func (c *FeedbackCollector) GetFeedback(id string) (*Feedback, error) {
	return c.Get(id)
}

// ListFeedback implements the FeedbackCollector interface
func (c *FeedbackCollector) ListFeedback(filter FeedbackFilter) ([]Feedback, error) {
	return c.List(filter)
}

// UpdateFeedbackStatus implements the FeedbackCollector interface
func (c *FeedbackCollector) UpdateFeedbackStatus(id string, status FeedbackStatus) (*Feedback, error) {
	err := c.UpdateStatus(id, status)
	if err != nil {
		return nil, err
	}
	return c.Get(id)
}
