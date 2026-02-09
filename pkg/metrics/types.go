package metrics

type InvocationType string

const (
	InvocationSkill   InvocationType = "skill"
	InvocationCommand InvocationType = "command"
	InvocationRule    InvocationType = "rule"
)

type UsageLog struct {
	ID         string     `json:"id"`
	Timestamp  string     `json:"timestamp"`
	Agent      string     `json:"agent"`
	Invocation Invocation `json:"invocation"`
	Context    Context    `json:"context"`
	Outcome    Outcome    `json:"outcome"`
	Metadata   *Metadata  `json:"metadata,omitempty"`
}

type Invocation struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

type Context struct {
	Task    string   `json:"task"`
	Project string   `json:"project"`
	Files   []string `json:"files"`
}

type Outcome struct {
	Success    bool    `json:"success"`
	Duration   int64   `json:"duration"`
	TokensUsed int64   `json:"tokensUsed,omitempty"`
	Cost       float64 `json:"cost,omitempty"`
}

type Metadata struct {
	Model     string   `json:"model,omitempty"`
	SessionID string   `json:"sessionId,omitempty"`
	ToolCalls []string `json:"toolCalls,omitempty"`
	Errors    []string `json:"errors,omitempty"`
}

type ProductivityMetrics struct {
	TotalInvocations   int64   `json:"totalInvocations"`
	SkillInvocations   int64   `json:"skillInvocations"`
	CommandInvocations int64   `json:"commandInvocations"`
	RuleApplications   int64   `json:"ruleApplications"`
	AverageDuration    float64 `json:"averageDuration"`
	TotalDuration      int64   `json:"totalDuration"`
	SuccessRate        float64 `json:"successRate"`
	FailureRate        float64 `json:"failureRate"`
	TotalTokensUsed    int64   `json:"totalTokensUsed"`
	TotalCost          float64 `json:"totalCost"`
	InvocationsPerHour float64 `json:"invocationsPerHour"`
	TokensPerTask      float64 `json:"tokensPerTask"`
}

type UsagePattern struct {
	Skill           string  `json:"skill"`
	Frequency       int     `json:"frequency"`
	AverageDuration float64 `json:"averageDuration"`
	SuccessRate     float64 `json:"successRate"`
	TimeOfDay       string  `json:"timeOfDay"`
}

type LogStore struct {
	Entries  []UsageLog `json:"entries"`
	Metadata StoreMeta  `json:"metadata"`
}

type StoreMeta struct {
	Version     string `json:"version"`
	LastUpdated string `json:"lastUpdated"`
}

type DailySummary struct {
	Date               string              `json:"date"`
	TotalInvocations   int64               `json:"totalInvocations"`
	SkillInvocations   int64               `json:"skillInvocations"`
	CommandInvocations int64               `json:"commandInvocations"`
	RuleApplications   int64               `json:"ruleApplications"`
	AverageDuration    float64             `json:"averageDuration"`
	SuccessRate        float64             `json:"successRate"`
	TopSkills          []SkillCount        `json:"topSkills"`
	Insights           []string            `json:"insights"`
	Metrics            ProductivityMetrics `json:"metrics"`
}

type SkillCount struct {
	Skill string `json:"skill"`
	Count int    `json:"count"`
}

type BenchmarkComparison struct {
	Baseline           ProductivityMetrics `json:"baseline"`
	Current            ProductivityMetrics `json:"current"`
	Improvements       ImprovementMetrics  `json:"improvements"`
	PercentImprovement PercentImprovement  `json:"percentImprovement"`
}

type ImprovementMetrics struct {
	Duration      float64 `json:"duration"`
	SuccessRate   float64 `json:"successRate"`
	TokensPerTask float64 `json:"tokensPerTask"`
}

type PercentImprovement struct {
	Duration      float64 `json:"duration"`
	SuccessRate   float64 `json:"successRate"`
	TokensPerTask float64 `json:"tokensPerTask"`
}
