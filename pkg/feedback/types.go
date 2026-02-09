package feedback

// FeedbackType represents the category of feedback
type FeedbackType string

const (
	FeedbackWorkflow   FeedbackType = "workflow"
	FeedbackFeature    FeedbackType = "feature"
	FeedbackBug        FeedbackType = "bug"
	FeedbackSuggestion FeedbackType = "suggestion"
	FeedbackPraise    FeedbackType = "praise"
)

// SatisfactionLevel represents user satisfaction
type SatisfactionLevel int

const (
	SatisfactionVeryLow  SatisfactionLevel = 1
	SatisfactionLow     SatisfactionLevel = 2
	SatisfactionNeutral SatisfactionLevel = 3
	SatisfactionHigh    SatisfactionLevel = 4
	SatisfactionVeryHigh SatisfactionLevel = 5
)

// Feedback represents a user feedback submission
type Feedback struct {
	ID           string            `json:"id"`
	Timestamp    string            `json:"timestamp"`
	SessionID    string            `json:"sessionId"`
	UserID       string            `json:"userId,omitempty"`
	FeedbackType FeedbackType      `json:"feedbackType"`
	Satisfaction SatisfactionLevel `json:"satisfaction"`
	Category     string            `json:"category,omitempty"`
	Message      string            `json:"message"`
	Tags         []string          `json:"tags,omitempty"`
	Metadata     *FeedbackMetadata `json:"metadata,omitempty"`
	Status       FeedbackStatus    `json:"status"`
}

// FeedbackMetadata contains contextual information
type FeedbackMetadata struct {
	Model          string   `json:"model,omitempty"`
	NexusVersion   string   `json:"nexusVersion,omitempty"`
	PulseVersion   string   `json:"pulseVersion,omitempty"`
	WorkspaceID    string   `json:"workspaceId,omitempty"`
	TaskID         string   `json:"taskId,omitempty"`
	SessionDuration int64   `json:"sessionDurationSeconds,omitempty"`
	SkillsUsed     []string `json:"skillsUsed,omitempty"`
}

// FeedbackStatus represents the processing state
type FeedbackStatus string

const (
	FeedbackStatusNew       FeedbackStatus = "new"
	FeedbackStatusReviewed  FeedbackStatus = "reviewed"
	FeedbackStatusTriaged   FeedbackStatus = "triaged"
	FeedbackStatusResolved  FeedbackStatus = "resolved"
)

// FeedbackFilter for querying feedback
type FeedbackFilter struct {
	Types        []FeedbackType `json:"types,omitempty"`
	Satisfaction []int          `json:"satisfaction,omitempty"`
	Categories   []string       `json:"categories,omitempty"`
	Status       FeedbackStatus `json:"status,omitempty"`
	StartTime    string         `json:"startTime,omitempty"`
	EndTime      string         `json:"endTime,omitempty"`
	Limit        int            `json:"limit,omitempty"`
	Offset       int            `json:"offset,omitempty"`
}

// FeedbackStats contains aggregated feedback statistics
type FeedbackStats struct {
	TotalFeedback           int                    `json:"totalFeedback"`
	AverageSatisfaction     float64                `json:"averageSatisfaction"`
	ByType                  map[string]int         `json:"byType"`
	ByCategory              map[string]int         `json:"byCategory"`
	ByStatus                map[string]int         `json:"byStatus"`
	SatisfactionDistribution map[int]int           `json:"satisfactionDistribution"`
	RecentTrend             []DailyStat            `json:"recentTrend"`
}

// DailyStat represents daily feedback statistics
type DailyStat struct {
	Date            string  `json:"date"`
	Count           int     `json:"count"`
	AvgSatisfaction float64 `json:"avgSatisfaction"`
}
