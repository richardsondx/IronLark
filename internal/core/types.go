package core

import "time"

type ApprovalMode string

const (
	ApprovalSuggest  ApprovalMode = "suggest"
	ApprovalConfirm  ApprovalMode = "confirm"
	ApprovalAutoSafe ApprovalMode = "auto-safe"
	ApprovalAgent    ApprovalMode = "agent"
)

func (m ApprovalMode) Valid() bool {
	switch m {
	case ApprovalSuggest, ApprovalConfirm, ApprovalAutoSafe, ApprovalAgent:
		return true
	default:
		return false
	}
}

type InteractionMode string

const (
	InteractionExecuteFirst InteractionMode = "execute-first"
	InteractionPlanFirst    InteractionMode = "plan-first"
)

func (m InteractionMode) Valid() bool {
	switch m {
	case InteractionExecuteFirst, InteractionPlanFirst:
		return true
	default:
		return false
	}
}

type ActionType string

const (
	ActionRunShell       ActionType = "run_shell"
	ActionReadFiles      ActionType = "read_files"
	ActionListDir        ActionType = "list_dir"
	ActionSearchFiles    ActionType = "search_files"
	ActionSemanticSearch ActionType = "semantic_search"
	ActionEditFile       ActionType = "edit_file"
	ActionWebSearch      ActionType = "web_search"
	ActionFetchRules     ActionType = "fetch_rules"
	ActionAskUser        ActionType = "ask_user"
	ActionInspect        ActionType = "inspect"
	ActionCheckpoint     ActionType = "checkpoint"
	ActionFinish         ActionType = "finish"

	// Backward-compatible aliases for older prompts and persisted sessions.
	ActionRun      = ActionRunShell
	ActionReadFile = ActionReadFiles
	ActionGrep     = ActionSearchFiles
	ActionPatch    = ActionEditFile
)

type Action struct {
	ID               string     `json:"id"`
	Type             ActionType `json:"type"`
	Title            string     `json:"title"`
	Reason           string     `json:"reason"`
	Command          string     `json:"command,omitempty"`
	Path             string     `json:"path,omitempty"`
	Paths            []string   `json:"paths,omitempty"`
	Query            string     `json:"query,omitempty"`
	Pattern          string     `json:"pattern,omitempty"`
	Glob             string     `json:"glob,omitempty"`
	PatchUnifiedDiff string     `json:"patch_unified_diff,omitempty"`
	CWD              string     `json:"cwd,omitempty"`
	TimeoutSec       int        `json:"timeout_sec,omitempty"`
}

type Verification struct {
	Type        ActionType `json:"type"`
	Command     string     `json:"command,omitempty"`
	Path        string     `json:"path,omitempty"`
	Paths       []string   `json:"paths,omitempty"`
	SuccessHint string     `json:"success_hint,omitempty"`
	TimeoutSec  int        `json:"timeout_sec,omitempty"`
}

type LLMResponse struct {
	Summary        string         `json:"summary"`
	Findings       []string       `json:"findings"`
	Actions        []Action       `json:"actions"`
	Verification   []Verification `json:"verification"`
	NeedsUserInput bool           `json:"needs_user_input"`
	Confidence     float64        `json:"confidence,omitempty"`
}

type RiskLevel string

const (
	RiskLow    RiskLevel = "LOW"
	RiskMedium RiskLevel = "MEDIUM"
	RiskHigh   RiskLevel = "HIGH"
)

type RiskReport struct {
	Level              RiskLevel `json:"level"`
	NeedsSudo          bool      `json:"needs_sudo"`
	TouchesSystemFiles bool      `json:"touches_system_files"`
	RollbackAvailable  bool      `json:"rollback_available"`
	Reason             string    `json:"reason"`
}

type ActionResult struct {
	Action       Action     `json:"action"`
	Risk         RiskReport `json:"risk"`
	Approved     bool       `json:"approved"`
	Skipped      bool       `json:"skipped"`
	Stdout       string     `json:"stdout,omitempty"`
	Stderr       string     `json:"stderr,omitempty"`
	Summary      string     `json:"summary,omitempty"`
	ExitCode     int        `json:"exit_code,omitempty"`
	DurationMS   int64      `json:"duration_ms,omitempty"`
	Error        string     `json:"error,omitempty"`
	PatchID      string     `json:"patch_id,omitempty"`
	BackupPath   string     `json:"backup_path,omitempty"`
	CheckpointID string     `json:"checkpoint_id,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   time.Time  `json:"finished_at"`
}

type ConversationMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
