package graph

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	cfgpkg "github.com/richardsondx/IronLark/internal/config"
)

type Manager struct {
	Store      Store
	Config     cfgpkg.GraphConfig
	WorkingDir string
	Host       string
	User       string
	Run        func(context.Context, string, ...string) (string, error)
	Now        func() time.Time
}

func NewManager(store Store, cfg cfgpkg.GraphConfig, cwd string) *Manager {
	host, _ := os.Hostname()
	user := os.Getenv("USER")
	return &Manager{
		Store:      store,
		Config:     cfg,
		WorkingDir: cwd,
		Host:       strings.TrimSpace(host),
		User:       strings.TrimSpace(user),
	}
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m *Manager) run(ctx context.Context, name string, args ...string) (string, error) {
	if m.Run != nil {
		return m.Run(ctx, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = m.WorkingDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (m *Manager) HostKey() string {
	host := firstNonEmpty(m.Host, "unknown-host")
	user := firstNonEmpty(m.User, "unknown-user")
	return user + "@" + host
}

func (m *Manager) Enabled() bool {
	return enabledPtr(m.Config.Enabled, true)
}

func (m *Manager) WatchEnabled() bool {
	return enabledPtr(m.Config.Watch.Enabled, false)
}

func (m *Manager) EnsureFresh(ctx context.Context, mode string) (GraphSnapshot, []GraphEvent, error) {
	if !m.Enabled() {
		return GraphSnapshot{}, nil, nil
	}
	latest, err := m.Store.Latest(m.HostKey())
	if err != nil {
		return GraphSnapshot{}, nil, err
	}
	if mode == "" {
		mode = ModeLight
	}
	if latest.ID != "" && mode == ModeLight {
		minInterval, err := time.ParseDuration(firstNonEmpty(m.Config.RefreshMinInterval, "5m"))
		if err != nil {
			minInterval = 5 * time.Minute
		}
		if latest.CollectedAt.Add(minInterval).After(m.now()) {
			return latest, nil, nil
		}
	}
	return m.Crawl(ctx, mode)
}

func (m *Manager) Crawl(ctx context.Context, mode string) (GraphSnapshot, []GraphEvent, error) {
	if !m.Enabled() {
		return GraphSnapshot{}, nil, nil
	}
	if mode == "" {
		mode = ModeFull
	}
	caps := m.DetectCapabilities(ctx)
	prev, err := m.Store.Latest(m.HostKey())
	if err != nil {
		return GraphSnapshot{}, nil, err
	}
	plan := m.BuildPlan(caps, prev, mode)
	snapshot := GraphSnapshot{
		ID:           snapshotID(m.HostKey(), m.now()),
		Host:         m.HostKey(),
		User:         m.User,
		CWD:          m.WorkingDir,
		CollectedAt:  m.now(),
		Mode:         mode,
		Capabilities: caps,
		Coverage:     plan.Selections,
	}

	for idx, sel := range plan.Selections {
		if sel.Skipped || !sel.Enabled {
			continue
		}
		switch sel.Name {
		case "process":
			snapshot.Processes = m.crawlProcesses(ctx)
		case "network":
			snapshot.Listeners = m.crawlNetwork(ctx)
		case "service":
			snapshot.Services = m.crawlServices(ctx)
		case "docker":
			snapshot.Containers = m.crawlDocker(ctx, mode)
		case "git":
			snapshot.Repos = m.crawlGit(ctx, caps.RepoRoots)
		case "file":
			snapshot.Files = m.crawlFiles(caps)
		case "cron":
			snapshot.Schedules = m.crawlSchedules(ctx, caps)
		case "log":
			snapshot.Logs = m.crawlLogs(ctx, snapshot, mode)
		case "security":
			snapshot.Security = m.crawlSecurity(snapshot)
		}
		plan.Selections[idx].Reason = strings.TrimSpace(plan.Selections[idx].Reason)
	}
	snapshot.Coverage = plan.Selections
	snapshot.Relations = m.inferRelations(snapshot)
	events := m.diff(prev, snapshot)
	if err := m.Store.SaveSnapshot(snapshot, m.trimEvents(events)); err != nil {
		return GraphSnapshot{}, nil, err
	}
	return snapshot, events, nil
}

func (m *Manager) DetectCapabilities(ctx context.Context) HostCapabilities {
	caps := HostCapabilities{
		Host:       m.HostKey(),
		User:       m.User,
		DetectedAt: m.now(),
	}
	caps.DockerAvailable = m.commandExists(ctx, "docker")
	caps.SystemdAvailable = m.commandExists(ctx, "systemctl")
	caps.JournalAvailable = m.commandExists(ctx, "journalctl")
	caps.GitAvailable = m.commandExists(ctx, "git")
	caps.SSAvailable = m.commandExists(ctx, "ss")
	caps.PSAvailable = m.commandExists(ctx, "ps")
	if caps.GitAvailable {
		if root := m.repoRoot(ctx, m.WorkingDir); root != "" {
			caps.RepoRoots = append(caps.RepoRoots, root)
		}
	}
	for _, cronPath := range []string{"/etc/crontab", "/etc/cron.d", "/etc/cron.daily", "/etc/cron.hourly"} {
		if _, err := os.Stat(cronPath); err == nil {
			caps.CronPaths = append(caps.CronPaths, cronPath)
		}
	}
	if caps.SystemdAvailable {
		if _, err := m.run(ctx, "sh", "-lc", "systemctl list-timers --all --no-legend >/dev/null 2>&1"); err == nil {
			caps.TimersAvailable = true
		}
	}
	for _, hint := range []string{"nginx.conf", "sites-enabled", "Caddyfile", "httpd.conf"} {
		if m.pathExists(filepath.Join(m.WorkingDir, hint)) || m.pathExists(filepath.Join("/etc/nginx", hint)) {
			caps.ProxyHints = append(caps.ProxyHints, hint)
		}
	}
	for _, db := range []string{"postgres", "mysql", "redis"} {
		if m.commandExists(ctx, db) {
			caps.DatabaseHints = append(caps.DatabaseHints, db)
		}
	}
	return caps
}

func (m *Manager) BuildPlan(caps HostCapabilities, prev GraphSnapshot, mode string) CrawlPlan {
	selections := []CrawlerSelection{
		m.selectCrawler("process", mode, caps.PSAvailable, "ps is unavailable"),
		m.selectCrawler("network", mode, caps.SSAvailable, "ss is unavailable"),
		m.selectCrawler("service", mode, caps.SystemdAvailable, "systemctl is unavailable"),
		m.selectCrawler("docker", mode, caps.DockerAvailable, "docker is unavailable"),
		m.selectCrawler("file", mode, true, ""),
		m.selectCrawler("git", mode, caps.GitAvailable && len(caps.RepoRoots) > 0, "no git repository detected"),
		m.selectCrawler("cron", mode, len(caps.CronPaths) > 0 || caps.TimersAvailable, "no cron or timers detected"),
		m.selectCrawler("log", mode, caps.JournalAvailable || caps.DockerAvailable, "no supported log sources detected"),
		m.selectCrawler("security", mode, true, ""),
	}
	if mode == ModeLight {
		for idx := range selections {
			switch selections[idx].Name {
			case "file", "cron", "security":
				if !selections[idx].Skipped {
					selections[idx].Skipped = true
					selections[idx].Reason = "excluded from light crawl"
				}
			case "git":
				if prev.ID == "" {
					selections[idx].Depth = "summary"
				}
			}
		}
	}
	if len(prev.Services) == 0 && len(prev.Containers) == 0 {
		for idx := range selections {
			if selections[idx].Name == "log" && !selections[idx].Skipped && mode == ModeLight {
				selections[idx].Skipped = true
				selections[idx].Reason = "no tracked services or containers yet"
			}
		}
	}
	return CrawlPlan{
		Mode:       mode,
		Host:       m.HostKey(),
		Generated:  m.now(),
		Selections: selections,
	}
}

func (m *Manager) Summary(since time.Time) (GraphSummary, error) {
	snapshot, err := m.Store.Latest(m.HostKey())
	if err != nil {
		return GraphSummary{}, err
	}
	events, err := m.Store.EventsSince(m.HostKey(), since)
	if err != nil {
		return GraphSummary{}, err
	}
	summary := GraphSummary{
		Host:       snapshot.Host,
		SnapshotAt: snapshot.CollectedAt,
		Coverage:   snapshot.Coverage,
		Relations:  snapshot.Relations,
		Recent:     tailEvents(events, 10),
	}
	summary.Highlights = append(summary.Highlights,
		fmt.Sprintf("services=%d processes=%d listeners=%d containers=%d", len(snapshot.Services), len(snapshot.Processes), len(snapshot.Listeners), len(snapshot.Containers)),
		fmt.Sprintf("important_files=%d repos=%d schedules=%d findings=%d", len(snapshot.Files), len(snapshot.Repos), len(snapshot.Schedules), len(snapshot.Security)),
	)
	for _, sel := range snapshot.Coverage {
		if sel.Skipped {
			summary.Highlights = append(summary.Highlights, fmt.Sprintf("%s skipped: %s", sel.Name, sel.Reason))
		}
	}
	return summary, nil
}

func (m *Manager) Digest(limit int) string {
	if !m.Enabled() {
		return ""
	}
	snapshot, err := m.Store.Latest(m.HostKey())
	if err != nil || snapshot.ID == "" {
		return ""
	}
	events, _ := m.Store.EventsSince(m.HostKey(), m.now().Add(-6*time.Hour))
	lines := []string{
		fmt.Sprintf("Server graph snapshot at %s", snapshot.CollectedAt.Format(time.RFC3339)),
		fmt.Sprintf("Services: %s", joinServiceNames(snapshot.Services, 6)),
		fmt.Sprintf("Ports: %s", joinPorts(snapshot.Listeners, 8)),
		fmt.Sprintf("Containers: %s", joinContainerNames(snapshot.Containers, 6)),
		fmt.Sprintf("Relations: %s", joinRelations(snapshot.Relations, 6)),
		fmt.Sprintf("Recent changes: %s", joinEvents(events, 5)),
	}
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if !strings.HasSuffix(line, ": ") {
			filtered = append(filtered, line)
		}
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	return strings.Join(filtered, "\n")
}

func (m *Manager) selectCrawler(name, mode string, available bool, unavailableReason string) CrawlerSelection {
	cfg, ok := m.Config.Crawlers[name]
	enabled := !ok || enabledPtr(cfg.Enabled, true)
	sel := CrawlerSelection{Name: name, Enabled: enabled, Depth: mode}
	if !enabled {
		sel.Skipped = true
		sel.Reason = "disabled in config"
		return sel
	}
	if !available {
		sel.Skipped = true
		sel.Reason = unavailableReason
		return sel
	}
	if mode == ModeLight {
		sel.Timeout = 5
	} else {
		sel.Timeout = 15
	}
	return sel
}

func (m *Manager) commandExists(ctx context.Context, name string) bool {
	out, err := m.run(ctx, "sh", "-lc", "command -v "+name+" >/dev/null 2>&1 && echo yes || echo no")
	return err == nil && strings.TrimSpace(out) == "yes"
}

func (m *Manager) repoRoot(ctx context.Context, cwd string) string {
	out, err := m.run(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (m *Manager) pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (m *Manager) crawlProcesses(ctx context.Context) []Process {
	out, err := m.run(ctx, "sh", "-lc", "ps -eo pid=,ppid=,user=,comm= | head -n 25")
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	processes := make([]Process, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		ppid, _ := strconv.Atoi(fields[1])
		processes = append(processes, Process{
			PID:     pid,
			PPID:    ppid,
			User:    fields[2],
			Command: strings.Join(fields[3:], " "),
		})
	}
	return processes
}

func (m *Manager) crawlNetwork(ctx context.Context) []Listener {
	out, err := m.run(ctx, "sh", "-lc", "ss -ltnpH 2>/dev/null | head -n 25")
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	listeners := make([]Listener, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		host, port := splitAddress(fields[3])
		listener := Listener{
			Proto:   fields[0],
			Address: host,
			Port:    port,
		}
		if len(fields) > 5 {
			listener.Process = strings.Join(fields[5:], " ")
			listener.PID = extractPID(listener.Process)
		}
		listeners = append(listeners, listener)
	}
	return listeners
}

func (m *Manager) crawlServices(ctx context.Context) []Service {
	out, err := m.run(ctx, "sh", "-lc", "systemctl list-units --type=service --all --no-legend --no-pager | head -n 20")
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	services := make([]Service, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		services = append(services, Service{
			Name:        fields[0],
			LoadState:   fields[1],
			ActiveState: fields[2],
			SubState:    fields[3],
		})
	}
	return services
}

func (m *Manager) crawlDocker(ctx context.Context, mode string) []Container {
	command := "docker ps --format '{{.ID}}|{{.Names}}|{{.Image}}|{{.State}}|{{.Status}}|{{.Ports}}' | head -n 20"
	if mode == ModeLight {
		command = "docker ps --format '{{.ID}}|{{.Names}}|{{.Image}}|{{.State}}|{{.Status}}|{{.Ports}}' | head -n 10"
	}
	out, err := m.run(ctx, "sh", "-lc", command)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	containers := make([]Container, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "|")
		for len(parts) < 6 {
			parts = append(parts, "")
		}
		containers = append(containers, Container{
			ID:      parts[0],
			Name:    parts[1],
			Image:   parts[2],
			State:   parts[3],
			Status:  parts[4],
			Ports:   parts[5],
			Running: strings.EqualFold(parts[3], "running"),
		})
	}
	return containers
}

func (m *Manager) crawlGit(ctx context.Context, roots []string) []Repo {
	repos := make([]Repo, 0, len(roots))
	for _, root := range roots {
		branch, _ := m.run(ctx, "git", "-C", root, "rev-parse", "--abbrev-ref", "HEAD")
		head, _ := m.run(ctx, "git", "-C", root, "rev-parse", "--short", "HEAD")
		status, _ := m.run(ctx, "git", "-C", root, "status", "--short")
		repos = append(repos, Repo{
			Root:   root,
			Branch: strings.TrimSpace(branch),
			Head:   strings.TrimSpace(head),
			Dirty:  strings.TrimSpace(status) != "",
		})
	}
	return repos
}

func (m *Manager) crawlFiles(caps HostCapabilities) []ImportantFile {
	paths := []string{}
	for _, path := range []string{
		filepath.Join(m.WorkingDir, ".env.example"),
		filepath.Join(m.WorkingDir, "docker-compose.yml"),
		filepath.Join(m.WorkingDir, "compose.yml"),
		filepath.Join(m.WorkingDir, "package.json"),
		filepath.Join(m.WorkingDir, "go.mod"),
		"/etc/nginx/nginx.conf",
		"/etc/crontab",
	} {
		if m.pathExists(path) {
			paths = append(paths, path)
		}
	}
	for _, root := range caps.RepoRoots {
		for _, candidate := range []string{"package.json", "go.mod", "Dockerfile", "docker-compose.yml", "compose.yml"} {
			path := filepath.Join(root, candidate)
			if m.pathExists(path) {
				paths = append(paths, path)
			}
		}
	}
	paths = dedupe(paths)
	files := make([]ImportantFile, 0, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		files = append(files, ImportantFile{
			Path:    path,
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			ModTime: info.ModTime().UTC(),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

func (m *Manager) crawlSchedules(ctx context.Context, caps HostCapabilities) []Schedule {
	schedules := make([]Schedule, 0, len(caps.CronPaths)+4)
	for _, cronPath := range caps.CronPaths {
		schedules = append(schedules, Schedule{Name: filepath.Base(cronPath), Source: cronPath})
	}
	if caps.TimersAvailable {
		out, err := m.run(ctx, "sh", "-lc", "systemctl list-timers --all --no-legend --no-pager | head -n 10")
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 0 {
					continue
				}
				name := fields[len(fields)-1]
				schedules = append(schedules, Schedule{Name: name, Source: "systemd-timer"})
			}
		}
	}
	return schedules
}

func (m *Manager) crawlLogs(ctx context.Context, snapshot GraphSnapshot, mode string) []LogIncident {
	maxLines := 3
	if mode == ModeFull {
		maxLines = 5
	}
	incidents := []LogIncident{}
	for _, service := range snapshot.Services {
		if service.ActiveState == "failed" || service.SubState == "failed" {
			incidents = append(incidents, LogIncident{
				Source:  service.Name,
				Summary: fmt.Sprintf("service state=%s/%s", service.ActiveState, service.SubState),
			})
		}
		if len(incidents) >= maxLines {
			return incidents
		}
	}
	for _, container := range snapshot.Containers {
		if !container.Running {
			incidents = append(incidents, LogIncident{
				Source:  container.Name,
				Summary: fmt.Sprintf("container state=%s status=%s", container.State, container.Status),
			})
		}
		if len(incidents) >= maxLines {
			return incidents
		}
	}
	return incidents
}

func (m *Manager) crawlSecurity(snapshot GraphSnapshot) []SecurityFinding {
	findings := []SecurityFinding{}
	for _, listener := range snapshot.Listeners {
		if listener.Port == 22 || listener.Port == 80 || listener.Port == 443 {
			continue
		}
		if listener.Address == "*" || listener.Address == "0.0.0.0" {
			findings = append(findings, SecurityFinding{
				ID:       fmt.Sprintf("listener:%d", listener.Port),
				Severity: "medium",
				Summary:  fmt.Sprintf("public listener on port %d", listener.Port),
			})
		}
	}
	return findings
}

func (m *Manager) inferRelations(snapshot GraphSnapshot) []GraphRelation {
	relations := []GraphRelation{}
	serviceByPID := map[int]string{}
	for _, service := range snapshot.Services {
		if service.MainPID > 0 {
			serviceByPID[service.MainPID] = service.Name
		}
	}
	for _, listener := range snapshot.Listeners {
		if listener.PID > 0 {
			if service, ok := serviceByPID[listener.PID]; ok {
				relations = append(relations, GraphRelation{
					From:     "service:" + service,
					To:       fmt.Sprintf("port:%d", listener.Port),
					Type:     "listens_on",
					Evidence: listener.Process,
				})
			}
			if listener.Process != "" {
				relations = append(relations, GraphRelation{
					From:     "process:" + listener.Process,
					To:       fmt.Sprintf("port:%d", listener.Port),
					Type:     "listens_on",
					Evidence: listener.Address,
				})
			}
		}
	}
	for _, container := range snapshot.Containers {
		if container.Ports != "" {
			relations = append(relations, GraphRelation{
				From:     "container:" + firstNonEmpty(container.Name, container.ID),
				To:       "ports:" + container.Ports,
				Type:     "publishes",
				Evidence: container.Image,
			})
		}
	}
	return dedupeRelations(relations)
}

func (m *Manager) diff(prev, current GraphSnapshot) []GraphEvent {
	if prev.ID == "" {
		return []GraphEvent{{
			ID:         snapshotID(current.Host, current.CollectedAt),
			SnapshotID: current.ID,
			Host:       current.Host,
			OccurredAt: current.CollectedAt,
			Type:       "graph.initialized",
			Severity:   "info",
			Summary:    "initialized server graph",
		}}
	}
	events := []GraphEvent{}
	events = append(events, diffNames(current, "service", serviceNames(prev.Services), serviceNames(current.Services), current.CollectedAt)...)
	events = append(events, diffNames(current, "process", processNames(prev.Processes), processNames(current.Processes), current.CollectedAt)...)
	events = append(events, diffNames(current, "container", containerNames(prev.Containers), containerNames(current.Containers), current.CollectedAt)...)
	events = append(events, diffNames(current, "port", listenerNames(prev.Listeners), listenerNames(current.Listeners), current.CollectedAt)...)
	events = append(events, diffNames(current, "schedule", scheduleNames(prev.Schedules), scheduleNames(current.Schedules), current.CollectedAt)...)
	prevFiles := fileMap(prev.Files)
	for _, file := range current.Files {
		if old, ok := prevFiles[file.Path]; ok && !old.ModTime.Equal(file.ModTime) {
			events = append(events, GraphEvent{
				ID:         hashParts(current.ID, "file", file.Path),
				SnapshotID: current.ID,
				Host:       current.Host,
				OccurredAt: current.CollectedAt,
				Type:       "file.changed",
				Severity:   "info",
				Summary:    "important file changed: " + file.Path,
				EntityIDs:  []string{"file:" + file.Path},
			})
		}
	}
	return dedupeEvents(events)
}

func (m *Manager) trimEvents(events []GraphEvent) []GraphEvent {
	retention := m.Config.RetentionDays
	if retention <= 0 {
		retention = 14
	}
	cutoff := m.now().AddDate(0, 0, -retention)
	filtered := make([]GraphEvent, 0, len(events))
	for _, event := range events {
		if event.OccurredAt.After(cutoff) || event.OccurredAt.Equal(cutoff) {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func ParseSince(value string, now time.Time) (time.Time, error) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "", "all":
		return time.Time{}, nil
	case "today":
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()), nil
	case "yesterday":
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		return start.Add(-24 * time.Hour), nil
	default:
		d, err := time.ParseDuration(value)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse since value %q: %w", value, err)
		}
		return now.Add(-d), nil
	}
}

func snapshotID(host string, now time.Time) string {
	return hashParts(host, now.UTC().Format(time.RFC3339Nano))
}

func hashParts(parts ...string) string {
	hash := sha1.Sum([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(hash[:8])
}

func diffNames(current GraphSnapshot, prefix string, prev, next []string, at time.Time) []GraphEvent {
	events := []GraphEvent{}
	prevSet := toSet(prev)
	nextSet := toSet(next)
	for _, name := range next {
		if !prevSet[name] {
			events = append(events, GraphEvent{
				ID:         hashParts(current.ID, prefix, "appeared", name),
				SnapshotID: current.ID,
				Host:       current.Host,
				OccurredAt: at,
				Type:       prefix + ".appeared",
				Severity:   "info",
				Summary:    fmt.Sprintf("%s appeared: %s", prefix, name),
				EntityIDs:  []string{prefix + ":" + name},
			})
		}
	}
	for _, name := range prev {
		if !nextSet[name] {
			events = append(events, GraphEvent{
				ID:         hashParts(current.ID, prefix, "disappeared", name),
				SnapshotID: current.ID,
				Host:       current.Host,
				OccurredAt: at,
				Type:       prefix + ".disappeared",
				Severity:   "warning",
				Summary:    fmt.Sprintf("%s disappeared: %s", prefix, name),
				EntityIDs:  []string{prefix + ":" + name},
			})
		}
	}
	return events
}

func enabledPtr(value *bool, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	return *value
}

func splitAddress(raw string) (string, int) {
	raw = strings.TrimSpace(raw)
	if idx := strings.LastIndex(raw, ":"); idx >= 0 {
		host := raw[:idx]
		port, _ := strconv.Atoi(strings.Trim(raw[idx+1:], "[]"))
		return strings.Trim(host, "[]"), port
	}
	return raw, 0
}

func extractPID(value string) int {
	start := strings.Index(value, "pid=")
	if start < 0 {
		return 0
	}
	start += len("pid=")
	end := start
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	pid, _ := strconv.Atoi(value[start:end])
	return pid
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func dedupe(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok || value == "" {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func dedupeRelations(values []GraphRelation) []GraphRelation {
	seen := map[string]struct{}{}
	out := make([]GraphRelation, 0, len(values))
	for _, value := range values {
		key := value.From + "|" + value.To + "|" + value.Type
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func dedupeEvents(values []GraphEvent) []GraphEvent {
	seen := map[string]struct{}{}
	out := make([]GraphEvent, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value.ID]; ok {
			continue
		}
		seen[value.ID] = struct{}{}
		out = append(out, value)
	}
	return out
}

func toSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func serviceNames(values []Service) []string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		names = append(names, value.Name+":"+value.ActiveState)
	}
	sort.Strings(names)
	return names
}

func processNames(values []Process) []string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		names = append(names, fmt.Sprintf("%d:%s", value.PID, value.Command))
	}
	sort.Strings(names)
	return names
}

func containerNames(values []Container) []string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		names = append(names, firstNonEmpty(value.Name, value.ID)+":"+value.State)
	}
	sort.Strings(names)
	return names
}

func listenerNames(values []Listener) []string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		names = append(names, fmt.Sprintf("%s:%d", value.Address, value.Port))
	}
	sort.Strings(names)
	return names
}

func scheduleNames(values []Schedule) []string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		names = append(names, value.Name)
	}
	sort.Strings(names)
	return names
}

func fileMap(values []ImportantFile) map[string]ImportantFile {
	out := make(map[string]ImportantFile, len(values))
	for _, value := range values {
		out[value.Path] = value
	}
	return out
}

func tailEvents(values []GraphEvent, max int) []GraphEvent {
	if max <= 0 || len(values) <= max {
		return values
	}
	return values[len(values)-max:]
}

func joinServiceNames(values []Service, max int) string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		names = append(names, value.Name+"="+value.ActiveState)
	}
	return joinLimited(names, max)
}

func joinPorts(values []Listener, max int) string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		names = append(names, fmt.Sprintf("%d/%s", value.Port, value.Proto))
	}
	return joinLimited(names, max)
}

func joinContainerNames(values []Container, max int) string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		names = append(names, firstNonEmpty(value.Name, value.ID)+"="+value.State)
	}
	return joinLimited(names, max)
}

func joinRelations(values []GraphRelation, max int) string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		names = append(names, fmt.Sprintf("%s %s %s", value.From, value.Type, value.To))
	}
	return joinLimited(names, max)
}

func joinEvents(values []GraphEvent, max int) string {
	names := make([]string, 0, len(values))
	for _, value := range tailEvents(values, max) {
		names = append(names, value.Summary)
	}
	return joinLimited(names, max)
}

func joinLimited(values []string, max int) string {
	if len(values) == 0 {
		return ""
	}
	if max > 0 && len(values) > max {
		values = append(values[:max], fmt.Sprintf("+%d more", len(values)-max))
	}
	return strings.Join(values, ", ")
}
