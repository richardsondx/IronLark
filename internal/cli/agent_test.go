package cli

import (
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
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
