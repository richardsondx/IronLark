package render

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/richardsondx/IronLark/internal/checkpoints"
	ctxpkg "github.com/richardsondx/IronLark/internal/context"
	"github.com/richardsondx/IronLark/internal/core"
	"github.com/richardsondx/IronLark/internal/patches"
	"github.com/richardsondx/IronLark/internal/sessions"
	"golang.org/x/term"
)

type Renderer struct {
	In    *bufio.Reader
	Out   io.Writer
	Err   io.Writer
	JSON  bool
	Color bool
}

func New(in io.Reader, out, err io.Writer, jsonOutput bool, colorMode string) *Renderer {
	return &Renderer{
		In:    bufio.NewReader(in),
		Out:   out,
		Err:   err,
		JSON:  jsonOutput,
		Color: shouldUseColor(out, jsonOutput, colorMode),
	}
}

func (r *Renderer) Snapshot(snapshot ctxpkg.Snapshot) error {
	if r.JSON {
		return r.encode(snapshot)
	}
	fmt.Fprintln(r.Out, "Inspecting:")
	for key, value := range snapshot.System {
		fmt.Fprintf(r.Out, "- %s: %v\n", key, value)
	}
	if snapshot.Repo.Detected {
		fmt.Fprintf(r.Out, "\nRepo: %s\n", snapshot.Repo.Root)
		if snapshot.Repo.GitStatus != "" {
			fmt.Fprintf(r.Out, "%s\n", snapshot.Repo.GitStatus)
		}
		if len(snapshot.Repo.DetectedStack) > 0 {
			fmt.Fprintf(r.Out, "Stack: %s\n", strings.Join(snapshot.Repo.DetectedStack, ", "))
		}
	}
	return nil
}

func (r *Renderer) Response(response core.LLMResponse) error {
	if r.JSON {
		return r.encode(response)
	}
	fmt.Fprintf(r.Out, "%s: %s\n", r.label("Summary"), response.Summary)
	if len(response.Findings) > 0 {
		fmt.Fprintf(r.Out, "\n%s:\n", r.heading("Findings"))
		for idx, finding := range response.Findings {
			fmt.Fprintf(r.Out, "%s %s\n", r.accent(fmt.Sprintf("%d.", idx+1)), finding)
		}
	}
	return nil
}

func (r *Renderer) PlannedActions(actions []core.Action, previews []core.RiskReport) {
	if len(actions) == 0 {
		return
	}
	fmt.Fprintf(r.Out, "\n%s:\n", r.heading("Proposed actions"))
	for idx, action := range actions {
		report := core.RiskReport{}
		if idx < len(previews) {
			report = previews[idx]
		}
		target := action.Command
		if target == "" {
			target = action.Path
		}
		if target == "" {
			target = action.Query
		}
		if target == "" && len(action.Paths) > 0 {
			target = strings.Join(action.Paths, ", ")
		}
		fmt.Fprintf(r.Out, "%s %s\n", r.actionTag(string(action.Type)), target)
		fmt.Fprintf(r.Out, "  %s: %s | %s: %t | %s: %t | %s: %t\n",
			r.label("Risk"), r.riskLevel(report.Level),
			r.label("Needs sudo"), report.NeedsSudo,
			r.label("Touches system files"), report.TouchesSystemFiles,
			r.label("Rollback available"), report.RollbackAvailable)
		if action.Reason != "" {
			fmt.Fprintf(r.Out, "  %s: %s\n", r.label("Why"), action.Reason)
		}
		if action.Type == core.ActionEditFile && action.PatchUnifiedDiff != "" {
			fmt.Fprintf(r.Out, "  %s:\n", r.label("Diff"))
			for _, line := range strings.Split(strings.TrimSpace(action.PatchUnifiedDiff), "\n") {
				fmt.Fprintf(r.Out, "    %s\n", r.formatDiffLine(line))
			}
		}
	}
}

func (r *Renderer) ActionProgress(action core.Action) {
	if r.JSON {
		return
	}
	target := action.Title
	if target == "" {
		target = action.Command
	}
	if target == "" {
		target = action.Path
	}
	if target == "" {
		target = action.Query
	}
	if target == "" {
		target = string(action.Type)
	}
	fmt.Fprintf(r.Out, "%s %s\n", r.actionTag(string(action.Type)), target)
}

func (r *Renderer) ApprovalPrompt(action core.Action, report core.RiskReport) {
	if r.JSON {
		return
	}
	fmt.Fprintf(r.Out, "\n%s:\n", r.heading("Approval needed"))
	target := action.Command
	if target == "" {
		target = action.Path
	}
	if target == "" {
		target = action.Query
	}
	if target == "" {
		target = action.Title
	}
	fmt.Fprintf(r.Out, "%s %s\n", r.actionTag(string(action.Type)), target)
	fmt.Fprintf(r.Out, "  %s: %s | %s: %t | %s: %t | %s: %t\n",
		r.label("Risk"), r.riskLevel(report.Level),
		r.label("Needs sudo"), report.NeedsSudo,
		r.label("Touches system files"), report.TouchesSystemFiles,
		r.label("Rollback available"), report.RollbackAvailable)
	if action.Reason != "" {
		fmt.Fprintf(r.Out, "  %s: %s\n", r.label("Why"), action.Reason)
	}
	if action.Type == core.ActionEditFile && action.PatchUnifiedDiff != "" {
		fmt.Fprintf(r.Out, "  %s:\n", r.label("Diff"))
		for _, line := range strings.Split(strings.TrimSpace(action.PatchUnifiedDiff), "\n") {
			fmt.Fprintf(r.Out, "    %s\n", r.formatDiffLine(line))
		}
	}
}

func (r *Renderer) Result(result core.ActionResult) {
	if r.JSON {
		_ = r.encode(result)
		return
	}
	status := "ok"
	if result.Error != "" {
		status = "error"
	}
	if result.Skipped {
		status = "skipped"
	}
	fmt.Fprintf(r.Out, "\n%s %s\n", r.resultTag(status), result.Action.Title)
	if result.Summary != "" {
		fmt.Fprintf(r.Out, "%s: %s\n", r.label("Summary"), result.Summary)
	}
	if result.Stdout != "" {
		r.outputBlock("Output", result.Stdout, ansiGray)
	}
	if result.Stderr != "" {
		r.outputBlock("Stderr", result.Stderr, ansiRed)
	}
	if result.Error != "" {
		fmt.Fprintf(r.Out, "%s: %s\n", r.label("Error"), strings.TrimSpace(result.Error))
	}
	if result.PatchID != "" {
		fmt.Fprintf(r.Out, "%s: %s\n", r.label("Patch ID"), result.PatchID)
	}
	if result.CheckpointID != "" {
		fmt.Fprintf(r.Out, "%s: %s\n", r.label("Checkpoint ID"), result.CheckpointID)
	}
}

func (r *Renderer) Sessions(records []sessions.Record) error {
	if r.JSON {
		return r.encode(records)
	}
	for _, record := range records {
		fmt.Fprintf(r.Out, "%s  %s  %s\n", record.ID, record.StartedAt.Format("2006-01-02 15:04:05"), record.Summary)
	}
	return nil
}

func (r *Renderer) Patches(records []patches.Record) error {
	if r.JSON {
		return r.encode(records)
	}
	for _, record := range records {
		fmt.Fprintf(r.Out, "%s  %s  %s\n", record.ID, record.CreatedAt.Format("2006-01-02 15:04:05"), record.Path)
	}
	return nil
}

func (r *Renderer) Checkpoints(records []checkpoints.Record) error {
	if r.JSON {
		return r.encode(records)
	}
	for _, record := range records {
		fmt.Fprintf(r.Out, "%s  %s  files=%d  %s\n", record.ID, record.CreatedAt.Format("2006-01-02 15:04:05"), len(record.Files), record.Reason)
	}
	return nil
}

func (r *Renderer) PromptChoice() (string, error) {
	fmt.Fprintf(r.Out, "\n%s:\n", r.heading("Choose"))
	fmt.Fprintf(r.Out, "%s %s\n", r.accent("1."), "Approve all")
	fmt.Fprintf(r.Out, "%s %s\n", r.accent("2."), "Approve step by step")
	fmt.Fprintf(r.Out, "%s %s\n", r.accent("3."), "Show commands only")
	fmt.Fprintf(r.Out, "%s %s\n", r.accent("4."), "Cancel")
	return r.readLine(r.prompt("> "))
}

func (r *Renderer) PromptApprovalChoice() (string, error) {
	fmt.Fprintf(r.Out, "\n%s:\n", r.heading("Choose"))
	fmt.Fprintf(r.Out, "%s %s\n", r.accent("1."), "Allow once")
	fmt.Fprintf(r.Out, "%s %s\n", r.accent("2."), "Always allow on this machine")
	fmt.Fprintf(r.Out, "%s %s\n", r.accent("3."), "Deny once")
	fmt.Fprintf(r.Out, "%s %s\n", r.accent("4."), "Cancel")
	return r.readLine(r.prompt("> "))
}

func (r *Renderer) Confirm(label string, double bool) (bool, error) {
	prompt := fmt.Sprintf("Run %s? [y/N] ", label)
	if double {
		prompt = fmt.Sprintf("High risk action. Type YES to run %s: ", label)
	}
	answer, err := r.readLine(prompt)
	if err != nil {
		return false, err
	}
	answer = strings.TrimSpace(answer)
	if double {
		return answer == "YES", nil
	}
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"), nil
}

func (r *Renderer) ReadPrompt(prefix string) (string, error) {
	return r.readLine(prefix)
}

func (r *Renderer) Message(text string) {
	fmt.Fprintln(r.Out, text)
}

func (r *Renderer) MessageJSON(v any) error {
	return r.encode(v)
}

func (r *Renderer) encode(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(r.Out, string(data))
	return err
}

func (r *Renderer) readLine(prefix string) (string, error) {
	fmt.Fprint(r.Out, prefix)
	line, err := r.In.ReadString('\n')
	if err != nil {
		if err == io.EOF {
			return strings.TrimSpace(line), nil
		}
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (r *Renderer) formatDiffLine(line string) string {
	if !r.Color {
		return line
	}
	switch {
	case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		return colorize(line, ansiBold)
	case strings.HasPrefix(line, "@@"):
		return colorize(line, ansiCyan)
	case strings.HasPrefix(line, "+"):
		return colorize(line, ansiGreen)
	case strings.HasPrefix(line, "-"):
		return colorize(line, ansiRed)
	default:
		return line
	}
}

func (r *Renderer) outputBlock(title, body, lineColor string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	fmt.Fprintf(r.Out, "%s:\n", r.label(title))
	for _, line := range strings.Split(body, "\n") {
		fmt.Fprintf(r.Out, "  %s %s\n", r.outputPrefix(lineColor), r.outputLine(line, lineColor))
	}
}

func (r *Renderer) heading(text string) string {
	return r.style(text, ansiBold, ansiCyan)
}

func (r *Renderer) label(text string) string {
	return r.style(text, ansiBold)
}

func (r *Renderer) accent(text string) string {
	return r.style(text, ansiCyan)
}

func (r *Renderer) prompt(text string) string {
	return r.style(text, ansiBold, ansiCyan)
}

func (r *Renderer) actionTag(text string) string {
	return r.style("["+text+"]", ansiBold, ansiBlue)
}

func (r *Renderer) resultTag(status string) string {
	code := ansiBlue
	switch status {
	case "ok":
		code = ansiGreen
	case "error":
		code = ansiRed
	case "skipped":
		code = ansiYellow
	}
	return r.style("["+status+"]", ansiBold, code)
}

func (r *Renderer) riskLevel(level core.RiskLevel) string {
	code := ansiBlue
	switch level {
	case core.RiskLow:
		code = ansiGreen
	case core.RiskMedium:
		code = ansiYellow
	case core.RiskHigh:
		code = ansiRed
	}
	return r.style(string(level), ansiBold, code)
}

func (r *Renderer) outputPrefix(lineColor string) string {
	return r.style("│", ansiBold, lineColor)
}

func (r *Renderer) outputLine(text, lineColor string) string {
	return r.style(text, lineColor)
}

func (r *Renderer) style(text string, codes ...string) string {
	if !r.Color {
		return text
	}
	return strings.Join(codes, "") + text + ansiReset
}

func shouldUseColor(out io.Writer, jsonOutput bool, colorMode string) bool {
	switch strings.TrimSpace(strings.ToLower(colorMode)) {
	case "always":
		return !jsonOutput
	case "never":
		return false
	}
	if forceColorEnabled() {
		return !jsonOutput
	}
	if jsonOutput || os.Getenv("NO_COLOR") != "" {
		return false
	}
	if termEnv := strings.TrimSpace(os.Getenv("TERM")); termEnv == "" || termEnv == "dumb" {
		return false
	}
	file, ok := out.(*os.File)
	if ok && term.IsTerminal(int(file.Fd())) {
		return true
	}
	return term.IsTerminal(int(os.Stdout.Fd())) || term.IsTerminal(int(os.Stderr.Fd()))
}

func colorize(value, code string) string {
	return code + value + ansiReset
}

func forceColorEnabled() bool {
	for _, key := range []string{"CLICOLOR_FORCE", "FORCE_COLOR"} {
		value := strings.TrimSpace(os.Getenv(key))
		if value != "" && value != "0" {
			return true
		}
	}
	return false
}

const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiBlue   = "\033[34m"
	ansiGray   = "\033[90m"
	ansiCyan   = "\033[36m"
)
