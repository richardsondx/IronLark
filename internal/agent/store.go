package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Workspace struct {
	Key            string    `json:"key"`
	User           string    `json:"user"`
	Host           string    `json:"host"`
	CWD            string    `json:"cwd"`
	ThreadID       string    `json:"thread_id,omitempty"`
	SessionID      string    `json:"session_id"`
	RunnerPID      int       `json:"runner_pid,omitempty"`
	SocketPath     string    `json:"socket_path"`
	State          string    `json:"state,omitempty"`
	LastAttachedAt time.Time `json:"last_attached_at,omitempty"`
	LastActiveAt   time.Time `json:"last_active_at"`
	CreatedAt      time.Time `json:"created_at"`
}

type Store struct {
	Dir string
}

func CurrentIdentity() (string, string) {
	currentUser := "unknown"
	if u, err := user.Current(); err == nil && strings.TrimSpace(u.Username) != "" {
		currentUser = strings.TrimSpace(u.Username)
	}
	host := "unknown"
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		host = strings.TrimSpace(h)
	}
	return currentUser, host
}

func WorkspaceKey(host, cwd string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(host) + "|" + filepath.Clean(cwd)))
	return hex.EncodeToString(sum[:])[:12]
}

func DefaultSessionPrefix(prefix string) string {
	prefix = sanitizeToken(prefix)
	if prefix == "" {
		return "ironlark"
	}
	return prefix
}

func BuildWorkspace(prefix, storeDir, user, host, cwd, threadID string) Workspace {
	key := WorkspaceKey(host, cwd)
	sessionID := fmt.Sprintf("%s-%s", DefaultSessionPrefix(prefix), key)
	return Workspace{
		Key:        key,
		User:       user,
		Host:       host,
		CWD:        filepath.Clean(cwd),
		ThreadID:   strings.TrimSpace(threadID),
		SessionID:  sessionID,
		SocketPath: filepath.Join(storeDir, key+".sock"),
		State:      StateStopped,
	}
}

func (s Store) Load(key string) (Workspace, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir, key+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return Workspace{}, nil
		}
		return Workspace{}, fmt.Errorf("read agent workspace: %w", err)
	}
	var workspace Workspace
	if err := json.Unmarshal(data, &workspace); err != nil {
		return Workspace{}, fmt.Errorf("decode agent workspace: %w", err)
	}
	if strings.TrimSpace(workspace.Key) == "" {
		workspace.Key = key
	}
	if strings.TrimSpace(workspace.SocketPath) == "" {
		workspace.SocketPath = filepath.Join(s.Dir, workspace.Key+".sock")
	}
	if strings.TrimSpace(workspace.SessionID) == "" {
		workspace.SessionID = fmt.Sprintf("%s-%s", DefaultSessionPrefix(""), workspace.Key)
	}
	if strings.TrimSpace(workspace.State) == "" {
		workspace.State = StateStopped
	}
	return workspace, nil
}

func (s Store) Save(workspace Workspace) error {
	if strings.TrimSpace(workspace.Key) == "" {
		return fmt.Errorf("workspace key is required")
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("create agent directory: %w", err)
	}
	now := time.Now().UTC()
	if workspace.CreatedAt.IsZero() {
		workspace.CreatedAt = now
	}
	workspace.LastActiveAt = now
	data, err := json.MarshalIndent(workspace, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent workspace: %w", err)
	}
	return os.WriteFile(filepath.Join(s.Dir, workspace.Key+".json"), data, 0o600)
}

func (s Store) Delete(key string) error {
	err := os.Remove(filepath.Join(s.Dir, key+".json"))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete agent workspace: %w", err)
	}
	return nil
}

func (s Store) List() ([]Workspace, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list agent workspaces: %w", err)
	}
	workspaces := make([]Workspace, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		workspace, err := s.Load(strings.TrimSuffix(entry.Name(), ".json"))
		if err == nil && workspace.Key != "" {
			workspaces = append(workspaces, workspace)
		}
	}
	sort.Slice(workspaces, func(i, j int) bool {
		return workspaces[i].LastActiveAt.After(workspaces[j].LastActiveAt)
	})
	return workspaces, nil
}

func sanitizeToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-_")
}
