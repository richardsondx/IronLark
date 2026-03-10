package ops

import (
	"time"

	"github.com/richardsondx/IronLark/internal/core"
)

type ProcessKind string

const (
	ProcessWatcher  ProcessKind = "watcher"
	ProcessRecovery ProcessKind = "recovery"
	ProcessShellRun ProcessKind = "shell_run"
	ProcessAgent    ProcessKind = "agent"
)

type ShellPromotionReason string

const (
	ShellPromotionLongExpected ShellPromotionReason = "long_expected"
	ShellPromotionTimedOut     ShellPromotionReason = "timed_out"
	ShellPromotionSignalKilled ShellPromotionReason = "signal_killed"
	ShellPromotionStalled      ShellPromotionReason = "stalled_output"
)

type EntityKind string

const (
	EntityService   EntityKind = "service"
	EntityContainer EntityKind = "container"
	EntityApp       EntityKind = "app"
)

type EntityManager string

const (
	ManagerSystemd EntityManager = "systemd"
	ManagerDocker  EntityManager = "docker"
	ManagerPort    EntityManager = "port"
)

type ProcessRecord struct {
	ID              string            `json:"id"`
	Kind            ProcessKind       `json:"kind"`
	PID             int               `json:"pid"`
	Host            string            `json:"host"`
	CWD             string            `json:"cwd"`
	Target          string            `json:"target"`
	State           string            `json:"state"`
	Provider        string            `json:"provider,omitempty"`
	Model           string            `json:"model,omitempty"`
	ApprovalMode    core.ApprovalMode `json:"approval_mode,omitempty"`
	RequestCount    int               `json:"request_count,omitempty"`
	TokenUsage      core.TokenUsage   `json:"token_usage,omitempty"`
	StartedAt       time.Time         `json:"started_at"`
	LastHeartbeatAt time.Time         `json:"last_heartbeat_at"`
	FinishedAt      time.Time         `json:"finished_at,omitempty"`
}

type ShellRunSpec struct {
	ID                 string               `json:"id"`
	Command            string               `json:"command"`
	CWD                string               `json:"cwd"`
	Shell              string               `json:"shell,omitempty"`
	TimeoutSec         int                  `json:"timeout_sec,omitempty"`
	StallWindowSec     int                  `json:"stall_window_sec,omitempty"`
	MaxRuntimeSec      int                  `json:"max_runtime_sec,omitempty"`
	PromotionReason    ShellPromotionReason `json:"promotion_reason,omitempty"`
	Host               string               `json:"host,omitempty"`
	ApprovalMode       core.ApprovalMode    `json:"approval_mode,omitempty"`
	Provider           string               `json:"provider,omitempty"`
	Model              string               `json:"model,omitempty"`
	CreatedAt          time.Time            `json:"created_at"`
}

type ShellRunState struct {
	State            string                `json:"state"`
	PID              int                   `json:"pid,omitempty"`
	PGID             int                   `json:"pgid,omitempty"`
	ExitCode         int                   `json:"exit_code,omitempty"`
	FailureKind      core.ShellFailureKind `json:"failure_kind,omitempty"`
	KilledBySignal   int                   `json:"killed_by_signal,omitempty"`
	AttemptCount     int                   `json:"attempt_count,omitempty"`
	LastSummary      string                `json:"last_summary,omitempty"`
	LastError        string                `json:"last_error,omitempty"`
	LastOutputAt     time.Time             `json:"last_output_at,omitempty"`
	StdoutPreview    string                `json:"stdout_preview,omitempty"`
	StderrPreview    string                `json:"stderr_preview,omitempty"`
	StdoutLogPath    string                `json:"stdout_log_path,omitempty"`
	StderrLogPath    string                `json:"stderr_log_path,omitempty"`
	StartedAt        time.Time             `json:"started_at"`
	UpdatedAt        time.Time             `json:"updated_at"`
	FinishedAt       time.Time             `json:"finished_at,omitempty"`
}

type LogicalEntity struct {
	ID              string        `json:"id"`
	Kind            EntityKind    `json:"kind"`
	Name            string        `json:"name"`
	DisplayName     string        `json:"display_name"`
	Manager         EntityManager `json:"manager"`
	Unit            string        `json:"unit,omitempty"`
	Container       string        `json:"container,omitempty"`
	Address         string        `json:"address,omitempty"`
	Port            int           `json:"port,omitempty"`
	HealthURL       string        `json:"health_url,omitempty"`
	ExpectedPorts   []int         `json:"expected_ports,omitempty"`
	ObserveOnly     bool          `json:"observe_only,omitempty"`
	Aliases         []string      `json:"aliases,omitempty"`
	DependencyIDs   []string      `json:"dependency_ids,omitempty"`
	ResolutionScore int           `json:"resolution_score,omitempty"`
}

type ProbeStatus struct {
	Type    string `json:"type"`
	Label   string `json:"label"`
	Healthy bool   `json:"healthy"`
	Detail  string `json:"detail,omitempty"`
}

type Baseline struct {
	CapturedAt     time.Time     `json:"captured_at"`
	Entity         LogicalEntity `json:"entity"`
	ExpectedPorts  []int         `json:"expected_ports,omitempty"`
	Manager        EntityManager `json:"manager"`
	HealthySignals []ProbeStatus `json:"healthy_signals,omitempty"`
}

type Watcher struct {
	ID                  string            `json:"id"`
	Query               string            `json:"query"`
	Host                string            `json:"host"`
	CWD                 string            `json:"cwd"`
	Entity              LogicalEntity     `json:"entity"`
	ObserveOnly         bool              `json:"observe_only"`
	State               string            `json:"state"`
	IntervalSec         int               `json:"interval_sec"`
	FailureThreshold    int               `json:"failure_threshold"`
	StabilityWindowSec  int               `json:"stability_window_sec"`
	CooldownSec         int               `json:"cooldown_sec"`
	RestartBudget       int               `json:"restart_budget"`
	ConsecutiveFailures int               `json:"consecutive_failures"`
	RestartAttempts     int               `json:"restart_attempts"`
	IncidentCount       int               `json:"incident_count"`
	LastSummary         string            `json:"last_summary,omitempty"`
	CurrentIncidentID   string            `json:"current_incident_id,omitempty"`
	Baseline            Baseline          `json:"baseline"`
	LastHealthyAt       time.Time         `json:"last_healthy_at,omitempty"`
	StableSince         time.Time         `json:"stable_since,omitempty"`
	CooldownUntil       time.Time         `json:"cooldown_until,omitempty"`
	ProcessID           string            `json:"process_id,omitempty"`
	Provider            string            `json:"provider,omitempty"`
	Model               string            `json:"model,omitempty"`
	ApprovalMode        core.ApprovalMode `json:"approval_mode,omitempty"`
	RequestCount        int               `json:"request_count,omitempty"`
	TokenUsage          core.TokenUsage   `json:"token_usage,omitempty"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
}

type RecoverySpec struct {
	ID                string            `json:"id"`
	Goal              string            `json:"goal"`
	Query             string            `json:"query"`
	Host              string            `json:"host"`
	CWD               string            `json:"cwd"`
	Entity            LogicalEntity     `json:"entity"`
	AllowedActions    []string          `json:"allowed_actions,omitempty"`
	RetryBudget       int               `json:"retry_budget"`
	StabilityWindowSec int              `json:"stability_window_sec"`
	ApprovalMode      core.ApprovalMode `json:"approval_mode,omitempty"`
	Provider          string            `json:"provider,omitempty"`
	Model             string            `json:"model,omitempty"`
	ThreadID          string            `json:"thread_id,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
}

type RecoveryState struct {
	Phase             string          `json:"phase"`
	Attempts          int             `json:"attempts"`
	CurrentHypothesis string          `json:"current_hypothesis,omitempty"`
	LastSummary       string          `json:"last_summary,omitempty"`
	LastError         string          `json:"last_error,omitempty"`
	BlockedReason     string          `json:"blocked_reason,omitempty"`
	StableSince       time.Time       `json:"stable_since,omitempty"`
	ProcessID         string          `json:"process_id,omitempty"`
	RequestCount      int             `json:"request_count,omitempty"`
	TokenUsage        core.TokenUsage `json:"token_usage,omitempty"`
	StartedAt         time.Time       `json:"started_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	FinishedAt        time.Time       `json:"finished_at,omitempty"`
}

type IncidentRecord struct {
	ID             string    `json:"id"`
	WatcherID      string    `json:"watcher_id,omitempty"`
	RecoveryID     string    `json:"recovery_id,omitempty"`
	EntityID       string    `json:"entity_id,omitempty"`
	Target         string    `json:"target"`
	Kind           string    `json:"kind"`
	Status         string    `json:"status"`
	Summary        string    `json:"summary"`
	Hypothesis     string    `json:"hypothesis,omitempty"`
	ObserveOnly    bool      `json:"observe_only,omitempty"`
	AutoRemediated bool      `json:"auto_remediated,omitempty"`
	EvidenceDir    string    `json:"evidence_dir,omitempty"`
	Commands       []string  `json:"commands,omitempty"`
	Notes          []string  `json:"notes,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	ResolvedAt     time.Time `json:"resolved_at,omitempty"`
}

type TimelineEvent struct {
	At      time.Time `json:"at"`
	Phase   string    `json:"phase,omitempty"`
	Type    string    `json:"type"`
	Summary string    `json:"summary"`
	Details []string  `json:"details,omitempty"`
}

type ProbeReport struct {
	Entity             LogicalEntity `json:"entity"`
	Healthy            bool          `json:"healthy"`
	Statuses           []ProbeStatus `json:"statuses,omitempty"`
	SelfDown           bool          `json:"self_down"`
	DependencyDown     bool          `json:"dependency_down"`
	DiskPressure       bool          `json:"disk_pressure"`
	OOMDetected        bool          `json:"oom_detected"`
	PortConflict       bool          `json:"port_conflict"`
	ConfigError        bool          `json:"config_error"`
	SuspectedUnknown   bool          `json:"suspected_unknown"`
	Summary            string        `json:"summary,omitempty"`
}

type Diagnosis struct {
	Hypothesis        string   `json:"hypothesis"`
	Summary           string   `json:"summary"`
	Reasons           []string `json:"reasons,omitempty"`
	RestartAllowed    bool     `json:"restart_allowed"`
	ResourceBlocker   bool     `json:"resource_blocker,omitempty"`
	ConfigBlocker     bool     `json:"config_blocker,omitempty"`
	DependencyBlocker bool     `json:"dependency_blocker,omitempty"`
}

type Summary struct {
	ActiveWatchers   int `json:"active_watchers"`
	ActiveRecoveries int `json:"active_recoveries"`
	ActiveShellRuns  int `json:"active_shell_runs"`
	OpenIncidents    int `json:"open_incidents"`
}

type QueryResult struct {
	Watchers   []Watcher        `json:"watchers,omitempty"`
	Recoveries []RecoveryStatus `json:"recoveries,omitempty"`
	ShellRuns  []ShellRunStatus `json:"shell_runs,omitempty"`
	Incidents  []IncidentRecord `json:"incidents,omitempty"`
}

type ShellRunStatus struct {
	Spec  ShellRunSpec  `json:"spec"`
	State ShellRunState `json:"state"`
}

type RecoveryStatus struct {
	Spec  RecoverySpec  `json:"spec"`
	State RecoveryState `json:"state"`
}
