package cli

import (
	"os"
	"path/filepath"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/telemetry"
)

var telemetryCollector *telemetry.Collector

func initTelemetry() error {
	if telemetryCollector != nil {
		return nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	dbPath := filepath.Join(homeDir, ".nexus", "telemetry.db")

	cfg := telemetry.Config{
		Enabled:       true,
		Anonymize:     true,
		RetentionDays: 30,
	}

	collector, err := telemetry.NewCollector(dbPath, cfg)
	if err != nil {
		return err
	}

	telemetryCollector = collector
	collector.RecordSessionStart()

	return nil
}

func recordCommand(cmd string, args []string, duration time.Duration, success bool, err error) {
	if telemetryCollector == nil {
		return
	}
	telemetryCollector.RecordCommand(cmd, args, duration, success, err)
}

func closeTelemetry() {
	if telemetryCollector != nil {
		telemetryCollector.RecordSessionEnd("")
		telemetryCollector.Close()
		telemetryCollector = nil
	}
}
