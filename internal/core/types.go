package core

import (
	"strings"
	"time"
)

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

type ApprovalDecisionKind string

const (
	ApprovalDecisionAllowOnce   ApprovalDecisionKind = "allow_once"
	ApprovalDecisionAllowAlways ApprovalDecisionKind = "allow_always"
	ApprovalDecisionAutoAccept  ApprovalDecisionKind = "auto_accept"
	ApprovalDecisionDenyOnce    ApprovalDecisionKind = "deny_once"
	ApprovalDecisionCancel      ApprovalDecisionKind = "cancel"
)

type ApprovalDecision struct {
	Kind              ApprovalDecisionKind `json:"kind"`
	AutoAcceptThrough RiskLevel            `json:"auto_accept_through,omitempty"`
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
	ActionWriteFile      ActionType = "write_file"
	ActionWebSearch      ActionType = "web_search"
	ActionFetchRules     ActionType = "fetch_rules"
	ActionFetchOps       ActionType = "fetch_ops"
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

func (t ActionType) Valid() bool {
	switch t {
	case ActionRunShell, ActionReadFiles, ActionListDir, ActionSearchFiles, ActionSemanticSearch, ActionEditFile,
		ActionWriteFile, ActionWebSearch, ActionFetchRules, ActionFetchOps, ActionAskUser, ActionInspect,
		ActionCheckpoint, ActionFinish:
		return true
	default:
		return false
	}
}

func NormalizeActionType(raw ActionType) (ActionType, bool) {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "", false
	}
	candidates := []ActionType{
		ActionSemanticSearch,
		ActionSearchFiles,
		ActionReadFiles,
		ActionRunShell,
		ActionListDir,
		ActionEditFile,
		ActionWriteFile,
		ActionWebSearch,
		ActionFetchRules,
		ActionFetchOps,
		ActionAskUser,
		ActionInspect,
		ActionCheckpoint,
		ActionFinish,
		ActionRun,
		ActionReadFile,
		ActionGrep,
		ActionPatch,
	}
	lower := strings.ToLower(text)
	for _, candidate := range candidates {
		target := string(candidate)
		if lower == target {
			return canonicalActionType(candidate), true
		}
		if strings.HasPrefix(lower, target) {
			remainder := lower[len(target):]
			if remainder == "" || strings.ContainsAny(remainder, "\",}] \n\r\t:") {
				return canonicalActionType(candidate), true
			}
		}
	}
	return ActionType(text), false
}

func canonicalActionType(actionType ActionType) ActionType {
	switch actionType {
	case ActionRun:
		return ActionRunShell
	case ActionReadFile:
		return ActionReadFiles
	case ActionGrep:
		return ActionSearchFiles
	case ActionPatch:
		return ActionEditFile
	default:
		return actionType
	}
}

type InputKind string

const (
	InputText       InputKind = "text"
	InputSecret     InputKind = "secret"
	InputConfirm    InputKind = "confirm"
	InputManualWait InputKind = "manual_wait"
)

type InputAlternative string

const (
	InputAlternativeSubmit   InputAlternative = "submit"
	InputAlternativeSkip     InputAlternative = "skip"
	InputAlternativeFollowUp InputAlternative = "follow_up"
)

type InputResponseMode string

const (
	InputResponseSubmitted InputResponseMode = "submitted"
	InputResponseSkipped   InputResponseMode = "skipped"
	InputResponseFollowUp  InputResponseMode = "follow_up"
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
	Content          string     `json:"content,omitempty"`
	FileMode         string     `json:"file_mode,omitempty"`
	CWD              string     `json:"cwd,omitempty"`
	TimeoutSec       int        `json:"timeout_sec,omitempty"`
	Detach           bool       `json:"detach,omitempty"`
	InputKind        InputKind  `json:"input_kind,omitempty"`
	FieldKey         string     `json:"field_key,omitempty"`
	Prompt           string     `json:"prompt,omitempty"`
	Clarification    string     `json:"clarification,omitempty"`
	Placeholder      string     `json:"placeholder,omitempty"`
	DestinationHint  string     `json:"destination_hint,omitempty"`
	ExpectsValue     bool       `json:"expects_value,omitempty"`
	Alternatives     []string   `json:"alternatives,omitempty"`
	OutputContent    string     `json:"output_content,omitempty"`
}

type Narration struct {
	TurnIntent  string                `json:"turn_intent,omitempty"`
	ActionHints []NarrationActionHint `json:"action_hints,omitempty"`
}

type NarrationActionHint struct {
	ActionID string `json:"action_id"`
	Text     string `json:"text"`
}

type NarrativeKind string

const (
	NarrativeTurnStarted       NarrativeKind = "turn_started"
	NarrativeIntent            NarrativeKind = "intent"
	NarrativeContextShift      NarrativeKind = "context_shift"
	NarrativeActionGroup       NarrativeKind = "action_group"
	NarrativeActionStarted     NarrativeKind = "action_started"
	NarrativeActionFinished    NarrativeKind = "action_finished"
	NarrativeVerificationStart NarrativeKind = "verification_started"
	NarrativeBlocked           NarrativeKind = "blocked"
	NarrativeTurnFinished      NarrativeKind = "turn_finished"
)

type NarrativeStatus string

const (
	NarrativePending NarrativeStatus = "pending"
	NarrativeRunning NarrativeStatus = "running"
	NarrativeDone    NarrativeStatus = "done"
	NarrativeError   NarrativeStatus = "error"
	NarrativeSkipped NarrativeStatus = "skipped"
)

type NarrativeEvent struct {
	ID        string          `json:"id,omitempty"`
	Kind      NarrativeKind   `json:"kind"`
	Phase     string          `json:"phase,omitempty"`
	Text      string          `json:"text"`
	ActionID  string          `json:"action_id,omitempty"`
	Target    string          `json:"target,omitempty"`
	Status    NarrativeStatus `json:"status,omitempty"`
	Details   []string        `json:"details,omitempty"`
	Timestamp time.Time       `json:"timestamp,omitempty"`
}

type ActionOutputStream string

const (
	ActionOutputStdout ActionOutputStream = "stdout"
	ActionOutputStderr ActionOutputStream = "stderr"
)

type ActionOutputChunk struct {
	ActionID string             `json:"action_id,omitempty"`
	Stream   ActionOutputStream `json:"stream"`
	Text     string             `json:"text"`
}

type ShellFailureKind string

const (
	ShellFailureNone         ShellFailureKind = ""
	ShellFailureTimeout      ShellFailureKind = "timeout"
	ShellFailureSignalKilled ShellFailureKind = "signal_killed"
	ShellFailureNonZeroExit  ShellFailureKind = "nonzero_exit"
	ShellFailureStartup      ShellFailureKind = "startup_error"
	ShellFailureStream       ShellFailureKind = "stream_error"
	ShellFailureStalled      ShellFailureKind = "stalled_output"
	ShellFailureUnknown      ShellFailureKind = "unknown"
)

type Verification struct {
	Type        ActionType `json:"type"`
	Command     string     `json:"command,omitempty"`
	Path        string     `json:"path,omitempty"`
	Paths       []string   `json:"paths,omitempty"`
	SuccessHint string     `json:"success_hint,omitempty"`
	TimeoutSec  int        `json:"timeout_sec,omitempty"`
}

type TokenUsage struct {
	PromptTokens     int  `json:"prompt_tokens,omitempty"`
	CompletionTokens int  `json:"completion_tokens,omitempty"`
	TotalTokens      int  `json:"total_tokens,omitempty"`
	Estimated        bool `json:"estimated,omitempty"`
}

func (u TokenUsage) Empty() bool {
	return u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0
}

func (u TokenUsage) Add(other TokenUsage) TokenUsage {
	out := TokenUsage{
		PromptTokens:     u.PromptTokens + other.PromptTokens,
		CompletionTokens: u.CompletionTokens + other.CompletionTokens,
		TotalTokens:      u.TotalTokens + other.TotalTokens,
		Estimated:        u.Estimated || other.Estimated,
	}
	if out.TotalTokens == 0 {
		out.TotalTokens = out.PromptTokens + out.CompletionTokens
	}
	return out
}

type LLMResponse struct {
	Summary        string         `json:"summary"`
	Findings       []string       `json:"findings"`
	Actions        []Action       `json:"actions"`
	Verification   []Verification `json:"verification"`
	Narration      *Narration     `json:"narration,omitempty"`
	NeedsUserInput bool           `json:"needs_user_input"`
	Confidence     float64        `json:"confidence,omitempty"`
	Usage          TokenUsage     `json:"usage,omitempty"`
}

type CompletionStatus string

const (
	CompletionFinished             CompletionStatus = "finished"
	CompletionIncompleteMaxTurns   CompletionStatus = "incomplete_max_turns"
	CompletionIncompleteNoProgress CompletionStatus = "incomplete_no_progress"
	CompletionBlockedUserInput     CompletionStatus = "blocked_user_input"
	CompletionFailed               CompletionStatus = "failed"
	CompletionBackgroundContinuing CompletionStatus = "background_continuing"
)

type RiskLevel string

const (
	RiskLow    RiskLevel = "LOW"
	RiskMedium RiskLevel = "MEDIUM"
	RiskHigh   RiskLevel = "HIGH"
)

func (l RiskLevel) Valid() bool {
	switch l {
	case RiskLow, RiskMedium, RiskHigh:
		return true
	default:
		return false
	}
}

func (l RiskLevel) Rank() int {
	switch l {
	case RiskLow:
		return 1
	case RiskMedium:
		return 2
	case RiskHigh:
		return 3
	default:
		return 0
	}
}

func (l RiskLevel) Covers(other RiskLevel) bool {
	return l.Valid() && other.Valid() && l.Rank() >= other.Rank()
}

type RiskReport struct {
	Level              RiskLevel `json:"level"`
	NeedsSudo          bool      `json:"needs_sudo"`
	TouchesSystemFiles bool      `json:"touches_system_files"`
	RollbackAvailable  bool      `json:"rollback_available"`
	Reason             string    `json:"reason"`
}

type ActionResult struct {
	Action                Action            `json:"action"`
	Risk                  RiskReport        `json:"risk"`
	Approved              bool              `json:"approved"`
	Skipped               bool              `json:"skipped"`
	Stdout                string            `json:"stdout,omitempty"`
	Stderr                string            `json:"stderr,omitempty"`
	Summary               string            `json:"summary,omitempty"`
	ExitCode              int               `json:"exit_code,omitempty"`
	DurationMS            int64             `json:"duration_ms,omitempty"`
	Error                 string            `json:"error,omitempty"`
	FailureKind           ShellFailureKind  `json:"failure_kind,omitempty"`
	TimedOut              bool              `json:"timed_out,omitempty"`
	KilledBySignal        int               `json:"killed_by_signal,omitempty"`
	Retryable             bool              `json:"retryable,omitempty"`
	BackgroundRecommended bool              `json:"background_recommended,omitempty"`
	BackgroundRunID       string            `json:"background_run_id,omitempty"`
	TaskID                string            `json:"task_id,omitempty"`
	Handler               string            `json:"handler,omitempty"`
	PatchID               string            `json:"patch_id,omitempty"`
	BackupPath            string            `json:"backup_path,omitempty"`
	CheckpointID          string            `json:"checkpoint_id,omitempty"`
	InputKind             InputKind         `json:"input_kind,omitempty"`
	FieldKey              string            `json:"field_key,omitempty"`
	ResponseMode          InputResponseMode `json:"response_mode,omitempty"`
	InputValue            string            `json:"input_value,omitempty"`
	IsSensitive           bool              `json:"is_sensitive,omitempty"`
	StartedAt             time.Time         `json:"started_at"`
	FinishedAt            time.Time         `json:"finished_at"`
}

type ConversationMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
