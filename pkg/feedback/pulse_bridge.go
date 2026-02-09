package feedback

import (
	"time"
)

// PulseFeedback represents feedback in Pulse format
type PulseFeedback struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Content     string    `json:"content"`
	Category    string    `json:"category"`
	Sentiment   float64   `json:"sentiment"`
	Source      string    `json:"source"`
	Tags        []string  `json:"tags"`
	Theme       string    `json:"theme"`
	Processed   bool      `json:"processed"`
	ProcessedAt *time.Time `json:"processed_at,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// PulseFeedbackRepository interface for storing Pulse-format feedback
type PulseFeedbackRepository interface {
	Create(feedback *PulseFeedback) error
	GetByID(id string) (*PulseFeedback, error)
	List(filter FeedbackFilter) ([]PulseFeedback, error)
	Update(feedback *PulseFeedback) error
	GetStats(days int) (*FeedbackStats, error)
}

// PulseFeedbackBridge transforms Nexus feedback to Pulse format
type PulseFeedbackBridge struct{}

func NewPulseFeedbackBridge() *PulseFeedbackBridge {
	return &PulseFeedbackBridge{}
}

func (b *PulseFeedbackBridge) ToPulse(nexusFb *Feedback) *PulseFeedback {
	sentiment := float64(nexusFb.Satisfaction-3) / 2.0
	category := string(nexusFb.FeedbackType)
	return &PulseFeedback{
		ID:          nexusFb.ID,
		Content:     nexusFb.Message,
		Category:    category,
		Sentiment:   sentiment,
		Source:      "nexus",
		Tags:        nexusFb.Tags,
		Theme:       nexusFb.Category,
		Processed:   false,
		CreatedAt:   b.parseTimestamp(nexusFb.Timestamp),
	}
}

func (b *PulseFeedbackBridge) parseTimestamp(ts string) time.Time {
	t, _ := time.Parse(time.RFC3339, ts)
	return t
}

func (b *PulseFeedbackBridge) ToNexus(pulseFb *PulseFeedback) *Feedback {
	return &Feedback{
		ID:           pulseFb.ID,
		Timestamp:    pulseFb.CreatedAt.Format(time.RFC3339),
		FeedbackType: FeedbackType(pulseFb.Category),
		Satisfaction: SatisfactionLevel(pulseFb.Sentiment*2 + 3),
		Category:     pulseFb.Category,
		Message:      pulseFb.Content,
		Tags:         pulseFb.Tags,
		Status:       FeedbackStatusNew,
	}
}

// FeedbackConverter provides bidirectional conversion
type FeedbackConverter struct{ bridge *PulseFeedbackBridge }

func NewFeedbackConverter() *FeedbackConverter {
	return &FeedbackConverter{bridge: NewPulseFeedbackBridge()}
}

func (c *FeedbackConverter) ConvertToPulse(nexusFb *Feedback) *PulseFeedback {
	return c.bridge.ToPulse(nexusFb)
}

func (c *FeedbackConverter) ConvertToNexus(pulseFb *PulseFeedback) *Feedback {
	return c.bridge.ToNexus(pulseFb)
}

// PulseFeedbackSync synchronizes feedback to Pulse repository
type PulseFeedbackSync struct {
	bridge *PulseFeedbackBridge
	repo   PulseFeedbackRepository
}

func NewPulseFeedbackSync(repo PulseFeedbackRepository) *PulseFeedbackSync {
	return &PulseFeedbackSync{
		bridge: NewPulseFeedbackBridge(),
		repo:   repo,
	}
}

func (s *PulseFeedbackSync) SyncNewFeedback(nexusFbs []Feedback) (int, error) {
	synced := 0
	for _, fb := range nexusFbs {
		if fb.Status == FeedbackStatusNew {
			pulseFb := s.bridge.ToPulse(&fb)
			if err := s.repo.Create(pulseFb); err != nil {
				return synced, err
			}
			synced++
		}
	}
	return synced, nil
}
