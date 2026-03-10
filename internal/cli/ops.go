package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/richardsondx/IronLark/internal/agent"
	"github.com/richardsondx/IronLark/internal/app"
	"github.com/richardsondx/IronLark/internal/core"
	"github.com/richardsondx/IronLark/internal/ops"
)

type psEntry struct {
	ID              string    `json:"id"`
	Kind            string    `json:"kind"`
	State           string    `json:"state"`
	Health          string    `json:"health,omitempty"`
	PID             int       `json:"pid"`
	Target          string    `json:"target"`
	Host            string    `json:"host"`
	CWD             string    `json:"cwd"`
	Age             string    `json:"age"`
	LastActivity    string    `json:"last_activity"`
	RequestCount    int       `json:"request_count,omitempty"`
	TokenUsage      string    `json:"token_usage,omitempty"`
	ApprovalMode    string    `json:"approval_mode,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	LastHeartbeatAt time.Time `json:"last_heartbeat_at"`
}

func newPSCommand(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ps",
		Short: "List and control live IronLark processes",
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			entries, err := listProcessEntries(cmd.Context(), application)
			if err != nil {
				return err
			}
			if application.Runtime.JSONOutput {
				return application.Renderer.MessageJSON(entries)
			}
			for _, line := range renderPSEntries(entries) {
				application.Renderer.Message(line)
			}
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "stop <id|pid>",
		Short: "Gracefully stop a watcher, recovery run, or agent session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return stopOrKillProcess(cmd.Context(), flags, args[0], syscall.SIGTERM)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "kill <id|pid>",
		Short: "Force kill a watcher, recovery run, or agent session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return stopOrKillProcess(cmd.Context(), flags, args[0], syscall.SIGKILL)
		},
	})
	return cmd
}

func newWatchCommand(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "watch <query>",
		Short:        "Start or inspect a background watcher",
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return runWatchList(cmd.Context(), flags)
			}
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			watcher, pid, err := application.Ops.StartWatcher(cmd.Context(), buildOpsDeps(application), strings.Join(args, " "), executable)
			if err != nil {
				return err
			}
			application.Renderer.Message(fmt.Sprintf("Started watcher %s for %s (pid %d)", watcher.ID, watcher.Entity.DisplayName, pid))
			if watcher.ObserveOnly {
				application.Renderer.Message("Watcher is in observe-only mode because no strong auto-fix probe was inferred.")
			}
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List watchers",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWatchList(cmd.Context(), flags)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "status [id|target]",
		Short: "Show watcher status",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				return runWatchList(cmd.Context(), flags)
			}
			watcher, err := resolveWatcher(application.Ops, args[0])
			if err != nil {
				return err
			}
			if application.Runtime.JSONOutput {
				return application.Renderer.MessageJSON(watcher)
			}
			application.Renderer.Message(fmt.Sprintf("Watcher: %s", watcher.ID))
			application.Renderer.Message(fmt.Sprintf("Target: %s", watcher.Entity.DisplayName))
			application.Renderer.Message(fmt.Sprintf("State: %s", watcher.State))
			application.Renderer.Message(fmt.Sprintf("Observe only: %t", watcher.ObserveOnly))
			application.Renderer.Message(fmt.Sprintf("Summary: %s", watcher.LastSummary))
			application.Renderer.Message(fmt.Sprintf("Failures: %d  Restarts: %d  Incidents: %d", watcher.ConsecutiveFailures, watcher.RestartAttempts, watcher.IncidentCount))
			if !watcher.CooldownUntil.IsZero() {
				application.Renderer.Message(fmt.Sprintf("Cooldown until: %s", watcher.CooldownUntil.Format(time.RFC3339)))
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "report <id|target>",
		Short: "Show watcher report with incidents",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			watcher, err := resolveWatcher(application.Ops, args[0])
			if err != nil {
				return err
			}
			incidents, err := incidentsForWatcher(application.Ops, watcher.ID)
			if err != nil {
				return err
			}
			report := map[string]any{
				"watcher":   watcher,
				"incidents": incidents,
			}
			if application.Runtime.JSONOutput {
				return application.Renderer.MessageJSON(report)
			}
			application.Renderer.Message(fmt.Sprintf("Watcher %s", watcher.ID))
			application.Renderer.Message(fmt.Sprintf("Target: %s  state=%s", watcher.Entity.DisplayName, watcher.State))
			application.Renderer.Message(fmt.Sprintf("Summary: %s", watcher.LastSummary))
			for _, incident := range incidents {
				application.Renderer.Message(fmt.Sprintf("- %s  %s  %s", incident.UpdatedAt.Format("2006-01-02 15:04:05"), incident.Status, incident.Summary))
				if incident.EvidenceDir != "" {
					application.Renderer.Message("  evidence: " + incident.EvidenceDir)
				}
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "stop <id>",
		Short: "Stop a watcher",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return stopOrKillProcess(cmd.Context(), flags, args[0], syscall.SIGTERM)
		},
	})
	cmd.AddCommand(newWatchRunnerCommand(flags))
	return cmd
}

func newRecoverCommand(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "recover <goal>",
		Short:        "Start or inspect a background recovery run",
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return runRecoveryList(cmd.Context(), flags)
			}
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			spec, pid, err := application.Ops.StartRecovery(cmd.Context(), buildOpsDeps(application), strings.Join(args, " "), executable)
			if err != nil {
				return err
			}
			application.Renderer.Message(fmt.Sprintf("Started recovery %s for %s (pid %d)", spec.ID, spec.Entity.DisplayName, pid))
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List recovery runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRecoveryList(cmd.Context(), flags)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "status <id>",
		Short: "Show recovery status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			status, err := application.Ops.LoadRecoveryStatus(args[0])
			if err != nil {
				return err
			}
			if status.Spec.ID == "" {
				return fmt.Errorf("recovery %q was not found", args[0])
			}
			if application.Runtime.JSONOutput {
				return application.Renderer.MessageJSON(status)
			}
			application.Renderer.Message(fmt.Sprintf("Recovery: %s", status.Spec.ID))
			application.Renderer.Message(fmt.Sprintf("Goal: %s", status.Spec.Goal))
			application.Renderer.Message(fmt.Sprintf("Target: %s", status.Spec.Entity.DisplayName))
			application.Renderer.Message(fmt.Sprintf("Phase: %s", status.State.Phase))
			application.Renderer.Message(fmt.Sprintf("Attempts: %d/%d", status.State.Attempts, status.Spec.RetryBudget))
			application.Renderer.Message(fmt.Sprintf("Summary: %s", status.State.LastSummary))
			if status.State.BlockedReason != "" {
				application.Renderer.Message(fmt.Sprintf("Blocked: %s", status.State.BlockedReason))
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "report <id>",
		Short: "Show recovery report with progress and timeline",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			status, err := application.Ops.LoadRecoveryStatus(args[0])
			if err != nil {
				return err
			}
			if status.Spec.ID == "" {
				return fmt.Errorf("recovery %q was not found", args[0])
			}
			progress, err := application.Ops.ReadRecoveryProgress(args[0])
			if err != nil {
				return err
			}
			timeline, err := application.Ops.LoadRecoveryTimeline(args[0])
			if err != nil {
				return err
			}
			report := map[string]any{
				"status":   status,
				"progress": progress,
				"timeline": timeline,
			}
			if application.Runtime.JSONOutput {
				return application.Renderer.MessageJSON(report)
			}
			application.Renderer.Message(fmt.Sprintf("Recovery %s", status.Spec.ID))
			application.Renderer.Message(fmt.Sprintf("Goal: %s", status.Spec.Goal))
			application.Renderer.Message(fmt.Sprintf("Phase: %s", status.State.Phase))
			application.Renderer.Message(progress)
			for _, event := range timeline {
				application.Renderer.Message(fmt.Sprintf("- %s  %s  %s", event.At.Format("2006-01-02 15:04:05"), event.Phase, event.Summary))
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "stop <id>",
		Short: "Stop a recovery run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return stopOrKillProcess(cmd.Context(), flags, args[0], syscall.SIGTERM)
		},
	})
	cmd.AddCommand(newRecoverRunnerCommand(flags))
	return cmd
}

func newWatchRunnerCommand(flags *rootFlags) *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:          "__run",
		Short:        "Internal watcher runner",
		Hidden:       true,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			return application.Ops.RunWatcher(cmd.Context(), buildOpsDeps(application), id)
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "watcher id")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

func newRecoverRunnerCommand(flags *rootFlags) *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:          "__run",
		Short:        "Internal recovery runner",
		Hidden:       true,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			return application.Ops.RunRecovery(cmd.Context(), buildOpsDeps(application), id)
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "recovery id")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

func buildOpsDeps(application *app.App) ops.RuntimeDeps {
	host := ""
	if application.Graph != nil {
		host = application.Graph.HostKey()
	}
	if host == "" {
		userName, rawHost := agent.CurrentIdentity()
		host = userName + "@" + rawHost
	}
	return ops.RuntimeDeps{
		Runtime:    application.Runtime,
		Graph:      application.Graph,
		Executor:   application.Executor,
		Policy:     application.PolicyStore,
		Host:       host,
		WorkingDir: application.Runtime.WorkingDir,
	}
}

func runWatchList(ctx context.Context, flags *rootFlags) error {
	_ = ctx
	application, err := buildApp(flags)
	if err != nil {
		return err
	}
	watchers, err := application.Ops.ListWatchers()
	if err != nil {
		return err
	}
	if application.Runtime.JSONOutput {
		return application.Renderer.MessageJSON(watchers)
	}
	for _, watcher := range watchers {
		application.Renderer.Message(fmt.Sprintf("%s  %-11s  %-7t  %s", watcher.ID, watcher.State, watcher.ObserveOnly, watcher.Entity.DisplayName))
	}
	return nil
}

func runRecoveryList(ctx context.Context, flags *rootFlags) error {
	_ = ctx
	application, err := buildApp(flags)
	if err != nil {
		return err
	}
	recoveries, err := application.Ops.ListRecoveries()
	if err != nil {
		return err
	}
	if application.Runtime.JSONOutput {
		return application.Renderer.MessageJSON(recoveries)
	}
	for _, recovery := range recoveries {
		application.Renderer.Message(fmt.Sprintf("%s  %-10s  attempts=%d  %s", recovery.Spec.ID, recovery.State.Phase, recovery.State.Attempts, recovery.Spec.Entity.DisplayName))
	}
	return nil
}

func stopOrKillProcess(ctx context.Context, flags *rootFlags, id string, sig syscall.Signal) error {
	application, err := buildApp(flags)
	if err != nil {
		return err
	}
	records, err := application.Ops.ListProcesses()
	if err != nil {
		return err
	}
	record, err := ops.StopByIDOrPID(records, id, sig)
	if err == nil {
		if record.Kind == ops.ProcessShellRun {
			status, loadErr := application.Ops.LoadShellRunStatus(record.ID)
			if loadErr == nil && status.Spec.ID != "" {
				if sig == syscall.SIGKILL {
					status.State.State = "failed"
					status.State.LastSummary = "shell run killed"
				} else {
					status.State.State = "stopped"
					status.State.LastSummary = "shell run stopped"
				}
				status.State.FinishedAt = time.Now().UTC()
				_ = application.Ops.SaveShellRunState(record.ID, status.State)
			}
		}
		action := "Stopped"
		if sig == syscall.SIGKILL {
			action = "Killed"
		}
		application.Renderer.Message(fmt.Sprintf("%s %s %s (pid %d)", action, record.Kind, record.ID, record.PID))
		return nil
	}

	workspaces, listErr := application.Agents.List()
	if listErr != nil {
		return listErr
	}
	manager := agent.SessionManager{Store: application.Agents}
	for _, workspace := range workspaces {
		refreshed, _ := manager.Inspect(ctx, workspace)
		match := refreshed.Key == id
		if !match {
			if pid, convErr := strconv.Atoi(id); convErr == nil {
				match = refreshed.RunnerPID == pid
			}
		}
		if !match {
			continue
		}
		if sig == syscall.SIGTERM {
			if stopErr := manager.Stop(ctx, refreshed); stopErr != nil {
				return stopErr
			}
			refreshed.State = agent.StateStopped
			_ = application.Agents.Save(refreshed)
			application.Renderer.Message(fmt.Sprintf("Stopped agent %s", refreshed.Key))
			return nil
		}
		if refreshed.RunnerPID <= 0 {
			return fmt.Errorf("agent %s does not have a live pid", refreshed.Key)
		}
		if killErr := syscall.Kill(refreshed.RunnerPID, syscall.SIGKILL); killErr != nil {
			return killErr
		}
		refreshed.State = agent.StateStopped
		_ = application.Agents.Save(refreshed)
		application.Renderer.Message(fmt.Sprintf("Killed agent %s", refreshed.Key))
		return nil
	}
	return err
}

func listProcessEntries(ctx context.Context, application *app.App) ([]psEntry, error) {
	processes, err := application.Ops.ListProcesses()
	if err != nil {
		return nil, err
	}
	entries := make([]psEntry, 0, len(processes))
	now := time.Now().UTC()
	for _, process := range processes {
		state := process.State
		if process.PID > 0 && !processAlive(process.PID) && state != "done" && state != "stopped" && state != "failed" {
			state = "dead"
		}
		entry := psEntry{
			ID:              process.ID,
			Kind:            string(process.Kind),
			State:           state,
			PID:             process.PID,
			Target:          process.Target,
			Host:            process.Host,
			CWD:             process.CWD,
			Age:             humanDuration(now.Sub(process.StartedAt)),
			LastActivity:    humanDuration(now.Sub(process.LastHeartbeatAt)) + " ago",
			RequestCount:    process.RequestCount,
			TokenUsage:      formatUsage(process.TokenUsage),
			ApprovalMode:    string(process.ApprovalMode),
			StartedAt:       process.StartedAt,
			LastHeartbeatAt: process.LastHeartbeatAt,
		}
		entries = append(entries, annotatePSEntry(entry, now))
	}
	workspaces, err := application.Agents.List()
	if err != nil {
		return nil, err
	}
	manager := agent.SessionManager{Store: application.Agents}
	for _, workspace := range workspaces {
		refreshed, _ := manager.Inspect(ctx, workspace)
		entry := psEntry{
			ID:              refreshed.Key,
			Kind:            "agent",
			State:           refreshed.State,
			PID:             refreshed.RunnerPID,
			Target:          refreshed.CWD,
			Host:            refreshed.Host,
			CWD:             refreshed.CWD,
			Age:             humanDuration(now.Sub(refreshed.CreatedAt)),
			LastActivity:    humanDuration(now.Sub(refreshed.LastActiveAt)) + " ago",
			TokenUsage:      "n/a",
			ApprovalMode:    string(application.Runtime.ApprovalMode),
			StartedAt:       refreshed.CreatedAt,
			LastHeartbeatAt: refreshed.LastActiveAt,
		}
		entries = append(entries, annotatePSEntry(entry, now))
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].LastHeartbeatAt.After(entries[j].LastHeartbeatAt)
	})
	return entries, nil
}

func annotatePSEntry(entry psEntry, now time.Time) psEntry {
	entry.Health = psHealth(entry, now)
	return entry
}

func psHealth(entry psEntry, now time.Time) string {
	idle := now.Sub(entry.LastHeartbeatAt)
	switch {
	case entry.State == "dead":
		return "exited"
	case entry.Kind == string(ops.ProcessAgent) && entry.State == agent.StateStale:
		return "orphaned"
	case entry.Kind == string(ops.ProcessAgent) && entry.State == agent.StateStarting:
		if idle > 15*time.Second {
			return "stuck?"
		}
		return "starting"
	case isTerminalPSState(entry.State):
		return "-"
	}
	if threshold, ok := psStuckThreshold(entry.Kind); ok && idle > threshold {
		return "stuck?"
	}
	if entry.Kind == string(ops.ProcessAgent) && idle > 30*time.Minute {
		return "idle"
	}
	return "ok"
}

func isTerminalPSState(state string) bool {
	switch state {
	case "done", "stopped", "failed":
		return true
	default:
		return false
	}
}

func psStuckThreshold(kind string) (time.Duration, bool) {
	switch kind {
	case string(ops.ProcessWatcher):
		return 90 * time.Second, true
	case string(ops.ProcessRecovery):
		return 5 * time.Minute, true
	default:
		return 0, false
	}
}

func renderPSEntries(entries []psEntry) []string {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tKIND\tSTATE\tHEALTH\tPID\tAGE\tLAST\tTOKENS\tTARGET")
	for _, entry := range entries {
		tokens := entry.TokenUsage
		if tokens == "" {
			tokens = "0"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			entry.ID,
			entry.Kind,
			entry.State,
			entry.Health,
			formatPID(entry.PID),
			entry.Age,
			entry.LastActivity,
			tokens,
			entry.Target,
		)
	}
	_ = tw.Flush()
	return strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
}

func formatPID(pid int) string {
	if pid <= 0 {
		return "-"
	}
	return strconv.Itoa(pid)
}

func resolveWatcher(manager *ops.Manager, value string) (ops.Watcher, error) {
	watcher, err := manager.LoadWatcher(value)
	if err != nil {
		return ops.Watcher{}, err
	}
	if watcher.ID != "" {
		return watcher, nil
	}
	watchers, err := manager.ListWatchers()
	if err != nil {
		return ops.Watcher{}, err
	}
	value = strings.ToLower(strings.TrimSpace(value))
	for _, item := range watchers {
		if strings.Contains(strings.ToLower(item.Entity.DisplayName), value) || strings.Contains(strings.ToLower(item.Query), value) {
			return item, nil
		}
	}
	return ops.Watcher{}, fmt.Errorf("watcher %q was not found", value)
}

func incidentsForWatcher(manager *ops.Manager, watcherID string) ([]ops.IncidentRecord, error) {
	incidents, err := manager.ListIncidents()
	if err != nil {
		return nil, err
	}
	filtered := make([]ops.IncidentRecord, 0, len(incidents))
	for _, incident := range incidents {
		if incident.WatcherID == watcherID {
			filtered = append(filtered, incident)
		}
	}
	return filtered, nil
}

func formatUsage(usage core.TokenUsage) string {
	if usage.Empty() {
		return "0"
	}
	label := strconv.Itoa(usage.TotalTokens)
	if usage.Estimated {
		label += "~"
	}
	return label
}

func humanDuration(value time.Duration) string {
	if value < 0 {
		value = 0
	}
	switch {
	case value < time.Minute:
		return fmt.Sprintf("%ds", int(value.Seconds()))
	case value < time.Hour:
		return fmt.Sprintf("%dm", int(value.Minutes()))
	case value < 24*time.Hour:
		return fmt.Sprintf("%dh", int(value.Hours()))
	default:
		return fmt.Sprintf("%dd", int(value.Hours()/24))
	}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
