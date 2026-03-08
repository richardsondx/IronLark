package graph

import "time"

const (
	ModeLight = "light"
	ModeFull  = "full"
)

type HostCapabilities struct {
	Host             string    `json:"host"`
	User             string    `json:"user"`
	DetectedAt       time.Time `json:"detected_at"`
	DockerAvailable  bool      `json:"docker_available"`
	SystemdAvailable bool      `json:"systemd_available"`
	JournalAvailable bool      `json:"journal_available"`
	GitAvailable     bool      `json:"git_available"`
	SSAvailable      bool      `json:"ss_available"`
	PSAvailable      bool      `json:"ps_available"`
	RepoRoots        []string  `json:"repo_roots,omitempty"`
	CronPaths        []string  `json:"cron_paths,omitempty"`
	TimersAvailable  bool      `json:"timers_available"`
	ProxyHints       []string  `json:"proxy_hints,omitempty"`
	DatabaseHints    []string  `json:"database_hints,omitempty"`
}

type CrawlerSelection struct {
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Skipped  bool   `json:"skipped,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Depth    string `json:"depth,omitempty"`
	Timeout  int    `json:"timeout_sec,omitempty"`
	FailedAt string `json:"failed_at,omitempty"`
}

type CrawlPlan struct {
	Mode       string             `json:"mode"`
	Host       string             `json:"host"`
	Generated  time.Time          `json:"generated_at"`
	Selections []CrawlerSelection `json:"selections"`
}

type Service struct {
	Name        string `json:"name"`
	LoadState   string `json:"load_state,omitempty"`
	ActiveState string `json:"active_state,omitempty"`
	SubState    string `json:"sub_state,omitempty"`
	MainPID     int    `json:"main_pid,omitempty"`
}

type Process struct {
	PID     int    `json:"pid"`
	PPID    int    `json:"ppid,omitempty"`
	User    string `json:"user,omitempty"`
	Command string `json:"command,omitempty"`
}

type Listener struct {
	Proto   string `json:"proto,omitempty"`
	Address string `json:"address,omitempty"`
	Port    int    `json:"port,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Process string `json:"process,omitempty"`
}

type Container struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Image   string `json:"image,omitempty"`
	State   string `json:"state,omitempty"`
	Status  string `json:"status,omitempty"`
	Ports   string `json:"ports,omitempty"`
	Running bool   `json:"running,omitempty"`
}

type Repo struct {
	Root   string `json:"root"`
	Branch string `json:"branch,omitempty"`
	Head   string `json:"head,omitempty"`
	Dirty  bool   `json:"dirty,omitempty"`
}

type ImportantFile struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size,omitempty"`
	Mode    string    `json:"mode,omitempty"`
	ModTime time.Time `json:"mod_time,omitempty"`
}

type Schedule struct {
	Name   string `json:"name"`
	Source string `json:"source,omitempty"`
}

type LogIncident struct {
	Source  string `json:"source"`
	Summary string `json:"summary"`
}

type SecurityFinding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
}

type GraphRelation struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Type     string `json:"type"`
	Evidence string `json:"evidence,omitempty"`
}

type GraphSnapshot struct {
	ID           string             `json:"id"`
	Host         string             `json:"host"`
	User         string             `json:"user"`
	CWD          string             `json:"cwd,omitempty"`
	CollectedAt  time.Time          `json:"collected_at"`
	Mode         string             `json:"mode"`
	Capabilities HostCapabilities   `json:"capabilities"`
	Coverage     []CrawlerSelection `json:"coverage,omitempty"`
	Services     []Service          `json:"services,omitempty"`
	Processes    []Process          `json:"processes,omitempty"`
	Listeners    []Listener         `json:"listeners,omitempty"`
	Containers   []Container        `json:"containers,omitempty"`
	Repos        []Repo             `json:"repos,omitempty"`
	Files        []ImportantFile    `json:"files,omitempty"`
	Schedules    []Schedule         `json:"schedules,omitempty"`
	Logs         []LogIncident      `json:"logs,omitempty"`
	Security     []SecurityFinding  `json:"security,omitempty"`
	Relations    []GraphRelation    `json:"relations,omitempty"`
}

type GraphEvent struct {
	ID         string    `json:"id"`
	SnapshotID string    `json:"snapshot_id"`
	Host       string    `json:"host"`
	OccurredAt time.Time `json:"occurred_at"`
	Type       string    `json:"type"`
	Severity   string    `json:"severity"`
	Summary    string    `json:"summary"`
	EntityIDs  []string  `json:"entity_ids,omitempty"`
	Evidence   []string  `json:"evidence,omitempty"`
	RelatedTo  []string  `json:"related_to,omitempty"`
}

type GraphSummary struct {
	Host       string             `json:"host"`
	SnapshotAt time.Time          `json:"snapshot_at"`
	Coverage   []CrawlerSelection `json:"coverage,omitempty"`
	Highlights []string           `json:"highlights,omitempty"`
	Relations  []GraphRelation    `json:"relations,omitempty"`
	Recent     []GraphEvent       `json:"recent_events,omitempty"`
}

type WatchState struct {
	Enabled   bool      `json:"enabled"`
	UpdatedAt time.Time `json:"updated_at"`
	Interval  string    `json:"interval,omitempty"`
}
