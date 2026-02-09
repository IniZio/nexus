package metrics

import (
	"sort"
	"time"
)

type Analyzer struct {
	calculator *Calculator
}

func NewAnalyzer() *Analyzer {
	return &Analyzer{
		calculator: NewCalculator(),
	}
}

func (a *Analyzer) AnalyzePatterns(logs []UsageLog) ([]UsagePattern, error) {
	skillGroups := a.groupBySkill(logs)

	var patterns []UsagePattern
	for skill, skillLogs := range skillGroups {
		if len(skillLogs) == 0 {
			continue
		}

		frequency := len(skillLogs)
		averageDuration := a.calculateAverageDuration(skillLogs)
		successRate := a.calculateSuccessRate(skillLogs)
		timeOfDay := a.getPeakTimeOfDay(skillLogs)

		patterns = append(patterns, UsagePattern{
			Skill:           skill,
			Frequency:       frequency,
			AverageDuration: averageDuration,
			SuccessRate:     successRate,
			TimeOfDay:       timeOfDay,
		})
	}

	sort.Slice(patterns, func(i, j int) bool {
		return patterns[i].Frequency > patterns[j].Frequency
	})

	return patterns, nil
}

func (a *Analyzer) groupBySkill(logs []UsageLog) map[string][]UsageLog {
	groups := make(map[string][]UsageLog)

	for _, log := range logs {
		if log.Invocation.Type == string(InvocationSkill) {
			skill := log.Invocation.Name
			groups[skill] = append(groups[skill], log)
		}
	}

	return groups
}

func (a *Analyzer) calculateAverageDuration(logs []UsageLog) float64 {
	if len(logs) == 0 {
		return 0
	}

	var total int64
	for _, log := range logs {
		total += log.Outcome.Duration
	}

	return float64(total) / float64(len(logs))
}

func (a *Analyzer) calculateSuccessRate(logs []UsageLog) float64 {
	if len(logs) == 0 {
		return 0
	}

	var success int
	for _, log := range logs {
		if log.Outcome.Success {
			success++
		}
	}

	return float64(success) / float64(len(logs)) * 100
}

func (a *Analyzer) getPeakTimeOfDay(logs []UsageLog) string {
	if len(logs) == 0 {
		return "unknown"
	}

	hourCounts := make(map[int]int)

	for _, log := range logs {
		timestamp, err := time.Parse(time.RFC3339, log.Timestamp)
		if err != nil {
			continue
		}

		hour := timestamp.Hour()
		hourCounts[hour]++
	}

	var peakHour int
	var peakCount int

	for hour, count := range hourCounts {
		if count > peakCount {
			peakCount = count
			peakHour = hour
		}
	}

	return a.getTimeOfDay(peakHour)
}

func (a *Analyzer) getTimeOfDay(hour int) string {
	if hour >= 5 && hour < 12 {
		return "morning"
	}
	if hour >= 12 && hour < 17 {
		return "afternoon"
	}
	if hour >= 17 && hour < 21 {
		return "evening"
	}
	return "night"
}
