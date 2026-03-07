package policy

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/richardson/lark/internal/core"
	"mvdan.cc/sh/v3/syntax"
)

var (
	safeCommands = map[string][]string{
		"cat":        nil,
		"ls":         nil,
		"pwd":        nil,
		"id":         nil,
		"whoami":     nil,
		"uname":      nil,
		"ps":         nil,
		"ss":         nil,
		"lsof":       nil,
		"df":         nil,
		"du":         nil,
		"free":       nil,
		"grep":       nil,
		"rg":         nil,
		"head":       nil,
		"tail":       nil,
		"stat":       nil,
		"file":       nil,
		"journalctl": nil,
		"find":       nil,
		"git":        {"status", "log", "show", "diff", "rev-parse", "branch"},
		"docker":     {"ps", "logs", "inspect", "images"},
		"systemctl":  {"status", "show", "is-active", "is-failed", "list-units"},
		"sed":        {"-n"},
	}
	mutatingCommands = []string{
		"apt", "apt-get", "yum", "dnf", "apk", "pacman", "brew",
		"systemctl", "service", "chmod", "chown", "useradd", "usermod",
		"mkdir", "touch", "mv", "cp", "tee", "docker", "kubectl",
	}
	highRiskCommands = []string{
		"rm", "dd", "mkfs", "shutdown", "reboot", "iptables", "ufw",
		"firewall-cmd", "killall",
	}
	systemRoots = []string{"/etc", "/usr", "/var", "/opt", "/lib", "/root", "/srv"}
)

type Classifier struct {
	protectedPaths []string
}

func NewClassifier(protectedPaths []string) *Classifier {
	return &Classifier{protectedPaths: protectedPaths}
}

func (c *Classifier) Classify(action core.Action, readOnly bool) (core.RiskReport, error) {
	switch action.Type {
	case core.ActionRunShell:
		report, err := c.classifyCommand(action.Command)
		if err != nil {
			return core.RiskReport{}, err
		}
		if readOnly && report.Level != core.RiskLow {
			report.Reason = "blocked by read-only mode"
		}
		return report, nil
	case core.ActionReadFiles, core.ActionListDir, core.ActionSearchFiles, core.ActionSemanticSearch, core.ActionWebSearch, core.ActionFetchRules:
		return core.RiskReport{
			Level:              c.actionPathRisk(action),
			TouchesSystemFiles: c.actionTouchesSystemPath(action),
			RollbackAvailable:  false,
			Reason:             "read-only retrieval tool",
		}, nil
	case core.ActionEditFile:
		level := c.actionPathRisk(action)
		if level == core.RiskLow {
			level = core.RiskMedium
		}
		return core.RiskReport{
			Level:              level,
			TouchesSystemFiles: c.actionTouchesSystemPath(action),
			RollbackAvailable:  true,
			Reason:             "file patch",
		}, nil
	case core.ActionCheckpoint:
		return core.RiskReport{
			Level:             c.actionPathRisk(action),
			RollbackAvailable: true,
			Reason:            "checkpoint snapshot",
		}, nil
	case core.ActionAskUser, core.ActionFinish:
		return core.RiskReport{
			Level:  core.RiskLow,
			Reason: "no system mutation",
		}, nil
	default:
		return core.RiskReport{
			Level:  core.RiskMedium,
			Reason: "unknown action type",
		}, nil
	}
}

func (c *Classifier) NeedsApproval(action core.Action, report core.RiskReport, mode core.ApprovalMode, autoApproveReads bool, readOnly bool) bool {
	if mode == core.ApprovalSuggest {
		return true
	}
	if readOnly && report.Level != core.RiskLow {
		return true
	}
	if report.Level == core.RiskLow && autoApproveReads {
		return false
	}
	if mode == core.ApprovalAgent && report.Level == core.RiskMedium && !readOnly {
		return false
	}
	return report.Level != core.RiskLow || mode == core.ApprovalConfirm
}

func (c *Classifier) RequiresDoubleConfirm(report core.RiskReport) bool {
	return report.Level == core.RiskHigh
}

func (c *Classifier) classifyCommand(command string) (core.RiskReport, error) {
	parser := syntax.NewParser()
	if _, err := parser.Parse(strings.NewReader(command), "command"); err != nil {
		return core.RiskReport{}, fmt.Errorf("parse shell command: %w", err)
	}

	tokens := strings.Fields(command)
	if len(tokens) == 0 {
		return core.RiskReport{Level: core.RiskLow, Reason: "empty command"}, nil
	}

	report := core.RiskReport{
		Level:  core.RiskMedium,
		Reason: "shell command",
	}
	if strings.Contains(command, ">") || strings.Contains(command, ">>") || strings.Contains(command, "| tee") {
		report.Level = core.RiskMedium
		report.Reason = "command writes redirected output"
	}
	if tokens[0] == "sudo" {
		report.NeedsSudo = true
		tokens = tokens[1:]
		if len(tokens) == 0 {
			return report, nil
		}
	}

	root := tokens[0]
	if slices.Contains(highRiskCommands, root) {
		report.Level = core.RiskHigh
		report.Reason = "destructive or system-wide command"
		return report, nil
	}

	if slices.Contains(mutatingCommands, root) {
		report.Level = core.RiskMedium
		report.Reason = "mutating system command"
		if root == "systemctl" && len(tokens) > 1 && slices.Contains(safeCommands["systemctl"], tokens[1]) {
			report.Level = core.RiskLow
			report.Reason = "service inspection"
		} else if root == "docker" && len(tokens) > 1 && slices.Contains(safeCommands["docker"], tokens[1]) {
			report.Level = core.RiskLow
			report.Reason = "container inspection"
		}
		return report, nil
	}

	if allowed, ok := safeCommands[root]; ok {
		if len(allowed) == 0 {
			if root == "find" && (strings.Contains(command, "-exec") || strings.Contains(command, "-delete")) {
				report.Level = core.RiskHigh
				report.Reason = "find command contains mutating flag"
				return report, nil
			}
			if root == "sed" && !strings.Contains(command, " -n ") && !strings.HasPrefix(command, "sed -n") {
				report.Level = core.RiskMedium
				report.Reason = "sed without -n may mutate files with -i"
				return report, nil
			}
			report.Level = core.RiskLow
			report.Reason = "read-only inspection command"
			return report, nil
		}
		if len(tokens) > 1 && slices.Contains(allowed, tokens[1]) {
			report.Level = core.RiskLow
			report.Reason = "read-only inspection command"
			return report, nil
		}
	}

	if c.touchesSystemPath(command) {
		report.TouchesSystemFiles = true
	}
	return report, nil
}

func (c *Classifier) pathRisk(path string) core.RiskLevel {
	if c.matchesProtectedPath(path) || c.touchesSystemPath(path) {
		return core.RiskMedium
	}
	return core.RiskLow
}

func (c *Classifier) touchesSystemPath(value string) bool {
	for _, root := range systemRoots {
		if strings.HasPrefix(value, root) {
			return true
		}
	}
	return c.matchesProtectedPath(value)
}

func (c *Classifier) matchesProtectedPath(value string) bool {
	for _, pattern := range c.protectedPaths {
		if strings.HasPrefix(pattern, "/") {
			matched, _ := filepath.Match(pattern, value)
			if matched || strings.HasPrefix(value, pattern) {
				return true
			}
			continue
		}
		if filepath.Base(value) == pattern {
			return true
		}
	}
	return false
}

func (c *Classifier) actionPathRisk(action core.Action) core.RiskLevel {
	paths := action.Paths
	if len(paths) == 0 && action.Path != "" {
		paths = []string{action.Path}
	}
	level := core.RiskLow
	for _, path := range paths {
		pathLevel := c.pathRisk(path)
		if pathLevel == core.RiskMedium {
			level = core.RiskMedium
		}
	}
	return level
}

func (c *Classifier) actionTouchesSystemPath(action core.Action) bool {
	paths := action.Paths
	if len(paths) == 0 && action.Path != "" {
		paths = []string{action.Path}
	}
	for _, path := range paths {
		if c.touchesSystemPath(path) {
			return true
		}
	}
	return false
}
