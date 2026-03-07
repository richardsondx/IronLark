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

func New(in io.Reader, out, err io.Writer, jsonOutput bool) *Renderer {
	return &Renderer{
		In:    bufio.NewReader(in),
		Out:   out,
		Err:   err,
		JSON:  jsonOutput,
		Color: shouldUseColor(out, jsonOutput),
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
	fmt.Fprintf(r.Out, "Summary: %s\n", response.Summary)
	if len(response.Findings) > 0 {
		fmt.Fprintln(r.Out, "\nFindings:")
		for idx, finding := range response.Findings {
			fmt.Fprintf(r.Out, "%d. %s\n", idx+1, finding)
		}
	}
	return nil
}

func (r *Renderer) PlannedActions(actions []core.Action, previews []core.RiskReport) {
	if len(actions) == 0 {
		return
	}
	fmt.Fprintln(r.Out, "\nProposed actions:")
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
		fmt.Fprintf(r.Out, "[%s] %s\n", action.Type, target)
		fmt.Fprintf(r.Out, "  Risk: %s | Needs sudo: %t | Touches system files: %t | Rollback available: %t\n",
			report.Level, report.NeedsSudo, report.TouchesSystemFiles, report.RollbackAvailable)
		if action.Reason != "" {
			fmt.Fprintf(r.Out, "  Why: %s\n", action.Reason)
		}
		if action.Type == core.ActionEditFile && action.PatchUnifiedDiff != "" {
			fmt.Fprintln(r.Out, "  Diff:")
			for _, line := range strings.Split(strings.TrimSpace(action.PatchUnifiedDiff), "\n") {
				fmt.Fprintf(r.Out, "    %s\n", r.formatDiffLine(line))
			}
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
	fmt.Fprintf(r.Out, "\n[%s] %s\n", status, result.Action.Title)
	if result.Summary != "" {
		fmt.Fprintf(r.Out, "%s\n", result.Summary)
	}
	if result.Stdout != "" {
		fmt.Fprintf(r.Out, "%s\n", strings.TrimSpace(result.Stdout))
	}
	if result.Stderr != "" {
		fmt.Fprintf(r.Out, "%s\n", strings.TrimSpace(result.Stderr))
	}
	if result.Error != "" {
		fmt.Fprintf(r.Out, "Error: %s\n", strings.TrimSpace(result.Error))
	}
	if result.PatchID != "" {
		fmt.Fprintf(r.Out, "Patch ID: %s\n", result.PatchID)
	}
	if result.CheckpointID != "" {
		fmt.Fprintf(r.Out, "Checkpoint ID: %s\n", result.CheckpointID)
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
	fmt.Fprintln(r.Out, "\nChoose:")
	fmt.Fprintln(r.Out, "1. Approve all")
	fmt.Fprintln(r.Out, "2. Approve step by step")
	fmt.Fprintln(r.Out, "3. Show commands only")
	fmt.Fprintln(r.Out, "4. Cancel")
	return r.readLine("> ")
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

func shouldUseColor(out io.Writer, jsonOutput bool) bool {
	if jsonOutput || os.Getenv("NO_COLOR") != "" {
		return false
	}
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

func colorize(value, code string) string {
	return code + value + ansiReset
}

const (
	ansiReset = "\033[0m"
	ansiBold  = "\033[1m"
	ansiRed   = "\033[31m"
	ansiGreen = "\033[32m"
	ansiCyan  = "\033[36m"
)
