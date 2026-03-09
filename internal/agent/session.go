package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"
)

const (
	StateStarting = "starting"
	StateLive     = "live"
	StateStopped  = "stopped"
	StateStale    = "stale"
)

type controlRequest struct {
	Type string `json:"type"`
	Rows int    `json:"rows,omitempty"`
	Cols int    `json:"cols,omitempty"`
}

type controlResponse struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	State     string `json:"state,omitempty"`
	RunnerPID int    `json:"runner_pid,omitempty"`
}

type Launcher interface {
	Launch(workspace Workspace, executable string, args []string) (int, error)
}

type ControlClient interface {
	Status(ctx context.Context, socketPath string) (controlResponse, error)
	Stop(ctx context.Context, socketPath string) error
	Resize(ctx context.Context, socketPath string, rows, cols int) error
	Attach(socketPath string) (net.Conn, error)
}

type SessionManager struct {
	Store        Store
	Launcher     Launcher
	Control      ControlClient
	StartTimeout time.Duration
	PollInterval time.Duration
}

func (m SessionManager) launcher() Launcher {
	if m.Launcher != nil {
		return m.Launcher
	}
	return detachedLauncher{}
}

func (m SessionManager) control() ControlClient {
	if m.Control != nil {
		return m.Control
	}
	return socketControlClient{}
}

func (m SessionManager) timeout() time.Duration {
	if m.StartTimeout > 0 {
		return m.StartTimeout
	}
	return 5 * time.Second
}

func (m SessionManager) pollInterval() time.Duration {
	if m.PollInterval > 0 {
		return m.PollInterval
	}
	return 100 * time.Millisecond
}

func (m SessionManager) Inspect(ctx context.Context, workspace Workspace) (Workspace, error) {
	if strings.TrimSpace(workspace.SocketPath) == "" {
		workspace.State = StateStale
		return workspace, nil
	}
	status, err := m.control().Status(ctx, workspace.SocketPath)
	if err == nil && status.OK {
		workspace.State = firstState(status.State, StateLive)
		if status.RunnerPID > 0 {
			workspace.RunnerPID = status.RunnerPID
		}
		return workspace, nil
	}
	if workspace.RunnerPID > 0 && processAlive(workspace.RunnerPID) {
		workspace.State = StateStarting
		return workspace, nil
	}
	workspace.State = StateStale
	return workspace, nil
}

func (m SessionManager) EnsureWorkspace(ctx context.Context, workspace Workspace, executable string, args []string) (Workspace, error) {
	inspected, err := m.Inspect(ctx, workspace)
	if err != nil {
		return workspace, err
	}
	if inspected.State == StateLive {
		return inspected, nil
	}
	_ = os.Remove(inspected.SocketPath)
	pid, err := m.launcher().Launch(inspected, executable, args)
	if err != nil {
		return workspace, err
	}
	inspected.RunnerPID = pid
	inspected.State = StateStarting
	if err := m.Store.Save(inspected); err != nil {
		return workspace, err
	}

	deadline := time.Now().Add(m.timeout())
	for {
		status, statusErr := m.control().Status(ctx, inspected.SocketPath)
		if statusErr == nil && status.OK {
			inspected.State = firstState(status.State, StateLive)
			if status.RunnerPID > 0 {
				inspected.RunnerPID = status.RunnerPID
			}
			if err := m.Store.Save(inspected); err != nil {
				return workspace, err
			}
			return inspected, nil
		}
		if time.Now().After(deadline) {
			return workspace, fmt.Errorf("timed out waiting for agent session %q to start", inspected.Key)
		}
		select {
		case <-ctx.Done():
			return workspace, ctx.Err()
		case <-time.After(m.pollInterval()):
		}
	}
}

func (m SessionManager) Attach(ctx context.Context, workspace Workspace) error {
	workspace, err := m.Inspect(ctx, workspace)
	if err != nil {
		return err
	}
	if workspace.State != StateLive {
		return fmt.Errorf("agent workspace %q is stale; run `lk agent` again to start a new session", workspace.Key)
	}
	conn, err := m.control().Attach(workspace.SocketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := io.WriteString(conn, `{"type":"attach"}`+"\n"); err != nil {
		return err
	}
	if err := resizeRemote(ctx, m.control(), workspace.SocketPath); err != nil {
		return err
	}

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("agent attach requires an interactive terminal")
	}
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer term.Restore(fd, oldState)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-sigCh:
				_ = resizeRemote(ctx, m.control(), workspace.SocketPath)
			}
		}
	}()

	copyErr := make(chan error, 1)
	go func() {
		_, copyErrValue := io.Copy(conn, os.Stdin)
		copyErr <- copyErrValue
	}()
	_, stdoutErr := io.Copy(os.Stdout, conn)
	stdinErr := <-copyErr
	if stdoutErr != nil && !isIgnorableAttachIOError(stdoutErr) {
		return stdoutErr
	}
	if stdinErr != nil && !isIgnorableAttachIOError(stdinErr) {
		return stdinErr
	}
	return nil
}

func (m SessionManager) Stop(ctx context.Context, workspace Workspace) error {
	if err := m.control().Stop(ctx, workspace.SocketPath); err != nil && workspace.RunnerPID == 0 {
		return err
	}
	if workspace.RunnerPID > 0 && processAlive(workspace.RunnerPID) {
		_ = syscall.Kill(-workspace.RunnerPID, syscall.SIGTERM)
		deadline := time.Now().Add(2 * time.Second)
		for processAlive(workspace.RunnerPID) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if processAlive(workspace.RunnerPID) {
			_ = syscall.Kill(-workspace.RunnerPID, syscall.SIGKILL)
		}
	}
	_ = os.Remove(workspace.SocketPath)
	return nil
}

type detachedLauncher struct{}

func (detachedLauncher) Launch(workspace Workspace, executable string, args []string) (int, error) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer devNull.Close()

	cmd := exec.Command(executable, args...)
	cmd.Dir = workspace.CWD
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

type socketControlClient struct{}

func (socketControlClient) Status(ctx context.Context, socketPath string) (controlResponse, error) {
	var response controlResponse
	if err := callControl(ctx, socketPath, controlRequest{Type: "status"}, &response); err != nil {
		return controlResponse{}, err
	}
	if !response.OK {
		return response, errors.New(firstState(response.Error, "agent session is unavailable"))
	}
	return response, nil
}

func (socketControlClient) Stop(ctx context.Context, socketPath string) error {
	var response controlResponse
	if err := callControl(ctx, socketPath, controlRequest{Type: "stop"}, &response); err != nil {
		return err
	}
	if !response.OK {
		return errors.New(firstState(response.Error, "failed to stop agent session"))
	}
	return nil
}

func (socketControlClient) Resize(ctx context.Context, socketPath string, rows, cols int) error {
	var response controlResponse
	if err := callControl(ctx, socketPath, controlRequest{Type: "resize", Rows: rows, Cols: cols}, &response); err != nil {
		return err
	}
	if !response.OK {
		return errors.New(firstState(response.Error, "failed to resize agent session"))
	}
	return nil
}

func (socketControlClient) Attach(socketPath string) (net.Conn, error) {
	return net.Dial("unix", socketPath)
}

func callControl(ctx context.Context, socketPath string, request controlRequest, response *controlResponse) error {
	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return err
	}
	return json.NewDecoder(bufio.NewReader(conn)).Decode(response)
}

func resizeRemote(ctx context.Context, control ControlClient, socketPath string) error {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return nil
	}
	return control.Resize(ctx, socketPath, rows, cols)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func firstState(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func isIgnorableAttachIOError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) || errors.Is(err, fs.ErrClosed) || errors.Is(err, os.ErrClosed) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "file already closed") ||
		strings.Contains(message, "broken pipe") ||
		strings.Contains(message, "use of closed network connection")
}

type ringBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func (b *ringBuffer) Append(chunk []byte) {
	if len(chunk) == 0 || b.limit <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, chunk...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
}

func (b *ringBuffer) Snapshot() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data...)
}

func sessionSocketPath(dir, key string) string {
	return filepath.Join(dir, key+".sock")
}
