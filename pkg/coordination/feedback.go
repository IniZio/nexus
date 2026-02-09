package coordination

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nexus/nexus/pkg/feedback"
)

// FeedbackCollector interface for collecting feedback
type FeedbackCollector interface {
	Collect(fb *feedback.Feedback) error
	GetFeedback(id string) (*feedback.Feedback, error)
	ListFeedback(filter feedback.FeedbackFilter) ([]feedback.Feedback, error)
	UpdateFeedbackStatus(id string, status feedback.FeedbackStatus) (*feedback.Feedback, error)
	GetStats(days int) (*feedback.FeedbackStats, error)
}

// FeedbackSubmission represents a new feedback submission request
type FeedbackSubmission struct {
	FeedbackType  feedback.FeedbackType  `json:"feedbackType"`
	Satisfaction  feedback.SatisfactionLevel `json:"satisfaction"`
	Category      string                  `json:"category,omitempty"`
	Message       string                  `json:"message"`
	Tags          []string                `json:"tags,omitempty"`
}

// FeedbackResponse represents the response after submitting feedback
type FeedbackResponse struct {
	Feedback *feedback.Feedback `json:"feedback"`
	Message  string             `json:"message"`
}

// FeedbackListResponse represents a paginated list of feedback
type FeedbackListResponse struct {
	Feedback []feedback.Feedback `json:"feedback"`
	Total    int                `json:"total"`
	Page     int                `json:"page"`
	Limit    int                `json:"limit"`
}

// StatusUpdate represents a status update request
type StatusUpdate struct {
	Status feedback.FeedbackStatus `json:"status"`
}

// SessionRating represents a session rating request
type SessionRating struct {
	Satisfaction feedback.SatisfactionLevel `json:"satisfaction"`
	Comment      string                     `json:"comment,omitempty"`
}

// SuccessResponse represents a generic success response
type SuccessResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// handleFeedbackRequest handles all feedback requests
func (s *Server) handleFeedbackRequest(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleSubmitFeedback(w, r)
	case http.MethodGet:
		s.handleListFeedback(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleFeedbackItemRequest handles requests for a specific feedback item
func (s *Server) handleFeedbackItemRequest(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/feedback/")
	if path == "" {
		http.Error(w, "Feedback ID required", http.StatusBadRequest)
		return
	}

	parts := strings.Split(path, "/")
	feedbackID := parts[0]

	switch r.Method {
	case http.MethodGet:
		s.handleGetFeedback(w, r, feedbackID)
	case http.MethodPatch:
		s.handleUpdateFeedbackStatus(w, r, feedbackID)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleFeedbackStatusRequest handles status update requests for feedback
func (s *Server) handleFeedbackStatusRequest(w http.ResponseWriter, r *http.Request, feedbackID string) {
	if r.Method != http.MethodPatch {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.handleUpdateFeedbackStatus(w, r, feedbackID)
}

// handleSubmitFeedback handles POST /api/feedback
// Submit new feedback
func (s *Server) handleSubmitFeedback(w http.ResponseWriter, r *http.Request) {
	var submission FeedbackSubmission
	if err := json.NewDecoder(r.Body).Decode(&submission); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	// Validate required fields
	if submission.Message == "" {
		http.Error(w, "Message is required", http.StatusBadRequest)
		return
	}
	if submission.FeedbackType == "" {
		http.Error(w, "FeedbackType is required", http.StatusBadRequest)
		return
	}

	// Extract session info from headers or context
	sessionID := r.Header.Get("X-Session-ID")
	if sessionID == "" {
		sessionID = fmt.Sprintf("session_%d", time.Now().UnixNano())
	}
	userID := r.Header.Get("X-User-ID")

	// Create feedback object
	fb := &feedback.Feedback{
		ID:           fmt.Sprintf("fb_%d", time.Now().UnixNano()),
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
		SessionID:   sessionID,
		UserID:      userID,
		FeedbackType: submission.FeedbackType,
		Satisfaction: submission.Satisfaction,
		Category:     submission.Category,
		Message:      submission.Message,
		Tags:         submission.Tags,
		Status:       feedback.FeedbackStatusNew,
	}

	// Collect feedback using the FeedbackCollector if available
	if s.feedbackCollector != nil {
		if err := s.feedbackCollector.Collect(fb); err != nil {
			http.Error(w, fmt.Sprintf("Failed to collect feedback: %v", err), http.StatusInternalServerError)
			return
		}
	} else {
		// Store in memory as fallback
		s.feedbackMu.Lock()
		if s.feedbackStore == nil {
			s.feedbackStore = make(map[string]*feedback.Feedback)
		}
		s.feedbackStore[fb.ID] = fb
		s.feedbackMu.Unlock()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(FeedbackResponse{
		Feedback: fb,
		Message:  "Feedback submitted successfully",
	})
}

// handleListFeedback handles GET /api/feedback
// List feedback with filters
func (s *Server) handleListFeedback(w http.ResponseWriter, r *http.Request) {
	// Parse query parameters
	filter := feedback.FeedbackFilter{}

	if types := r.URL.Query().Get("type"); types != "" {
		for _, t := range strings.Split(types, ",") {
			if ft := feedback.FeedbackType(strings.TrimSpace(t)); ft != "" {
				filter.Types = append(filter.Types, ft)
			}
		}
	}

	if satisfaction := r.URL.Query().Get("satisfaction"); satisfaction != "" {
		for _, s := range strings.Split(satisfaction, ",") {
			if val, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && val > 0 {
				filter.Satisfaction = append(filter.Satisfaction, val)
			}
		}
	}

	if categories := r.URL.Query().Get("category"); categories != "" {
		for _, c := range strings.Split(categories, ",") {
			if cat := strings.TrimSpace(c); cat != "" {
				filter.Categories = append(filter.Categories, cat)
			}
		}
	}

	if status := r.URL.Query().Get("status"); status != "" {
		filter.Status = feedback.FeedbackStatus(status)
	}

	if days := r.URL.Query().Get("days"); days != "" {
		if d, err := strconv.Atoi(days); err == nil && d > 0 {
			now := time.Now()
			filter.StartTime = now.AddDate(0, 0, -d).Format(time.RFC3339)
			filter.EndTime = now.Format(time.RFC3339)
		}
	}

	if limit := r.URL.Query().Get("limit"); limit != "" {
		if l, err := strconv.Atoi(limit); err == nil && l > 0 {
			filter.Limit = l
		} else {
			filter.Limit = 20 // default limit
		}
	} else {
		filter.Limit = 20
	}

	if offset := r.URL.Query().Get("offset"); offset != "" {
		if o, err := strconv.Atoi(offset); err == nil && o >= 0 {
			filter.Offset = o
		}
	}

	var feedbackList []feedback.Feedback
	var total int

	if s.feedbackCollector != nil {
		list, err := s.feedbackCollector.ListFeedback(filter)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to list feedback: %v", err), http.StatusInternalServerError)
			return
		}
		feedbackList = list
		total = len(list)
	} else {
		// Fallback: iterate in-memory store
		s.feedbackMu.RLock()
		for _, fb := range s.feedbackStore {
			if matchesFilter(fb, &filter) {
				feedbackList = append(feedbackList, *fb)
			}
		}
		total = len(feedbackList)
		s.feedbackMu.RUnlock()
	}

	// Apply pagination
	page := filter.Offset/filter.Limit + 1
	if filter.Offset+filter.Limit < len(feedbackList) {
		feedbackList = feedbackList[filter.Offset : filter.Offset+filter.Limit]
	} else if filter.Offset < len(feedbackList) {
		feedbackList = feedbackList[filter.Offset:]
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(FeedbackListResponse{
		Feedback: feedbackList,
		Total:    total,
		Page:     page,
		Limit:    filter.Limit,
	})
}

// handleGetFeedback handles GET /api/feedback/:id
// Get specific feedback by ID
func (s *Server) handleGetFeedback(w http.ResponseWriter, r *http.Request, feedbackID string) {
	if feedbackID == "" {
		http.Error(w, "Feedback ID is required", http.StatusBadRequest)
		return
	}

	var fb *feedback.Feedback
	var err error

	if s.feedbackCollector != nil {
		fb, err = s.feedbackCollector.GetFeedback(feedbackID)
		if err != nil {
			http.Error(w, fmt.Sprintf("Feedback not found: %v", err), http.StatusNotFound)
			return
		}
	} else {
		// Fallback: look in memory
		s.feedbackMu.RLock()
		fb = s.feedbackStore[feedbackID]
		s.feedbackMu.RUnlock()
		if fb == nil {
			http.Error(w, "Feedback not found", http.StatusNotFound)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fb)
}

// handleUpdateFeedbackStatus handles PATCH /api/feedback/:id/status
// Update feedback status
func (s *Server) handleUpdateFeedbackStatus(w http.ResponseWriter, r *http.Request, feedbackID string) {
	if feedbackID == "" {
		http.Error(w, "Feedback ID is required", http.StatusBadRequest)
		return
	}

	var update StatusUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	// Validate status
	if !isValidFeedbackStatus(string(update.Status)) {
		http.Error(w, fmt.Sprintf("Invalid status: %s", update.Status), http.StatusBadRequest)
		return
	}

	var fb *feedback.Feedback
	var err error

	if s.feedbackCollector != nil {
		fb, err = s.feedbackCollector.UpdateFeedbackStatus(feedbackID, update.Status)
		if err != nil {
			http.Error(w, fmt.Sprintf("Feedback not found: %v", err), http.StatusNotFound)
			return
		}
	} else {
		// Fallback: update in memory
		s.feedbackMu.Lock()
		fb, exists := s.feedbackStore[feedbackID]
		if !exists {
			s.feedbackMu.Unlock()
			http.Error(w, "Feedback not found", http.StatusNotFound)
			return
		}
		fb.Status = update.Status
		s.feedbackMu.Unlock()
		fb = fb
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fb)
}

// handleFeedbackStats handles GET /api/feedback/stats
// Get feedback statistics
func (s *Server) handleFeedbackStats(w http.ResponseWriter, r *http.Request) {
	days := 30 // default
	if d := r.URL.Query().Get("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 {
			days = parsed
		}
	}

	if s.feedbackCollector != nil {
		stats, err := s.feedbackCollector.GetStats(days)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to get stats: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stats)
	} else {
		// Fallback: compute stats from memory
		stats := computeFeedbackStats(s.feedbackStore, days)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stats)
	}
}

// handleSessionRate handles POST /api/feedback/session-rate
// Rate current session
func (s *Server) handleSessionRate(w http.ResponseWriter, r *http.Request) {
	var rating SessionRating
	if err := json.NewDecoder(r.Body).Decode(&rating); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	if rating.Satisfaction == 0 {
		http.Error(w, "Satisfaction rating is required", http.StatusBadRequest)
		return
	}

	// Extract session info
	sessionID := r.Header.Get("X-Session-ID")
	if sessionID == "" {
		sessionID = fmt.Sprintf("session_%d", time.Now().UnixNano())
	}
	userID := r.Header.Get("X-User-ID")

	// Create feedback from session rating
	fb := &feedback.Feedback{
		ID:           fmt.Sprintf("fb_%d", time.Now().UnixNano()),
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
		SessionID:   sessionID,
		UserID:      userID,
		FeedbackType: feedback.FeedbackPraise,
		Satisfaction: rating.Satisfaction,
		Message:      rating.Comment,
		Status:       feedback.FeedbackStatusNew,
	}

	if s.feedbackCollector != nil {
		if err := s.feedbackCollector.Collect(fb); err != nil {
			http.Error(w, fmt.Sprintf("Failed to collect feedback: %v", err), http.StatusInternalServerError)
			return
		}
	} else {
		// Store in memory as fallback
		s.feedbackMu.Lock()
		if s.feedbackStore == nil {
			s.feedbackStore = make(map[string]*feedback.Feedback)
		}
		s.feedbackStore[fb.ID] = fb
		s.feedbackMu.Unlock()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SuccessResponse{
		Success: true,
		Message: "Session rating recorded successfully",
	})
}

// Helper functions

// matchesFilter checks if a feedback item matches the filter criteria
func matchesFilter(fb *feedback.Feedback, filter *feedback.FeedbackFilter) bool {
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

	if len(filter.Categories) > 0 {
		match := false
		for _, c := range filter.Categories {
			if fb.Category == c {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}

	if filter.Status != "" && fb.Status != filter.Status {
		return false
	}

	if filter.StartTime != "" {
		if fb.Timestamp < filter.StartTime {
			return false
		}
	}

	if filter.EndTime != "" && fb.Timestamp > filter.EndTime {
		return false
	}

	return true
}

// isValidFeedbackStatus checks if a status is valid
func isValidFeedbackStatus(status string) bool {
	validStatuses := map[string]bool{
		string(feedback.FeedbackStatusNew):      true,
		string(feedback.FeedbackStatusReviewed): true,
		string(feedback.FeedbackStatusTriaged):  true,
		string(feedback.FeedbackStatusResolved): true,
	}
	return validStatuses[status]
}

// computeFeedbackStats computes statistics from in-memory feedback store
func computeFeedbackStats(store map[string]*feedback.Feedback, days int) *feedback.FeedbackStats {
	cutoff := time.Now().AddDate(0, 0, -days).Format(time.RFC3339)

	stats := &feedback.FeedbackStats{
		ByType:                  make(map[string]int),
		ByCategory:              make(map[string]int),
		ByStatus:                make(map[string]int),
		SatisfactionDistribution: make(map[int]int),
		RecentTrend:              make([]feedback.DailyStat, 0),
	}

	dailyCounts := make(map[string]int)
	dailySatisfaction := make(map[string][]int)

	for _, fb := range store {
		if fb.Timestamp < cutoff {
			continue
		}

		stats.TotalFeedback++
		stats.ByType[string(fb.FeedbackType)]++
		if fb.Category != "" {
			stats.ByCategory[fb.Category]++
		}
		stats.ByStatus[string(fb.Status)]++
		stats.SatisfactionDistribution[int(fb.Satisfaction)] = stats.SatisfactionDistribution[int(fb.Satisfaction)] + 1

		// Daily stats for trend
		date := fb.Timestamp[:10]
		dailyCounts[date]++
		dailySatisfaction[date] = append(dailySatisfaction[date], int(fb.Satisfaction))
	}

	// Calculate averages
	totalSatisfaction := 0
	count := 0
	for satisfaction, countVal := range stats.SatisfactionDistribution {
		totalSatisfaction += satisfaction * countVal
		count += countVal
	}
	if count > 0 {
		stats.AverageSatisfaction = float64(totalSatisfaction) / float64(count)
	}

	// Build recent trend
	dates := make([]string, 0, len(dailyCounts))
	for date := range dailyCounts {
		dates = append(dates, date)
	}
	for _, date := range dates {
		sum := 0
		for _, s := range dailySatisfaction[date] {
			sum += s
		}
		avg := 0.0
		if len(dailySatisfaction[date]) > 0 {
			avg = float64(sum) / float64(len(dailySatisfaction[date]))
		}
		stats.RecentTrend = append(stats.RecentTrend, feedback.DailyStat{
			Date:            date,
			Count:           dailyCounts[date],
			AvgSatisfaction: avg,
		})
	}

	return stats
}
