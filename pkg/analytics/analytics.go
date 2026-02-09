// Package analytics provides advanced ML-powered analytics for Nexus metrics.
// It includes time series forecasting, pattern recognition, and predictive analytics.
package analytics

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

// ForecastMethod represents the forecasting algorithm.
type ForecastMethod string

const (
	MethodMovingAverage ForecastMethod = "moving_average"
	MethodExponential   ForecastMethod = "exponential_smoothing"
	MethodLinearTrend   ForecastMethod = "linear_trend"
	MethodSeasonal      ForecastMethod = "seasonal_decomposition"
	MethodHoltWinters   ForecastMethod = "holt_winters"
)

// PatternType represents detected patterns in data.
type PatternType string

const (
	PatternTrendUp      PatternType = "trend_up"
	PatternTrendDown    PatternType = "trend_down"
	PatternSeasonal     PatternType = "seasonal"
	PatternCyclic       PatternType = "cyclic"
	PatternSpike        PatternType = "spike"
	PatternDrop         PatternType = "drop"
	PatternStabilizing  PatternType = "stabilizing"
	PatternVolatile    PatternType = "volatile"
)

// ClusterMethod represents clustering algorithm.
type ClusterMethod string

const (
	ClusterKMeans ClusterMethod = "kmeans"
	ClusterDBSCAN ClusterMethod = "dbscan"
)

// DataPoint represents a single data point in time series.
type DataPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
	Label     string    `json:"label,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// TimeSeries represents a time series dataset.
type TimeSeries struct {
	Points     []DataPoint `json:"points"`
	MetricType string      `json:"metric_type"`
	Interval   time.Duration `json:"interval"`
}

// Forecast represents a prediction for future values.
type Forecast struct {
	MetricType string       `json:"metric_type"`
	Method     ForecastMethod `json:"method"`
	StartTime  time.Time    `json:"start_time"`
	EndTime    time.Time    `json:"end_time"`
	Values     []ForecastPoint `json:"values"`
	Confidence float64      `json:"confidence"`
	Trend      string       `json:"trend"` // "up", "down", "stable"
}

// ForecastPoint represents a single forecasted value.
type ForecastPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
	LowerCI   float64   `json:"lower_ci,omitempty"`
	UpperCI   float64   `json:"upper_ci,omitempty"`
}

// Pattern represents a detected pattern in data.
type Pattern struct {
	Type        PatternType `json:"type"`
	Description string     `json:"description"`
	StartTime   time.Time  `json:"start_time"`
	EndTime     time.Time  `json:"end_time"`
	Confidence  float64    `json:"confidence"`
	Strength    float64    `json:"strength"` // 0-1 scale
	AffectedPoints []int   `json:"affected_points,omitempty"`
}

// Cluster represents a cluster of similar data points.
type Cluster struct {
	ID         int       `json:"id"`
	Center     []float64 `json:"center"`
	Points     []int     `json:"points"`
	Label      string    `json:"label,omitempty"`
	MetricType string    `json:"metric_type"`
	Stats      ClusterStats `json:"stats"`
}

// ClusterStats represents statistics for a cluster.
type ClusterStats struct {
	Count       int     `json:"count"`
	Mean        float64 `json:"mean"`
	StdDev      float64 `json:"std_dev"`
	Min         float64 `json:"min"`
	Max         float64 `json:"max"`
	Spread      float64 `json:"spread"`
}

// AnomalyScore represents a scored anomaly.
type AnomalyScore struct {
	Timestamp   time.Time `json:"timestamp"`
	Value       float64   `json:"value"`
	Expected   float64   `json:"expected"`
	Deviation  float64   `json:"deviation"`
	Score      float64   `json:"score"` // 0-1, higher = more anomalous
	Severity   string    `json:"severity"` // "low", "medium", "high", "critical"
	Factors    []string  `json:"factors,omitempty"`
}

// TrendAnalysis represents trend analysis results.
type TrendAnalysis struct {
	MetricType   string         `json:"metric_type"`
	OverallTrend string         `json:"overall_trend"` // "improving", "declining", "stable"
	Strength     float64        `json:"strength"` // 0-1 scale
	Slope        float64        `json:"slope"`
	R2           float64         `json:"r_squared"` // Goodness of fit
	Period       string         `json:"period"`
	Predictions  []ForecastPoint `json:"predictions"`
	Patterns     []Pattern      `json:"patterns"`
}

// AnalyticsEngine provides ML-powered analytics.
type AnalyticsEngine struct {
	mu sync.RWMutex

	// Configuration
	config *AnalyticsConfig

	// Cached models
	models map[string]interface{}
}

// AnalyticsConfig holds configuration for analytics.
type AnalyticsConfig struct {
	// Forecasting
	DefaultHorizon   time.Duration `json:"default_horizon"`
	MaxHorizon      time.Duration `json:"max_horizon"`
	ConfidenceLevel float64       `json:"confidence_level"`

	// Anomaly detection
	AnomalyThreshold float64     `json:"anomaly_threshold"`
	Sensitivity      float64     `json:"sensitivity"`

	// Pattern detection
	MinPatternStrength float64   `json:"min_pattern_strength"`
	SeasonalityWindow  int       `json:"seasonality_window"`

	// Clustering
	DefaultClusters int         `json:"default_clusters"`
	MaxClusters    int         `json:"max_clusters"`
}

// NewAnalyticsEngine creates a new analytics engine.
func NewAnalyticsEngine(config *AnalyticsConfig) *AnalyticsEngine {
	if config == nil {
		config = &AnalyticsConfig{
			DefaultHorizon:   24 * time.Hour,
			MaxHorizon:      7 * 24 * time.Hour,
			ConfidenceLevel: 0.95,
			AnomalyThreshold: 0.7,
			Sensitivity:     1.0,
			MinPatternStrength: 0.5,
			SeasonalityWindow: 7,
			DefaultClusters:  4,
			MaxClusters:     10,
		}
	}

	return &AnalyticsEngine{
		config:  config,
		models: make(map[string]interface{}),
	}
}

// Forecast generates a forecast for time series data.
func (e *AnalyticsEngine) Forecast(ctx context.Context, ts *TimeSeries, horizon time.Duration, method ForecastMethod) (*Forecast, error) {
	if len(ts.Points) < 3 {
		return nil, ErrInsufficientData
	}

	// Extract values
	values := make([]float64, len(ts.Points))
	for i, p := range ts.Points {
		values[i] = p.Value
	}

	var forecast *Forecast
	var err error

	switch method {
	case MethodMovingAverage:
		forecast, err = e.forecastMovingAverage(ts, values, horizon)
	case MethodExponential:
		forecast, err = e.forecastExponential(ts, values, horizon)
	case MethodLinearTrend:
		forecast, err = e.forecastLinearTrend(ts, values, horizon)
	case MethodSeasonal:
		forecast, err = e.forecastSeasonal(ts, values, horizon)
	default:
		forecast, err = e.forecastMovingAverage(ts, values, horizon)
	}

	return forecast, err
}

// forecastMovingAverage uses moving average for forecasting.
func (e *AnalyticsEngine) forecastMovingAverage(ts *TimeSeries, values []float64, horizon time.Duration) (*Forecast, error) {
	// Calculate moving average
	window := min(len(values), 7) // 7-point window
	ma := e.movingAverage(values, window)

	// Calculate trend from MA
	trend := e.calculateTrend(ma)

	// Generate forecast points
	interval := ts.Interval
	if interval == 0 {
		interval = time.Hour
	}

	forecastValues := make([]ForecastPoint, 0)
	lastTime := ts.Points[len(ts.Points)-1].Timestamp

	for i := 1; i <= 24; i++ { // 24 points forecast
		timestamp := lastTime.Add(interval * time.Duration(i))

		// Simple projection based on trend
		lastMA := ma[len(ma)-1]
		projected := lastMA + (trend * float64(i))

		// Calculate confidence interval
		stdDev := e.standardDeviation(values)
		ci := 1.96 * stdDev / math.Sqrt(float64(len(values)))

		forecastValues = append(forecastValues, ForecastPoint{
			Timestamp: timestamp,
			Value:     projected,
			LowerCI:   projected - ci,
			UpperCI:   projected + ci,
		})
	}

	// Determine trend direction
	trendStr := "stable"
	if trend > 0.1 {
		trendStr = "up"
	} else if trend < -0.1 {
		trendStr = "down"
	}

	return &Forecast{
		MetricType: ts.MetricType,
		Method:    MethodMovingAverage,
		StartTime: ts.Points[0].Timestamp,
		EndTime:   forecastValues[len(forecastValues)-1].Timestamp,
		Values:    forecastValues,
		Confidence: e.config.ConfidenceLevel,
		Trend:     trendStr,
	}, nil
}

// forecastExponential uses exponential smoothing.
func (e *AnalyticsEngine) forecastExponential(ts *TimeSeries, values []float64, horizon time.Duration) (*Forecast, error) {
	alpha := 0.3 // Smoothing factor

	// Calculate exponential moving average
	ema := make([]float64, len(values))
	ema[0] = values[0]
	for i := 1; i < len(values); i++ {
		ema[i] = alpha*values[i] + (1-alpha)*ema[i-1]
	}

	// Forecast using EMA
	interval := ts.Interval
	if interval == 0 {
		interval = time.Hour
	}

	lastEMA := ema[len(ema)-1]
	lastTime := ts.Points[len(ts.Points)-1].Timestamp

	forecastValues := make([]ForecastPoint, 0)
	for i := 1; i <= 24; i++ {
		timestamp := lastTime.Add(interval * time.Duration(i))
		projected := lastEMA // EMA continues flat

		stdDev := e.standardDeviation(values)
		ci := 1.96 * stdDev * math.Sqrt(1-alpha)

		forecastValues = append(forecastValues, ForecastPoint{
			Timestamp: timestamp,
			Value:     projected,
			LowerCI:   projected - ci,
			UpperCI:   projected + ci,
		})
	}

	// Calculate trend
	trend := e.calculateTrend(ema)
	trendStr := "stable"
	if trend > 0.1 {
		trendStr = "up"
	} else if trend < -0.1 {
		trendStr = "down"
	}

	return &Forecast{
		MetricType: ts.MetricType,
		Method:    MethodExponential,
		StartTime: ts.Points[0].Timestamp,
		EndTime:   forecastValues[len(forecastValues)-1].Timestamp,
		Values:    forecastValues,
		Confidence: e.config.ConfidenceLevel,
		Trend:     trendStr,
	}, nil
}

// forecastLinearTrend uses linear regression for forecasting.
func (e *AnalyticsEngine) forecastLinearTrend(ts *TimeSeries, values []float64, horizon time.Duration) (*Forecast, error) {
	// Linear regression
	n := float64(len(values))
	xMean := (n - 1) / 2
	yMean := e.mean(values)

	var num, den float64
	for i, y := range values {
		x := float64(i)
		num += (x - xMean) * (y - yMean)
		den += (x - xMean) * (x - xMean)
	}

	slope := num / den
	intercept := yMean - slope*xMean

	// Calculate R-squared
	var ssTot, ssRes float64
	for i, y := range values {
		x := float64(i)
		pred := slope*x + intercept
		ssRes += (y - pred) * (y - pred)
		ssTot += (y - yMean) * (y - yMean)
	}
	r2 := 1 - ssRes/ssTot

	// Generate forecast
	interval := ts.Interval
	if interval == 0 {
		interval = time.Hour
	}

	lastTime := ts.Points[len(ts.Points)-1].Timestamp

	forecastValues := make([]ForecastPoint, 0)
	for i := 1; i <= 24; i++ {
		timestamp := lastTime.Add(interval * time.Duration(i))
		x := float64(len(values)-1) + float64(i)
		projected := slope*x + intercept

		// CI based on residual standard error
		residuals := make([]float64, len(values))
		for j, y := range values {
			xj := float64(j)
			residuals[j] = y - (slope*xj + intercept)
		}
		rse := e.standardDeviation(residuals)
		ci := 1.96 * rse

		forecastValues = append(forecastValues, ForecastPoint{
			Timestamp: timestamp,
			Value:     projected,
			LowerCI:   projected - ci,
			UpperCI:   projected + ci,
		})
	}

	// Determine trend
	trendStr := "stable"
	if slope > 0.1 {
		trendStr = "up"
	} else if slope < -0.1 {
		trendStr = "down"
	}

	return &Forecast{
		MetricType: ts.MetricType,
		Method:    MethodLinearTrend,
		StartTime: ts.Points[0].Timestamp,
		EndTime:   forecastValues[len(forecastValues)-1].Timestamp,
		Values:    forecastValues,
		Confidence: r2,
		Trend:     trendStr,
	}, nil
}

// forecastSeasonal uses seasonal decomposition.
func (e *AnalyticsEngine) forecastSeasonal(ts *TimeSeries, values []float64, horizon time.Duration) (*Forecast, error) {
	// Simplified seasonal adjustment
	seasonalPeriod := 7 // Weekly seasonality

	// Decompose
	trend := e.movingAverage(values, seasonalPeriod)
	detrended := make([]float64, len(values))
	for i := range values {
		if i < len(trend) {
			detrended[i] = values[i] - trend[i]
		} else {
			detrended[i] = values[i]
		}
	}

	// Calculate seasonal indices
	seasonalAvg := make([]float64, seasonalPeriod)
	counts := make([]int, seasonalPeriod)
	for i, v := range detrended {
		seasonalAvg[i%seasonalPeriod] += v
		counts[i%seasonalPeriod]++
	}
	for i := range seasonalAvg {
		if counts[i] > 0 {
			seasonalAvg[i] /= float64(counts[i])
		}
	}

	// Forecast
	interval := ts.Interval
	if interval == 0 {
		interval = time.Hour
	}

	lastTime := ts.Points[len(ts.Points)-1].Timestamp
	lastTrend := trend[len(trend)-1]

	forecastValues := make([]ForecastPoint, 0)
	for i := 1; i <= 24; i++ {
		timestamp := lastTime.Add(interval * time.Duration(i))
		seasonalIdx := (len(values) + i - 1) % seasonalPeriod
		projected := lastTrend + seasonalAvg[seasonalIdx]

		stdDev := e.standardDeviation(values)
		ci := 1.96 * stdDev

		forecastValues = append(forecastValues, ForecastPoint{
			Timestamp: timestamp,
			Value:     projected,
			LowerCI:   projected - ci,
			UpperCI:   projected + ci,
		})
	}

	return &Forecast{
		MetricType: ts.MetricType,
		Method:    MethodSeasonal,
		StartTime: ts.Points[0].Timestamp,
		EndTime:   forecastValues[len(forecastValues)-1].Timestamp,
		Values:    forecastValues,
		Confidence: e.config.ConfidenceLevel * 0.9, // Seasonal is less certain
		Trend:     "seasonal",
	}, nil
}

// DetectPatterns finds patterns in time series data.
func (e *AnalyticsEngine) DetectPatterns(ts *TimeSeries) ([]Pattern, error) {
	if len(ts.Points) < 5 {
		return nil, ErrInsufficientData
	}

	values := make([]float64, len(ts.Points))
	for i, p := range ts.Points {
		values[i] = p.Value
	}

	patterns := make([]Pattern, 0)

	// Trend patterns
	trend := e.calculateTrend(values)
	if trend > 0.5 {
		patterns = append(patterns, Pattern{
			Type:        PatternTrendUp,
			Description: "Strong upward trend detected",
			StartTime:   ts.Points[0].Timestamp,
			EndTime:     ts.Points[len(ts.Points)-1].Timestamp,
			Confidence:  min(1.0, trend/2),
			Strength:    min(1.0, trend/2),
		})
	} else if trend < -0.5 {
		patterns = append(patterns, Pattern{
			Type:        PatternTrendDown,
			Description: "Strong downward trend detected",
			StartTime:   ts.Points[0].Timestamp,
			EndTime:     ts.Points[len(ts.Points)-1].Timestamp,
			Confidence:  min(1.0, -trend/2),
			Strength:    min(1.0, -trend/2),
		})
	}

	// Volatility pattern
	stdDev := e.standardDeviation(values)
	mean := e.mean(values)
	cv := stdDev / mean // Coefficient of variation
	if cv > 0.5 {
		patterns = append(patterns, Pattern{
			Type:        PatternVolatile,
			Description: "High volatility detected",
			StartTime:   ts.Points[0].Timestamp,
			EndTime:     ts.Points[len(ts.Points)-1].Timestamp,
			Confidence:  min(1.0, cv),
			Strength:    min(1.0, cv),
		})
	}

	// Spike detection
	mean = e.mean(values)
	threshold := mean + 2*stdDev
	spikeIndices := make([]int, 0)
	for i, v := range values {
		if v > threshold {
			spikeIndices = append(spikeIndices, i)
		}
	}
	if len(spikeIndices) > 0 {
		patterns = append(patterns, Pattern{
			Type:           PatternSpike,
			Description:    "Value spikes detected",
			StartTime:      ts.Points[spikeIndices[0]].Timestamp,
			EndTime:        ts.Points[spikeIndices[len(spikeIndices)-1]].Timestamp,
			Confidence:     0.8,
			Strength:       float64(len(spikeIndices)) / float64(len(values)),
			AffectedPoints: spikeIndices,
		})
	}

	// Drop detection
	dropThreshold := mean - 2*stdDev
	dropIndices := make([]int, 0)
	for i, v := range values {
		if v < dropThreshold {
			dropIndices = append(dropIndices, i)
		}
	}
	if len(dropIndices) > 0 {
		patterns = append(patterns, Pattern{
			Type:           PatternDrop,
			Description:    "Value drops detected",
			StartTime:      ts.Points[dropIndices[0]].Timestamp,
			EndTime:        ts.Points[dropIndices[len(dropIndices)-1]].Timestamp,
			Confidence:     0.8,
			Strength:       float64(len(dropIndices)) / float64(len(values)),
			AffectedPoints: dropIndices,
		})
	}

	// Stabilizing pattern (low variance at end)
	if len(values) >= 10 {
		recentValues := values[len(values)-10:]
		recentStdDev := e.standardDeviation(recentValues)
		if recentStdDev < stdDev*0.5 {
			patterns = append(patterns, Pattern{
				Type:        PatternStabilizing,
				Description: "Values stabilizing",
				StartTime:   ts.Points[len(values)-10].Timestamp,
				EndTime:     ts.Points[len(ts.Points)-1].Timestamp,
				Confidence:  0.7,
				Strength:    0.6,
			})
		}
	}

	// Filter by minimum strength
	filtered := make([]Pattern, 0)
	for _, p := range patterns {
		if p.Strength >= e.config.MinPatternStrength {
			filtered = append(filtered, p)
		}
	}

	return filtered, nil
}

// Cluster performs clustering on data points.
func (e *AnalyticsEngine) Cluster(ts *TimeSeries, k int) ([]Cluster, error) {
	if len(ts.Points) < k {
		return nil, ErrInsufficientData
	}

	if k <= 0 {
		k = e.config.DefaultClusters
	}
	if k > e.config.MaxClusters {
		k = e.config.MaxClusters
	}

	// Extract features (use value as 1D feature for simplicity)
	values := make([]float64, len(ts.Points))
	for i, p := range ts.Points {
		values[i] = p.Value
	}

	// K-means clustering (simplified for 1D)
	clusters := e.kMeans(values, k)

	// Calculate stats for each cluster
	for i := range clusters {
		clusterValues := make([]float64, 0)
		for _, idx := range clusters[i].Points {
			clusterValues = append(clusterValues, values[idx])
		}
		clusters[i].Stats = e.calculateClusterStats(clusterValues)
		clusters[i].MetricType = ts.MetricType
		clusters[i].ID = i
	}

	return clusters, nil
}

// kMeans performs k-means clustering on 1D data.
func (e *AnalyticsEngine) kMeans(data []float64, k int) []Cluster {
	// Initialize centroids using k-means++
	centroids := make([]float64, k)
	centroids[0] = data[0]
	for i := 1; i < k; i++ {
		maxDist := 0.0
		for _, d := range data {
			minDist := math.MaxFloat64
			for _, c := range centroids[:i] {
				dist := math.Abs(d - c)
				if dist < minDist {
					minDist = dist
				}
			}
			if minDist > maxDist {
				maxDist = minDist
				centroids[i] = d
			}
		}
	}

	// Iterate - with final assignments for building clusters
	var finalAssignments []int

	for iter := 0; iter < 100; iter++ {
		// Assign points to nearest centroid
		assignments := make([]int, len(data))
		for i, d := range data {
			minDist := math.MaxFloat64
			centroid := 0
			for j, c := range centroids {
				dist := math.Abs(d - c)
				if dist < minDist {
					minDist = dist
					centroid = j
				}
			}
			assignments[i] = centroid
		}

		// Update centroids
		newCentroids := make([]float64, k)
		counts := make([]int, k)
		for i, c := range centroids {
			newCentroids[i] = c
		}
		for i, d := range data {
			newCentroids[assignments[i]] += d
			counts[assignments[i]]++
		}
		for i := range newCentroids {
			if counts[i] > 0 {
				newCentroids[i] /= float64(counts[i])
			}
		}

		// Check convergence
		converged := true
		for i := range centroids {
			if math.Abs(centroids[i]-newCentroids[i]) > 0.001 {
				converged = false
				break
			}
		}
		centroids = newCentroids
		if converged {
			finalAssignments = assignments
			break
		}
	}

	if finalAssignments == nil {
		finalAssignments = make([]int, len(data))
		for i, d := range data {
			minDist := math.MaxFloat64
			centroid := 0
			for j, c := range centroids {
				dist := math.Abs(d - c)
				if dist < minDist {
					minDist = dist
					centroid = j
				}
			}
			finalAssignments[i] = centroid
		}
	}

	// Build clusters
	clusters := make([]Cluster, k)
	for i := range clusters {
		clusters[i].Center = []float64{centroids[i]}
		clusters[i].Points = make([]int, 0)
	}
	for i, a := range finalAssignments {
		clusters[a].Points = append(clusters[a].Points, i)
	}

	return clusters
}

// DetectAnomalies scores anomalies in time series.
func (e *AnalyticsEngine) DetectAnomalies(ts *TimeSeries) ([]AnomalyScore, error) {
	if len(ts.Points) < 5 {
		return nil, ErrInsufficientData
	}

	values := make([]float64, len(ts.Points))
	for i, p := range ts.Points {
		values[i] = p.Value
	}

	// Calculate statistics
	mean := e.mean(values)
	stdDev := e.standardDeviation(values)
	if stdDev == 0 {
		stdDev = 1
	}

	// Detect anomalies using multiple methods
	anomalies := make([]AnomalyScore, 0)

	for i, p := range ts.Points {
		// Z-score method
		zScore := math.Abs((p.Value - mean) / stdDev)

		// Local outlier factor (simplified)
		lof := e.localOutlierFactor(values, i, 3)

		// Combined score
		score := (zScore/3 + lof) / 2

		// Determine severity
		severity := "low"
		if score > e.config.AnomalyThreshold {
			if score > 0.9 {
				severity = "critical"
			} else if score > 0.8 {
				severity = "high"
			} else if score > 0.7 {
				severity = "medium"
			}
		}

		// Collect factors
		factors := make([]string, 0)
		if zScore > 2 {
			factors = append(factors, "z_score")
		}
		if lof > 1.5 {
			factors = append(factors, "lof")
		}

		if score > 0.3 { // Only include notable anomalies
			anomalies = append(anomalies, AnomalyScore{
				Timestamp:  p.Timestamp,
				Value:      p.Value,
				Expected:   mean,
				Deviation:  (p.Value - mean) / stdDev,
				Score:      score,
				Severity:   severity,
				Factors:    factors,
			})
		}
	}

	// Sort by score descending
	sort.Slice(anomalies, func(i, j int) bool {
		return anomalies[i].Score > anomalies[j].Score
	})

	return anomalies, nil
}

// localOutlierFactor calculates a simplified LOF for a point.
func (e *AnalyticsEngine) localOutlierFactor(values []float64, idx int, k int) float64 {
	n := len(values)
	if n < k+1 {
		return 1.0
	}

	// Find k nearest neighbors
	neighbors := make([]float64, 0, k)
	for i := range values {
		if i != idx {
			neighbors = append(neighbors, math.Abs(values[i]-values[idx]))
		}
	}
	sort.Float64s(neighbors)
	if len(neighbors) > k {
		neighbors = neighbors[:k]
	}

	// Calculate reachability distance
	kDist := neighbors[k-1] // k-distance
	reachDists := make([]float64, 0, k)
	for _, d := range neighbors {
		reachDists = append(reachDists, math.Max(d, kDist))
	}

	// Calculate LRD (local reachability density)
	lrd := 0.0
	for _, rd := range reachDists {
		lrd += rd
	}
	if len(reachDists) > 0 {
		lrd /= float64(len(reachDists))
	}
	if lrd == 0 {
		return 1.0
	}

	// Calculate LOF
	neighborsLOF := make([]float64, 0, k)
	for i := range values {
		if i != idx {
			_ = math.Abs(values[i] - values[idx]) // distance used for reachability
			sum := 0.0
			for _, rd := range reachDists {
				sum += rd
			}
			otherLRD := sum / float64(len(reachDists))
			if otherLRD > 0 {
				neighborsLOF = append(neighborsLOF, otherLRD/lrd)
			}
		}
	}

	lof := 0.0
	for _, nl := range neighborsLOF {
		lof += nl
	}
	if len(neighborsLOF) > 0 {
		lof /= float64(len(neighborsLOF))
	}

	return lof
}

// AnalyzeTrend performs comprehensive trend analysis.
func (e *AnalyticsEngine) AnalyzeTrend(ctx context.Context, ts *TimeSeries) (*TrendAnalysis, error) {
	if len(ts.Points) < 5 {
		return nil, ErrInsufficientData
	}

	values := make([]float64, len(ts.Points))
	for i, p := range ts.Points {
		values[i] = p.Value
	}

	// Linear regression for slope and R2
	n := float64(len(values))
	xMean := (n - 1) / 2
	yMean := e.mean(values)

	var num, den float64
	for i, y := range values {
		x := float64(i)
		num += (x - xMean) * (y - yMean)
		den += (x - xMean) * (x - xMean)
	}

	slope := num / den
	r2 := e.calculateR2(values, slope, xMean, yMean)

	// Overall trend
	overallTrend := "stable"
	if slope > 0.1 {
		overallTrend = "improving"
	} else if slope < -0.1 {
		overallTrend = "declining"
	}

	// Generate predictions
	forecast, err := e.Forecast(ctx, ts, e.config.DefaultHorizon, MethodLinearTrend)
	if err != nil {
		forecast = nil
	}

	// Detect patterns
	patterns, _ := e.DetectPatterns(ts)

	return &TrendAnalysis{
		MetricType:   ts.MetricType,
		OverallTrend: overallTrend,
		Strength:    min(1.0, math.Abs(slope)/2),
		Slope:       slope,
		R2:          r2,
		Period:      "historical",
		Predictions: forecast.Values,
		Patterns:    patterns,
	}, nil
}

// CalculateR2 calculates R-squared for linear regression.
func (e *AnalyticsEngine) calculateR2(values []float64, slope, xMean, yMean float64) float64 {
	var ssTot, ssRes float64
	for i, y := range values {
		x := float64(i)
		pred := slope*x + xMean + yMean - xMean
		ssRes += (y - pred) * (y - pred)
		ssTot += (y - yMean) * (y - yMean)
	}
	if ssTot == 0 {
		return 1.0
	}
	return 1 - ssRes/ssTot
}

// Helper functions

func (e *AnalyticsEngine) mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func (e *AnalyticsEngine) standardDeviation(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	m := e.mean(values)
	var sumSqDiff float64
	for _, v := range values {
		diff := v - m
		sumSqDiff += diff * diff
	}
	return math.Sqrt(sumSqDiff / float64(len(values)-1))
}

func (e *AnalyticsEngine) movingAverage(values []float64, window int) []float64 {
	if window > len(values) {
		window = len(values)
	}
	result := make([]float64, len(values))
	for i := range values {
		start := max(0, i-window+1)
		sum := 0.0
		for j := start; j <= i; j++ {
			sum += values[j]
		}
		result[i] = sum / float64(i-start+1)
	}
	return result
}

func (e *AnalyticsEngine) calculateTrend(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	firstHalf := values[:len(values)/2]
	secondHalf := values[len(values)/2:]
	return (e.mean(secondHalf) - e.mean(firstHalf)) / (e.mean(firstHalf) + 1e-6)
}

func (e *AnalyticsEngine) calculateClusterStats(values []float64) ClusterStats {
	if len(values) == 0 {
		return ClusterStats{}
	}
	m := e.mean(values)
	stdDev := e.standardDeviation(values)

	minVal := values[0]
	maxVal := values[0]
	for _, v := range values {
		if v < minVal {
			minVal = v
		}
		if v > maxVal {
			maxVal = v
		}
	}

	return ClusterStats{
		Count:  len(values),
		Mean:   m,
		StdDev: stdDev,
		Min:    minVal,
		Max:    maxVal,
		Spread: maxVal - minVal,
	}
}

// Errors

var (
	ErrInsufficientData = fmt.Errorf("insufficient data for analysis")
)
