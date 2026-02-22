package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/telemetry"
	"github.com/spf13/cobra"
)

var (
	traceLimitFlag     int
	traceFromFlag      string
	traceSpansFlag     bool
	traceFormatFlag    string
	traceOutputFlag    string
	traceOlderThanFlag int
)

func init() {
	traceListCmd.Flags().IntVarP(&traceLimitFlag, "limit", "n", 50, "Maximum number of traces to list")
	traceListCmd.Flags().StringVar(&traceFromFlag, "from", "", "Filter traces from this date (YYYY-MM-DD)")

	traceShowCmd.Flags().BoolVar(&traceSpansFlag, "spans", false, "Show span details")

	traceExportCmd.Flags().StringVarP(&traceFormatFlag, "format", "f", "json", "Export format (json, yaml)")
	traceExportCmd.Flags().StringVarP(&traceOutputFlag, "output", "o", "", "Output file path")

	tracePruneCmd.Flags().IntVar(&traceOlderThanFlag, "older-than", 30, "Delete traces older than N days")

	traceCmd.AddCommand(traceListCmd)
	traceCmd.AddCommand(traceShowCmd)
	traceCmd.AddCommand(traceExportCmd)
	traceCmd.AddCommand(traceStatsCmd)
	traceCmd.AddCommand(tracePruneCmd)
}

var traceCmd = &cobra.Command{
	Use:   "trace",
	Short: "View and manage telemetry traces",
	Long:  "Access agent telemetry data for debugging and analysis",
}

var traceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List telemetry traces",
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := openTelemetryDB()
		if err != nil {
			return err
		}
		defer db.Close()

		since := time.Now().AddDate(0, 0, -30)
		if traceFromFlag != "" {
			since, err = time.Parse("2006-01-02", traceFromFlag)
			if err != nil {
				return fmt.Errorf("invalid date format: %v", err)
			}
		}

		events, err := db.QueryEvents(since)
		if err != nil {
			return fmt.Errorf("failed to query events: %w", err)
		}

		if len(events) == 0 {
			fmt.Println("No traces found")
			return nil
		}

		if traceLimitFlag > 0 && len(events) > traceLimitFlag {
			events = events[:traceLimitFlag]
		}

		if jsonOutput {
			printJSON(events)
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "ID\tTIMESTAMP\tTYPE\tSESSION\tCOMMAND\tDURATION\tSUCCESS\n")
		for _, e := range events {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%t\n",
				e.ID[:16],
				e.Timestamp.Format("2006-01-02 15:04"),
				e.EventType,
				e.SessionID[:8],
				e.Command,
				e.Duration.Round(time.Millisecond),
				e.Success,
			)
		}
		w.Flush()

		return nil
	},
}

var traceShowCmd = &cobra.Command{
	Use:   "show <trace_id>",
	Short: "Show trace details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		traceID := args[0]

		db, err := openTelemetryDB()
		if err != nil {
			return err
		}
		defer db.Close()

		events, err := db.QueryEvents(time.Now().AddDate(0, 0, -90))
		if err != nil {
			return fmt.Errorf("failed to query events: %w", err)
		}

		var matchingEvent *telemetry.Event
		var sessionEvents []telemetry.Event
		for i := range events {
			if events[i].ID == traceID || events[i].ID[:16] == traceID[:16] {
				matchingEvent = &events[i]
				break
			}
		}

		if matchingEvent == nil {
			return fmt.Errorf("trace not found: %s", traceID)
		}

		for _, e := range events {
			if e.SessionID == matchingEvent.SessionID {
				sessionEvents = append(sessionEvents, e)
			}
		}

		if jsonOutput {
			data := map[string]interface{}{
				"trace": matchingEvent,
			}
			if traceSpansFlag && len(sessionEvents) > 0 {
				data["session_events"] = sessionEvents
			}
			printJSON(data)
			return nil
		}

		fmt.Printf("Trace ID: %s\n", matchingEvent.ID)
		fmt.Printf("Timestamp: %s\n", matchingEvent.Timestamp.Format(time.RFC3339))
		fmt.Printf("Type: %s\n", matchingEvent.EventType)
		fmt.Printf("Session: %s\n", matchingEvent.SessionID)
		fmt.Printf("Command: %s\n", matchingEvent.Command)
		fmt.Printf("Duration: %s\n", matchingEvent.Duration.Round(time.Millisecond))
		fmt.Printf("Success: %t\n", matchingEvent.Success)

		if matchingEvent.ErrorType != "" {
			fmt.Printf("Error Type: %s\n", matchingEvent.ErrorType)
		}
		if len(matchingEvent.Args) > 0 {
			fmt.Printf("Args: %v\n", matchingEvent.Args)
		}
		if matchingEvent.TemplateUsed != "" {
			fmt.Printf("Template: %s\n", matchingEvent.TemplateUsed)
		}

		if traceSpansFlag && len(sessionEvents) > 0 {
			fmt.Printf("\nSession Events (%d total):\n", len(sessionEvents))
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "ID\tTIMESTAMP\tTYPE\tCOMMAND\tDURATION\n")
			for _, e := range sessionEvents {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					e.ID[:16],
					e.Timestamp.Format("15:04:05"),
					e.EventType,
					e.Command,
					e.Duration.Round(time.Millisecond),
				)
			}
			w.Flush()
		}

		return nil
	},
}

var traceExportCmd = &cobra.Command{
	Use:   "export <trace_id>",
	Short: "Export trace to file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		traceID := args[0]

		db, err := openTelemetryDB()
		if err != nil {
			return err
		}
		defer db.Close()

		events, err := db.QueryEvents(time.Now().AddDate(0, 0, -90))
		if err != nil {
			return fmt.Errorf("failed to query events: %w", err)
		}

		var matchingEvent *telemetry.Event
		for i := range events {
			if events[i].ID == traceID || events[i].ID[:16] == traceID[:16] {
				matchingEvent = &events[i]
				break
			}
		}

		if matchingEvent == nil {
			return fmt.Errorf("trace not found: %s", traceID)
		}

		var data []byte
		switch traceFormatFlag {
		case "yaml":
			data, err = yamlExport(matchingEvent)
			if err != nil {
				return fmt.Errorf("failed to export as yaml: %w", err)
			}
		case "json":
			fallthrough
		default:
			data, err = json.MarshalIndent(matchingEvent, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to export as json: %w", err)
			}
			data = append(data, '\n')
		}

		if traceOutputFlag != "" {
			err = os.WriteFile(traceOutputFlag, data, 0644)
			if err != nil {
				return fmt.Errorf("failed to write file: %w", err)
			}
			fmt.Printf("Exported to %s\n", traceOutputFlag)
		} else {
			fmt.Print(string(data))
		}

		return nil
	},
}

var traceStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show telemetry statistics",
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := openTelemetryDB()
		if err != nil {
			return err
		}
		defer db.Close()

		stats, err := db.GetCLIStats(30)
		if err != nil {
			return fmt.Errorf("failed to get stats: %w", err)
		}

		if jsonOutput {
			printJSON(stats)
			return nil
		}

		fmt.Println("Telemetry Statistics (Last 30 days)")
		fmt.Println("─────────────────────────────────")
		fmt.Printf("Total Events: %d\n", stats.TotalEvents)
		fmt.Printf("Total Sessions: %d\n", stats.TotalSessions)
		fmt.Printf("Total Commands: %d\n", stats.TotalCommands)
		fmt.Printf("Success Rate: %.1f%%\n", stats.SuccessRate*100)
		fmt.Printf("Avg Command Duration: %s\n", stats.AvgCommandDuration.Round(time.Millisecond))
		fmt.Printf("Workspaces Created: %d\n", stats.WorkspacesCreated)
		fmt.Printf("Tasks Completed: %d\n", stats.TasksCompleted)

		if len(stats.TopCommands) > 0 {
			fmt.Println("\nTop Commands:")
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "COMMAND\tCOUNT\tAVG DURATION\tSUCCESS RATE\n")
			for _, c := range stats.TopCommands[:5] {
				fmt.Fprintf(w, "%s\t%d\t%s\t%.1f%%\n",
					c.Command,
					c.Count,
					time.Duration(c.AvgDuration)*time.Millisecond,
					c.SuccessRate,
				)
			}
			w.Flush()
		}

		if len(stats.CommonErrors) > 0 {
			fmt.Println("\nCommon Errors:")
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "TYPE\tCOUNT\tFIRST SEEN\n")
			for _, e := range stats.CommonErrors[:5] {
				fmt.Fprintf(w, "%s\t%d\t%s\n",
					e.ErrorType,
					e.Count,
					e.FirstSeen.Format("2006-01-02"),
				)
			}
			w.Flush()
		}

		return nil
	},
}

var tracePruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Delete old traces",
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := openTelemetryDB()
		if err != nil {
			return err
		}
		defer db.Close()

		olderThan := time.Duration(traceOlderThanFlag) * 24 * time.Hour

		err = db.DeleteOldEvents(olderThan)
		if err != nil {
			return fmt.Errorf("failed to prune events: %w", err)
		}

		if jsonOutput {
			printJSON(map[string]string{
				"status":     "pruned",
				"older_than": fmt.Sprintf("%d days", traceOlderThanFlag),
			})
		} else {
			fmt.Printf("Pruned events older than %d days\n", traceOlderThanFlag)
		}

		return nil
	},
}

func openTelemetryDB() (*telemetry.TelemetryDB, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	dbPath := filepath.Join(homeDir, ".nexus", "telemetry.db")

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("telemetry database not found at %s", dbPath)
	}

	return telemetry.NewTelemetryDB(dbPath)
}

func yamlExport(event *telemetry.Event) ([]byte, error) {
	type yamlEvent struct {
		ID           string   `json:"id"`
		Timestamp    string   `json:"timestamp"`
		SessionID    string   `json:"session_id"`
		EventType    string   `json:"event_type"`
		Command      string   `json:"command,omitempty"`
		Args         []string `json:"args,omitempty"`
		Duration     string   `json:"duration"`
		Success      bool     `json:"success"`
		ErrorType    string   `json:"error_type,omitempty"`
		TemplateUsed string   `json:"template_used,omitempty"`
	}

	ye := yamlEvent{
		ID:           event.ID,
		Timestamp:    event.Timestamp.Format(time.RFC3339),
		SessionID:    event.SessionID,
		EventType:    event.EventType,
		Command:      event.Command,
		Args:         event.Args,
		Duration:     event.Duration.String(),
		Success:      event.Success,
		ErrorType:    event.ErrorType,
		TemplateUsed: event.TemplateUsed,
	}

	return json.MarshalIndent(ye, "", "  ")
}
