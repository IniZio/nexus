package team

import (
	"context"
	"testing"
	"time"
)

// TestTeamAnalyticsEngine tests the main engine.
func TestTeamAnalyticsEngine(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	if engine == nil {
		t.Fatal("Engine should not be nil")
	}

	if len(engine.members) != 0 {
		t.Error("Engine should start with empty members")
	}

	if len(engine.teams) != 0 {
		t.Error("Engine should start with empty teams")
	}

	if engine.config.DefaultPeriod != "monthly" {
		t.Error("Default period should be monthly")
	}

	if engine.config.VelocityWeight != 0.30 {
		t.Error("Velocity weight should be 0.30")
	}
}

// TestTeamAnalyticsEngine_CustomConfig tests custom configuration.
func TestTeamAnalyticsEngine_CustomConfig(t *testing.T) {
	config := &AnalyticsConfig{
		DefaultPeriod:       "weekly",
		MaxHistoryPeriod:    90 * 24 * time.Hour,
		VelocityWeight:      0.40,
		QualityWeight:       0.25,
		EfficiencyWeight:    0.20,
		CollaborationWeight: 0.15,
		MinParticipationRate: 0.6,
		ActiveThresholdHours: 20,
	}

	engine := NewTeamAnalyticsEngine(config)

	if engine.config.DefaultPeriod != "weekly" {
		t.Error("Default period should be weekly")
	}

	if engine.config.VelocityWeight != 0.40 {
		t.Error("Velocity weight should be 0.40")
	}
}

// Member Management Tests

func TestAddMember(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	member := &TeamMember{
		ID:    "member-001",
		Name:  "Alice Smith",
		Email: "alice@example.com",
		Role:  RoleSenior,
	}

	err := engine.AddMember(member)
	if err != nil {
		t.Fatalf("AddMember() error = %v", err)
	}

	retrieved, err := engine.GetMember("member-001")
	if err != nil {
		t.Fatalf("GetMember() error = %v", err)
	}

	if retrieved.Name != "Alice Smith" {
		t.Errorf("Member name = %v, want Alice Smith", retrieved.Name)
	}
}

func TestAddMember_InvalidID(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	member := &TeamMember{
		ID:   "",
		Name: "Alice",
	}

	err := engine.AddMember(member)
	if err != ErrInvalidMemberID {
		t.Errorf("AddMember() should return ErrInvalidMemberID, got %v", err)
	}
}

func TestAddMember_InvalidName(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	member := &TeamMember{
		ID:   "member-001",
		Name: "",
	}

	err := engine.AddMember(member)
	if err != ErrInvalidMemberName {
		t.Errorf("AddMember() should return ErrInvalidMemberName, got %v", err)
	}
}

func TestGetMember_NotFound(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	_, err := engine.GetMember("nonexistent")
	if err != ErrMemberNotFound {
		t.Errorf("GetMember() should return ErrMemberNotFound, got %v", err)
	}
}

func TestListMembers(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	// Add some members
	engine.AddMember(&TeamMember{ID: "m1", Name: "Alice"})
	engine.AddMember(&TeamMember{ID: "m2", Name: "Bob"})
	engine.AddMember(&TeamMember{ID: "m3", Name: "Charlie"})

	members := engine.ListMembers()

	if len(members) != 3 {
		t.Errorf("ListMembers() returned %d members, want 3", len(members))
	}
}

func TestUpdateMember(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	engine.AddMember(&TeamMember{ID: "m1", Name: "Alice", Role: RoleMid})

	err := engine.UpdateMember("m1", map[string]interface{}{
		"name": "Alice Smith",
		"role": RoleSenior,
	})
	if err != nil {
		t.Fatalf("UpdateMember() error = %v", err)
	}

	member, _ := engine.GetMember("m1")
	if member.Name != "Alice Smith" {
		t.Errorf("Member name = %v, want Alice Smith", member.Name)
	}

	if member.Role != RoleSenior {
		t.Errorf("Member role = %v, want senior", member.Role)
	}
}

func TestUpdateMember_NotFound(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	err := engine.UpdateMember("nonexistent", map[string]interface{}{
		"name": "New Name",
	})
	if err != ErrMemberNotFound {
		t.Errorf("UpdateMember() should return ErrMemberNotFound, got %v", err)
	}
}

// Team Management Tests

func TestAddTeam(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	team := &Team{
		ID:          "team-001",
		Name:        "Backend Team",
		Description: "Backend development team",
		LeadID:      "lead-001",
	}

	err := engine.AddTeam(team)
	if err != nil {
		t.Fatalf("AddTeam() error = %v", err)
	}

	retrieved, err := engine.GetTeam("team-001")
	if err != nil {
		t.Fatalf("GetTeam() error = %v", err)
	}

	if retrieved.Name != "Backend Team" {
		t.Errorf("Team name = %v, want Backend Team", retrieved.Name)
	}

	if retrieved.Active != true {
		t.Error("Team should be active")
	}
}

func TestAddTeam_InvalidID(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	team := &Team{
		ID:   "",
		Name: "Test Team",
	}

	err := engine.AddTeam(team)
	if err != ErrInvalidTeamID {
		t.Errorf("AddTeam() should return ErrInvalidTeamID, got %v", err)
	}
}

func TestAddTeam_InvalidName(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	team := &Team{
		ID:   "team-001",
		Name: "",
	}

	err := engine.AddTeam(team)
	if err != ErrInvalidTeamName {
		t.Errorf("AddTeam() should return ErrInvalidTeamName, got %v", err)
	}
}

func TestGetTeam_NotFound(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	_, err := engine.GetTeam("nonexistent")
	if err != ErrTeamNotFound {
		t.Errorf("GetTeam() should return ErrTeamNotFound, got %v", err)
	}
}

func TestAddTeamMember(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	// Setup
	engine.AddMember(&TeamMember{ID: "m1", Name: "Alice"})
	engine.AddTeam(&Team{ID: "team-001", Name: "Test Team"})

	// Add member to team
	err := engine.AddTeamMember("team-001", "m1")
	if err != nil {
		t.Fatalf("AddTeamMember() error = %v", err)
	}

	team, _ := engine.GetTeam("team-001")
	if len(team.MemberIDs) != 1 {
		t.Errorf("Team should have 1 member, got %d", len(team.MemberIDs))
	}

	if team.MemberIDs[0] != "m1" {
		t.Errorf("Team member = %v, want m1", team.MemberIDs[0])
	}
}

func TestAddTeamMember_MemberNotFound(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	engine.AddTeam(&Team{ID: "team-001", Name: "Test Team"})

	err := engine.AddTeamMember("team-001", "nonexistent")
	if err != ErrMemberNotFound {
		t.Errorf("AddTeamMember() should return ErrMemberNotFound, got %v", err)
	}
}

func TestAddTeamMember_AlreadyInTeam(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	engine.AddMember(&TeamMember{ID: "m1", Name: "Alice"})
	engine.AddTeam(&Team{ID: "team-001", Name: "Test Team"})
	engine.AddTeamMember("team-001", "m1")

	// Try to add again
	err := engine.AddTeamMember("team-001", "m1")
	if err != ErrMemberAlreadyInTeam {
		t.Errorf("AddTeamMember() should return ErrMemberAlreadyInTeam, got %v", err)
	}
}

// Metrics Tests

func TestRecordMetrics(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	engine.AddMember(&TeamMember{ID: "m1", Name: "Alice"})

	metrics := PerformanceMetrics{
		MemberID:         "m1",
		Period:           "monthly",
		TasksCompleted:   25,
		PointsCompleted:  50,
		Velocity:         5.0,
		BugRate:          2.0,
		CodeReviewScore:  85,
		PRMergeRate:      0.9,
		CycleTime:        24,
		LeadTime:         12,
		WorkInProgress:   3,
		ReviewsGiven:     15,
		ReviewsReceived:  10,
		CommentsAdded:    50,
		HelpRequests:     5,
		StartDate:        time.Now().Add(-30 * 24 * time.Hour),
		EndDate:          time.Now(),
	}

	err := engine.RecordMetrics(metrics)
	if err != nil {
		t.Fatalf("RecordMetrics() error = %v", err)
	}

	retrieved, err := engine.GetMemberMetrics("m1", "monthly")
	if err != nil {
		t.Fatalf("GetMemberMetrics() error = %v", err)
	}

	if len(retrieved) != 1 {
		t.Errorf("GetMemberMetrics() returned %d metrics, want 1", len(retrieved))
	}

	if retrieved[0].TasksCompleted != 25 {
		t.Errorf("TasksCompleted = %v, want 25", retrieved[0].TasksCompleted)
	}
}

func TestRecordMetrics_MemberNotFound(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	metrics := PerformanceMetrics{
		MemberID: "nonexistent",
		Period:   "monthly",
	}

	err := engine.RecordMetrics(metrics)
	if err != ErrMemberNotFound {
		t.Errorf("RecordMetrics() should return ErrMemberNotFound, got %v", err)
	}
}

func TestRecordMetrics_InvalidMemberID(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	metrics := PerformanceMetrics{
		MemberID: "",
		Period:   "monthly",
	}

	err := engine.RecordMetrics(metrics)
	if err != ErrInvalidMemberID {
		t.Errorf("RecordMetrics() should return ErrInvalidMemberID, got %v", err)
	}
}

func TestGetMemberMetrics_NotFound(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	_, err := engine.GetMemberMetrics("nonexistent", "monthly")
	if err != ErrMetricsNotFound {
		t.Errorf("GetMemberMetrics() should return ErrMetricsNotFound, got %v", err)
	}
}

func TestGetLatestMetrics(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	engine.AddMember(&TeamMember{ID: "m1", Name: "Alice"})

	// Record multiple metrics
	for i := 1; i <= 3; i++ {
		engine.RecordMetrics(PerformanceMetrics{
			MemberID:    "m1",
			Period:      "monthly",
			Velocity:    float64(i * 2),
			StartDate:   time.Now().Add(-time.Duration(i) * 30 * 24 * time.Hour),
			EndDate:     time.Now().Add(-time.Duration(i-1) * 30 * 24 * time.Hour),
		})
	}

	latest, err := engine.GetLatestMetrics("m1")
	if err != nil {
		t.Fatalf("GetLatestMetrics() error = %v", err)
	}

	if latest.Velocity != 6.0 {
		t.Errorf("Latest velocity = %v, want 6.0", latest.Velocity)
	}
}

// TeamMetrics Tests

func TestCalculateTeamMetrics(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	// Setup members and team
	engine.AddMember(&TeamMember{ID: "m1", Name: "Alice"})
	engine.AddMember(&TeamMember{ID: "m2", Name: "Bob"})
	engine.AddTeam(&Team{ID: "team-001", Name: "Test Team", MemberIDs: []string{"m1", "m2"}})

	// Record metrics for both members
	engine.RecordMetrics(PerformanceMetrics{
		MemberID:       "m1",
		Period:         "monthly",
		TasksCompleted: 20,
		PointsCompleted: 40,
		Velocity:       5.0,
		BugRate:        2.0,
		CodeReviewScore: 85,
		CycleTime:      24,
		LeadTime:       12,
		ReviewsGiven:   10,
		ReviewsReceived: 8,
		StartDate:      time.Now().Add(-30 * 24 * time.Hour),
		EndDate:        time.Now(),
	})

	engine.RecordMetrics(PerformanceMetrics{
		MemberID:        "m2",
		Period:          "monthly",
		TasksCompleted:  25,
		PointsCompleted: 50,
		Velocity:        6.0,
		BugRate:         3.0,
		CodeReviewScore: 90,
		CycleTime:       20,
		LeadTime:        10,
		ReviewsGiven:    15,
		ReviewsReceived: 12,
		StartDate:       time.Now().Add(-30 * 24 * time.Hour),
		EndDate:         time.Now(),
	})

	metrics, err := engine.CalculateTeamMetrics("team-001", "monthly")
	if err != nil {
		t.Fatalf("CalculateTeamMetrics() error = %v", err)
	}

	if metrics.TotalTasksCompleted != 45 {
		t.Errorf("TotalTasksCompleted = %v, want 45", metrics.TotalTasksCompleted)
	}

	if metrics.TotalPointsCompleted != 90 {
		t.Errorf("TotalPointsCompleted = %v, want 90", metrics.TotalPointsCompleted)
	}

	if metrics.TeamVelocity != 5.5 {
		t.Errorf("TeamVelocity = %v, want 5.5", metrics.TeamVelocity)
	}

	if metrics.MemberCount != 2 {
		t.Errorf("MemberCount = %v, want 2", metrics.MemberCount)
	}
}

func TestCalculateTeamMetrics_NoMetrics(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	engine.AddTeam(&Team{ID: "team-001", Name: "Test Team"})

	_, err := engine.CalculateTeamMetrics("team-001", "monthly")
	if err != ErrMetricsNotFound {
		t.Errorf("CalculateTeamMetrics() should return ErrMetricsNotFound, got %v", err)
	}
}

// Report Tests

func TestGenerateReport_Individual(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	engine.AddMember(&TeamMember{ID: "m1", Name: "Alice"})
	engine.RecordMetrics(PerformanceMetrics{
		MemberID:        "m1",
		Period:          "monthly",
		Velocity:        5.0,
		BugRate:         2.0,
		CodeReviewScore: 85,
		CycleTime:       24,
		LeadTime:        12,
		ReviewsGiven:    10,
		ReviewsReceived: 8,
		StartDate:       time.Now().Add(-30 * 24 * time.Hour),
		EndDate:         time.Now(),
	})

	report, err := engine.GenerateReport(context.Background(), "individual", "m1", "monthly")
	if err != nil {
		t.Fatalf("GenerateReport() error = %v", err)
	}

	if report.Type != "individual" {
		t.Errorf("Report type = %v, want individual", report.Type)
	}

	if report.SubjectID != "m1" {
		t.Errorf("SubjectID = %v, want m1", report.SubjectID)
	}

	if report.SubjectName != "Alice" {
		t.Errorf("SubjectName = %v, want Alice", report.SubjectName)
	}

	if report.OverallScore == 0 {
		t.Error("OverallScore should not be 0")
	}
}

func TestGenerateReport_Team(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	engine.AddMember(&TeamMember{ID: "m1", Name: "Alice"})
	engine.AddTeam(&Team{ID: "team-001", Name: "Test Team", MemberIDs: []string{"m1"}})
	engine.RecordMetrics(PerformanceMetrics{
		MemberID:        "m1",
		Period:          "monthly",
		Velocity:        5.0,
		BugRate:         2.0,
		CodeReviewScore: 85,
		CycleTime:       24,
		LeadTime:        12,
		ReviewsGiven:    10,
		ReviewsReceived: 8,
		StartDate:       time.Now().Add(-30 * 24 * time.Hour),
		EndDate:         time.Now(),
	})

	report, err := engine.GenerateReport(context.Background(), "team", "team-001", "monthly")
	if err != nil {
		t.Fatalf("GenerateReport() error = %v", err)
	}

	if report.Type != "team" {
		t.Errorf("Report type = %v, want team", report.Type)
	}

	if report.TeamMetrics == nil {
		t.Error("TeamMetrics should not be nil")
	}
}

func TestGenerateReport_InvalidType(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	_, err := engine.GenerateReport(context.Background(), "invalid", "m1", "monthly")
	if err != ErrInvalidReportType {
		t.Errorf("GenerateReport() should return ErrInvalidReportType, got %v", err)
	}
}

func TestGenerateReport_MemberNotFound(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	_, err := engine.GenerateReport(context.Background(), "individual", "nonexistent", "monthly")
	if err != ErrMemberNotFound {
		t.Errorf("GenerateReport() should return ErrMemberNotFound, got %v", err)
	}
}

// Leaderboard Tests

func TestGetLeaderboard(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	engine.AddMember(&TeamMember{ID: "m1", Name: "Alice"})
	engine.AddMember(&TeamMember{ID: "m2", Name: "Bob"})
	engine.AddMember(&TeamMember{ID: "m3", Name: "Charlie"})

	// Record different velocities
	engine.RecordMetrics(PerformanceMetrics{MemberID: "m1", Period: "monthly", Velocity: 5.0, StartDate: time.Now(), EndDate: time.Now()})
	engine.RecordMetrics(PerformanceMetrics{MemberID: "m2", Period: "monthly", Velocity: 8.0, StartDate: time.Now(), EndDate: time.Now()})
	engine.RecordMetrics(PerformanceMetrics{MemberID: "m3", Period: "monthly", Velocity: 3.0, StartDate: time.Now(), EndDate: time.Now()})

	leaderboard, err := engine.GetLeaderboard("velocity", "monthly", 0)
	if err != nil {
		t.Fatalf("GetLeaderboard() error = %v", err)
	}

	if len(leaderboard) != 3 {
		t.Errorf("Leaderboard has %d entries, want 3", len(leaderboard))
	}

	// Bob should be first (highest velocity)
	if leaderboard[0].MemberName != "Bob" {
		t.Errorf("First rank = %v, want Bob", leaderboard[0].MemberName)
	}

	if leaderboard[0].Rank != 1 {
		t.Errorf("First rank number = %v, want 1", leaderboard[0].Rank)
	}

	// Charlie should be last
	if leaderboard[2].MemberName != "Charlie" {
		t.Errorf("Last rank = %v, want Charlie", leaderboard[2].MemberName)
	}
}

func TestGetLeaderboard_WithLimit(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	for i := 1; i <= 5; i++ {
		engine.AddMember(&TeamMember{ID: "m" + string(rune('0'+i)), Name: string(rune('A' + i - 1))})
		engine.RecordMetrics(PerformanceMetrics{MemberID: "m" + string(rune('0'+i)), Period: "monthly", Velocity: float64(i * 2), StartDate: time.Now(), EndDate: time.Now()})
	}

	leaderboard, _ := engine.GetLeaderboard("velocity", "monthly", 3)

	if len(leaderboard) != 3 {
		t.Errorf("Leaderboard has %d entries, want 3", len(leaderboard))
	}
}

// Benchmark Tests

func TestAddBenchmark(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	benchmark := &Benchmark{
		ID:          "bench-001",
		Name:        "Velocity Target",
		Description: "Monthly velocity target",
		Category:    CategoryVelocity,
		MetricType:  "velocity",
		MinValue:    3.0,
		TargetValue: 5.0,
		MaxValue:    7.0,
		Percentile:  0.5,
		Period:      "monthly",
	}

	err := engine.AddBenchmark(benchmark)
	if err != nil {
		t.Fatalf("AddBenchmark() error = %v", err)
	}

	retrieved, err := engine.GetBenchmark("bench-001")
	if err != nil {
		t.Fatalf("GetBenchmark() error = %v", err)
	}

	if retrieved.Name != "Velocity Target" {
		t.Errorf("Benchmark name = %v, want Velocity Target", retrieved.Name)
	}
}

func TestGetBenchmark_NotFound(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	_, err := engine.GetBenchmark("nonexistent")
	if err != ErrBenchmarkNotFound {
		t.Errorf("GetBenchmark() should return ErrBenchmarkNotFound, got %v", err)
	}
}

func TestAddBenchmark_AutoID(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	benchmark := &Benchmark{
		Name:        "Auto ID Benchmark",
		Category:    CategoryQuality,
		MetricType:  "code_review_score",
		TargetValue: 80,
	}

	err := engine.AddBenchmark(benchmark)
	if err != nil {
		t.Fatalf("AddBenchmark() error = %v", err)
	}

	if benchmark.ID == "" {
		t.Error("Benchmark ID should be auto-generated")
	}
}

// Helper Function Tests

func TestMean(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	values := []float64{1.0, 2.0, 3.0, 4.0, 5.0}
	result := engine.mean(values)

	if result != 3.0 {
		t.Errorf("mean() = %v, want 3.0", result)
	}

	// Empty slice
	emptyResult := engine.mean([]float64{})
	if emptyResult != 0 {
		t.Errorf("mean() of empty slice = %v, want 0", emptyResult)
	}
}

func TestCalculateOverallScore(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	metrics := &PerformanceMetrics{
		Velocity:        5.0,
		CodeReviewScore: 85,
		CycleTime:       24,
		ReviewsGiven:    10,
		ReviewsReceived: 8,
	}

	score := engine.calculateOverallScore(metrics)

	if score < 0 || score > 100 {
		t.Errorf("Score = %v, should be between 0 and 100", score)
	}
}

func TestCalculateCollaborationScore(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	// Good collaboration
	score := engine.calculateCollaborationScore(20, 20, 5)
	if score <= 0 {
		t.Error("Collaboration score should be positive for active team")
	}

	// Zero members
	zeroScore := engine.calculateCollaborationScore(10, 10, 0)
	if zeroScore != 0 {
		t.Error("Collaboration score should be 0 for zero members")
	}
}

// Validation Error Tests

func TestValidationError(t *testing.T) {
	err := &ValidationError{Message: "test error"}

	if err.Error() != "test error" {
		t.Errorf("Error message = %v, want test error", err.Error())
	}
}

// Constants Tests

func TestMemberRole_Constants(t *testing.T) {
	roles := []MemberRole{RoleLead, RoleSenior, RoleMid, RoleJunior, RoleContractor}
	expected := []string{"lead", "senior", "mid", "junior", "contractor"}

	for i, role := range roles {
		if string(role) != expected[i] {
			t.Errorf("Role[%d] = %v, want %v", i, role, expected[i])
		}
	}
}

func TestMetricCategory_Constants(t *testing.T) {
	categories := []MetricCategory{CategoryVelocity, CategoryQuality, CategoryEfficiency, CategoryCollaboration, CategoryGrowth}
	expected := []string{"velocity", "quality", "efficiency", "collaboration", "growth"}

	for i, cat := range categories {
		if string(cat) != expected[i] {
			t.Errorf("Category[%d] = %v, want %v", i, cat, expected[i])
		}
	}
}

// Structure Tests

func TestPerformanceMetrics_Structure(t *testing.T) {
	metrics := PerformanceMetrics{
		MemberID:         "m1",
		Period:           "monthly",
		TasksCompleted:   25,
		PointsCompleted:  50,
		Velocity:         5.0,
		BugRate:          2.0,
		CodeReviewScore:  85,
		PRMergeRate:      0.9,
		CycleTime:        24,
		LeadTime:         12,
		WorkInProgress:   3,
		ReviewsGiven:     15,
		ReviewsReceived:  10,
		CommentsAdded:    50,
		HelpRequests:     5,
		SkillsAcquired:   []string{"Go", "Kubernetes"},
		Certifications:   []string{"AWS SA"},
		TrainingHours:    10,
	}

	if metrics.TasksCompleted != 25 {
		t.Error("TasksCompleted should be 25")
	}

	if len(metrics.SkillsAcquired) != 2 {
		t.Error("Should have 2 skills acquired")
	}
}

func TestTeamMetrics_Structure(t *testing.T) {
	metrics := TeamMetrics{
		TeamID:             "team-001",
		Period:             "monthly",
		TotalTasksCompleted: 100,
		TotalPointsCompleted: 200,
		TeamVelocity:        5.5,
		VelocityTrend:       "up",
		AvgBugRate:          2.5,
		AvgCodeReviewScore:  85,
		BugTrend:            "stable",
		AvgCycleTime:        24,
		AvgLeadTime:         12,
		EfficiencyTrend:     "up",
		TotalReviewsGiven:   50,
		TotalReviewsReceived: 40,
		CollaborationScore:  75,
		MemberCount:         5,
		ActiveMembers:       4,
		ParticipationRate:   0.8,
	}

	if metrics.TeamVelocity != 5.5 {
		t.Errorf("TeamVelocity = %v, want 5.5", metrics.TeamVelocity)
	}

	if metrics.MemberCount != 5 {
		t.Errorf("MemberCount = %v, want 5", metrics.MemberCount)
	}
}

func TestBenchmark_Structure(t *testing.T) {
	benchmark := Benchmark{
		ID:          "bench-001",
		Name:        "Velocity Target",
		Description: "Monthly velocity target",
		Category:    CategoryVelocity,
		MetricType:  "velocity",
		MinValue:    3.0,
		TargetValue: 5.0,
		MaxValue:    7.0,
		Percentile:  0.5,
		Period:      "monthly",
		IndustryAvg: 4.5,
	}

	if benchmark.TargetValue != 5.0 {
		t.Errorf("TargetValue = %v, want 5.0", benchmark.TargetValue)
	}
}

func TestPerformanceReport_Structure(t *testing.T) {
	report := PerformanceReport{
		ID:            "report-001",
		Title:         "Monthly Performance",
		Type:          "individual",
		SubjectID:     "m1",
		SubjectName:   "Alice",
		Period:        "monthly",
		OverallScore:  75,
		Highlights:    []string{"Great velocity"},
		Improvements:  []string{"Improve code reviews"},
		Benchmarks:    []BenchmarkScore{},
		TrendAnalysis: []TrendPoint{},
	}

	if report.OverallScore != 75 {
		t.Errorf("OverallScore = %v, want 75", report.OverallScore)
	}

	if len(report.Highlights) != 1 {
		t.Errorf("Highlights count = %v, want 1", len(report.Highlights))
	}
}

func TestLeaderboardEntry_Structure(t *testing.T) {
	entry := LeaderboardEntry{
		Rank:        1,
		MemberID:    "m1",
		MemberName:  "Alice",
		TeamID:      "team-001",
		Score:       95.5,
		MetricType:  "velocity",
		Period:      "monthly",
		Details:     map[string]float64{"tasks": 25, "points": 50},
	}

	if entry.Rank != 1 {
		t.Errorf("Rank = %v, want 1", entry.Rank)
	}

	if entry.Score != 95.5 {
		t.Errorf("Score = %v, want 95.5", entry.Score)
	}
}

// Edge Cases

func TestGetLeaderboard_EmptyMetrics(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	engine.AddMember(&TeamMember{ID: "m1", Name: "Alice"})
	// No metrics recorded

	leaderboard, err := engine.GetLeaderboard("velocity", "monthly", 0)
	if err != nil {
		t.Fatalf("GetLeaderboard() error = %v", err)
	}

	if len(leaderboard) != 0 {
		t.Errorf("Leaderboard should be empty, got %d entries", len(leaderboard))
	}
}

func TestCalculateTeamMetrics_EmptyTeam(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	engine.AddTeam(&Team{ID: "team-001", Name: "Empty Team", MemberIDs: []string{}})

	_, err := engine.CalculateTeamMetrics("team-001", "monthly")
	if err != ErrMetricsNotFound {
		t.Errorf("Should return ErrMetricsNotFound for empty team without metrics, got %v", err)
	}
}

func TestGenerateReport_MemberNoMetrics(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	engine.AddMember(&TeamMember{ID: "m1", Name: "Alice"})

	_, err := engine.GenerateReport(context.Background(), "individual", "m1", "monthly")
	if err != ErrMetricsNotFound {
		t.Errorf("GenerateReport() should return ErrMetricsNotFound, got %v", err)
	}
}

// Benchmark comparison tests

func TestCompareToBenchmarks(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	// Add benchmarks
	engine.AddBenchmark(&Benchmark{
		ID:          "b1",
		Name:        "Velocity Target",
		MetricType:  "velocity",
		TargetValue: 5.0,
		TeamID:      "", // Org-wide
	})
	engine.AddBenchmark(&Benchmark{
		ID:          "b2",
		Name:        "Bug Rate Target",
		MetricType:  "bug_rate",
		TargetValue: 3.0,
		TeamID:      "", // Org-wide
	})

	metrics := &PerformanceMetrics{
		Velocity:        6.0,
		BugRate:         2.0,
		CodeReviewScore: 85,
		CycleTime:       24,
	}

	scores := engine.compareToBenchmarks(metrics)

	if len(scores) != 2 {
		t.Errorf("Expected 2 benchmark scores, got %d", len(scores))
	}

	// Velocity should be positive (above benchmark)
	for _, s := range scores {
		if s.MetricType == "velocity" && s.Score <= 0 {
			t.Error("Velocity should be above benchmark")
		}
		if s.MetricType == "bug_rate" && s.Score <= 0 {
			t.Error("Bug rate below target should be positive score")
		}
	}
}

func TestGenerateHighlights(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	// High velocity
	highVelocity := &PerformanceMetrics{Velocity: 10.0}
	highlights := engine.generateHighlights(highVelocity)
	found := false
	for _, h := range highlights {
		if h == "Excellent velocity consistently above team average" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Should generate velocity highlight")
	}

	// Low reviews
	lowReviews := &PerformanceMetrics{ReviewsGiven: 2}
	improvements := engine.generateImprovements(lowReviews)
	found = false
	for _, i := range improvements {
		if i == "Increase participation in code reviews" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Should generate review improvement suggestion")
	}
}

// Trend analysis tests

func TestGenerateTrendAnalysis(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	metrics := []PerformanceMetrics{
		{Period: "jan", Velocity: 4.0},
		{Period: "feb", Velocity: 5.0},
		{Period: "mar", Velocity: 4.5},
	}

	trends := engine.generateTrendAnalysis(metrics)

	if len(trends) != 3 {
		t.Errorf("Expected 3 trend points, got %d", len(trends))
	}

	// First point should have no change
	if trends[0].Change != 0 {
		t.Errorf("First trend change should be 0, got %v", trends[0].Change)
	}

	// Feb should be up
	if trends[1].Trend != "up" {
		t.Errorf("Feb trend should be up, got %s", trends[1].Trend)
	}

	// Mar should be down
	if trends[2].Trend != "down" {
		t.Errorf("Mar trend should be down, got %s", trends[2].Trend)
	}
}

func TestGenerateTrendAnalysis_TooFew(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	metrics := []PerformanceMetrics{
		{Period: "jan", Velocity: 4.0},
	}

	trends := engine.generateTrendAnalysis(metrics)

	if trends != nil {
		t.Error("Should return nil for less than 2 metrics")
	}
}

// Team highlights and improvements tests

func TestGenerateTeamHighlights(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	metrics := &TeamMetrics{
		VelocityTrend:     "up",
		ParticipationRate: 0.95,
		CollaborationScore: 85,
	}

	highlights := engine.generateTeamHighlights(metrics)

	if len(highlights) < 2 {
		t.Error("Should generate multiple highlights for good team metrics")
	}
}

func TestGenerateTeamImprovements(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	metrics := &TeamMetrics{
		AvgBugRate:      6.0,
		ParticipationRate: 0.6,
	}

	improvements := engine.generateTeamImprovements(metrics)

	if len(improvements) < 2 {
		t.Error("Should generate multiple improvement suggestions")
	}
}

// Leaderboard collaboration test

func TestGetLeaderboard_Collaboration(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	engine.AddMember(&TeamMember{ID: "m1", Name: "Alice"})
	engine.AddMember(&TeamMember{ID: "m2", Name: "Bob"})

	engine.RecordMetrics(PerformanceMetrics{
		MemberID:        "m1",
		Period:          "monthly",
		ReviewsGiven:    30,
		ReviewsReceived: 25,
		StartDate:       time.Now(),
		EndDate:         time.Now(),
	})
	engine.RecordMetrics(PerformanceMetrics{
		MemberID:        "m2",
		Period:          "monthly",
		ReviewsGiven:    10,
		ReviewsReceived: 8,
		StartDate:       time.Now(),
		EndDate:         time.Now(),
	})

	leaderboard, _ := engine.GetLeaderboard("collaboration", "monthly", 0)

	if leaderboard[0].MemberName != "Alice" {
		t.Error("Alice should rank first in collaboration")
	}
}

// Benchmarks with team-specific filtering

func TestCompareToBenchmarks_TeamSpecific(t *testing.T) {
	engine := NewTeamAnalyticsEngine(nil)

	// Add org-wide benchmark
	engine.AddBenchmark(&Benchmark{
		ID:       "org-bench",
		Name:     "Org Velocity",
		MetricType: "velocity",
		TargetValue: 5.0,
		TeamID:   "", // Org-wide
	})

	// Add team-specific benchmark
	engine.AddBenchmark(&Benchmark{
		ID:       "team-bench",
		Name:     "Team Velocity",
		MetricType: "velocity",
		TargetValue: 6.0,
		TeamID:   "team-001", // Team-specific
	})

	metrics := &PerformanceMetrics{Velocity: 5.5}
	scores := engine.compareToBenchmarks(metrics)

	// Should only include org-wide benchmarks
	if len(scores) != 1 {
		t.Errorf("Expected 1 score (org-wide only), got %d", len(scores))
	}
}

// Error constants test

func TestErrorConstants(t *testing.T) {
	tests := []struct {
		err      error
		expected string
	}{
		{ErrInvalidMemberID, "invalid member ID"},
		{ErrInvalidMemberName, "invalid member name"},
		{ErrInvalidTeamID, "invalid team ID"},
		{ErrInvalidTeamName, "invalid team name"},
		{ErrMemberNotFound, "member not found"},
		{ErrTeamNotFound, "team not found"},
		{ErrMemberAlreadyInTeam, "member already in team"},
		{ErrMetricsNotFound, "metrics not found"},
		{ErrBenchmarkNotFound, "benchmark not found"},
		{ErrInvalidReportType, "invalid report type"},
	}

	for _, tt := range tests {
		if tt.err.Error() != tt.expected {
			t.Errorf("Error = %v, want %v", tt.err.Error(), tt.expected)
		}
	}
}
