package cli

import (
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"

	"github.com/richardsondx/IronLark/internal/agent"
)

func TestRootCommandSilencesUsageAndErrors(t *testing.T) {
	cmd := NewRootCommand()
	if !cmd.SilenceUsage {
		t.Fatalf("expected SilenceUsage to be enabled")
	}
	if !cmd.SilenceErrors {
		t.Fatalf("expected SilenceErrors to be enabled")
	}
}

func TestRecoverableAgentAttachError(t *testing.T) {
	cases := []error{
		syscall.EPIPE,
		io.EOF,
		net.ErrClosed,
		errors.New("write unix ->/tmp/lark.sock: write: broken pipe"),
		errors.New("read unix /tmp/lark.sock: connection reset by peer"),
	}
	for _, err := range cases {
		if !isRecoverableAgentAttachError(err) {
			t.Fatalf("expected %v to be recoverable", err)
		}
	}
	if isRecoverableAgentAttachError(errors.New("permission denied")) {
		t.Fatalf("expected unrelated error to remain fatal")
	}
}

func TestAgentRunnerCommandKeepsInitialPromptOutOfRunnerArgs(t *testing.T) {
	flags := &rootFlags{
		provider: "openai",
		model:    "gpt-5",
		color:    "always",
	}
	workspace := agent.Workspace{
		Key:      "abc123",
		ThreadID: "thread-1",
	}

	_, args, err := agentRunnerCommand(flags, workspace, true)
	if err != nil {
		t.Fatalf("agentRunnerCommand() error = %v", err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--prompt") || strings.Contains(joined, "check docker") {
		t.Fatalf("expected initial prompt to be injected after attach, got args %#v", args)
	}
}
