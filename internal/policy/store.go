package policy

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/richardsondx/IronLark/internal/core"
)

type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionDeny  Decision = "deny"
)

type RuleKind string

const (
	RuleActionType    RuleKind = "action_type"
	RuleCommandPrefix RuleKind = "command_prefix"
	RulePathPrefix    RuleKind = "path_prefix"
)

type Rule struct {
	ID        string    `json:"id"`
	Decision  Decision  `json:"decision"`
	Kind      RuleKind  `json:"kind"`
	Value     string    `json:"value"`
	CreatedAt time.Time `json:"created_at"`
	Action    string    `json:"action,omitempty"`
	Scope     string    `json:"scope,omitempty"`
}

type File struct {
	AutoAcceptThrough core.RiskLevel `json:"auto_accept_through,omitempty"`
	Rules             []Rule         `json:"rules"`
}

type Store struct {
	Path string
}

type Match struct {
	Rule     Rule
	Matched  bool
	Decision Decision
}

type Resolution struct {
	Match             Match
	AutoAcceptThrough core.RiskLevel
}

func (s Store) Load() (File, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return File{}, nil
		}
		return File{}, fmt.Errorf("read policy store: %w", err)
	}
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return File{}, fmt.Errorf("unmarshal policy store: %w", err)
	}
	if file.AutoAcceptThrough != "" && !file.AutoAcceptThrough.Valid() {
		return File{}, fmt.Errorf("invalid auto_accept_through %q", file.AutoAcceptThrough)
	}
	return file, nil
}

func (s Store) Save(file File) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return fmt.Errorf("create policy directory: %w", err)
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal policy store: %w", err)
	}
	if err := os.WriteFile(s.Path, data, 0o600); err != nil {
		return fmt.Errorf("write policy store: %w", err)
	}
	return nil
}

func (s Store) List() ([]Rule, error) {
	file, err := s.Load()
	if err != nil {
		return nil, err
	}
	return file.Rules, nil
}

func (s Store) AutoAcceptThrough() (core.RiskLevel, bool, error) {
	file, err := s.Load()
	if err != nil {
		return "", false, err
	}
	if !file.AutoAcceptThrough.Valid() {
		return "", false, nil
	}
	return file.AutoAcceptThrough, true, nil
}

func (s Store) Add(rule Rule) (Rule, error) {
	file, err := s.Load()
	if err != nil {
		return Rule{}, err
	}
	if strings.TrimSpace(rule.ID) == "" {
		rule.ID = time.Now().UTC().Format("20060102T150405.000000000")
	}
	if rule.CreatedAt.IsZero() {
		rule.CreatedAt = time.Now().UTC()
	}
	file.Rules = append(file.Rules, rule)
	return rule, s.Save(file)
}

func (s Store) Remove(id string) error {
	file, err := s.Load()
	if err != nil {
		return err
	}
	filtered := file.Rules[:0]
	for _, rule := range file.Rules {
		if rule.ID != id {
			filtered = append(filtered, rule)
		}
	}
	file.Rules = filtered
	return s.Save(file)
}

func (s Store) Evaluate(action core.Action) (Match, error) {
	resolution, err := s.Resolve(action)
	if err != nil {
		return Match{}, err
	}
	return resolution.Match, nil
}

func (s Store) Resolve(action core.Action) (Resolution, error) {
	file, err := s.Load()
	if err != nil {
		return Resolution{}, err
	}
	resolution := Resolution{AutoAcceptThrough: file.AutoAcceptThrough}
	var allow *Rule
	for _, rule := range file.Rules {
		if matchesRule(rule, action) {
			if rule.Decision == DecisionDeny {
				resolution.Match = Match{Rule: rule, Matched: true, Decision: DecisionDeny}
				return resolution, nil
			}
			r := rule
			allow = &r
		}
	}
	if allow != nil {
		resolution.Match = Match{Rule: *allow, Matched: true, Decision: DecisionAllow}
	}
	return resolution, nil
}

func (s Store) SetAutoAcceptThrough(level core.RiskLevel) error {
	if !level.Valid() {
		return fmt.Errorf("invalid auto accept risk level %q", level)
	}
	file, err := s.Load()
	if err != nil {
		return err
	}
	file.AutoAcceptThrough = level
	return s.Save(file)
}

func (s Store) ClearAutoAcceptThrough() error {
	file, err := s.Load()
	if err != nil {
		return err
	}
	file.AutoAcceptThrough = ""
	return s.Save(file)
}

func RuleForAction(action core.Action, decision Decision) Rule {
	rule := Rule{
		Decision: decision,
		Action:   string(action.Type),
		Scope:    "machine",
	}
	switch action.Type {
	case core.ActionRunShell:
		rule.Kind = RuleCommandPrefix
		rule.Value = normalizeCommand(action.Command)
	case core.ActionReadFiles, core.ActionEditFile, core.ActionWriteFile, core.ActionListDir, core.ActionSearchFiles, core.ActionSemanticSearch:
		rule.Kind = RulePathPrefix
		rule.Value = normalizePath(firstNonEmpty(action.Path, firstPath(action.Paths)))
	default:
		rule.Kind = RuleActionType
		rule.Value = string(action.Type)
	}
	return rule
}

func matchesRule(rule Rule, action core.Action) bool {
	if rule.Action != "" && rule.Action != string(action.Type) {
		return false
	}
	switch rule.Kind {
	case RuleActionType:
		return rule.Value == string(action.Type)
	case RuleCommandPrefix:
		return strings.HasPrefix(normalizeCommand(action.Command), rule.Value)
	case RulePathPrefix:
		target := normalizePath(firstNonEmpty(action.Path, firstPath(action.Paths)))
		return strings.HasPrefix(target, rule.Value)
	default:
		return false
	}
}

func normalizeCommand(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	roots := collectCommandRoots(command)
	hosts := collectCommandHosts(command)
	parts := []string{}
	if len(roots) > 0 {
		parts = append(parts, "cmd="+strings.Join(roots, ","))
	}
	if len(hosts) > 0 {
		parts = append(parts, "host="+strings.Join(hosts, ","))
	}
	if len(parts) > 0 {
		return strings.Join(parts, ";")
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func normalizePath(path string) string {
	return strings.TrimSpace(path)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstPath(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

var (
	urlPattern         = regexp.MustCompile(`https?://[^\s"'()]+`)
	commandWordPattern = regexp.MustCompile(`(?:^|[\n;|&()]|\$\()\s*([a-zA-Z][a-zA-Z0-9_./+-]*)`)
	assignmentPattern  = regexp.MustCompile(`\b[a-zA-Z_][a-zA-Z0-9_]*=`)
)

func collectCommandHosts(command string) []string {
	hosts := map[string]struct{}{}
	for _, raw := range urlPattern.FindAllString(command, -1) {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Hostname() == "" {
			continue
		}
		hosts[strings.ToLower(parsed.Hostname())] = struct{}{}
	}
	return sortedKeys(hosts)
}

func collectCommandRoots(command string) []string {
	roots := map[string]struct{}{}
	cleaned := urlPattern.ReplaceAllString(command, " ")
	cleaned = assignmentPattern.ReplaceAllString(cleaned, " ")
	matches := commandWordPattern.FindAllStringSubmatch(cleaned, -1)
	for _, match := range matches {
		word := strings.TrimSpace(match[1])
		if word == "" {
			continue
		}
		word = strings.TrimLeft(word, "$(")
		if idx := strings.LastIndex(word, "/"); idx >= 0 {
			word = word[idx+1:]
		}
		lower := strings.ToLower(word)
		if isIgnoredCommandWord(lower) {
			continue
		}
		roots[lower] = struct{}{}
	}
	return sortedKeys(roots)
}

func isIgnoredCommandWord(word string) bool {
	switch word {
	case "sudo", "sh", "bash", "zsh", "fish", "printf", "echo", "true", "false", "then", "do", "done", "fi", "if":
		return true
	default:
		return false
	}
}

func sortedKeys(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
