package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/creack/pty"
)

type RunnerServer struct {
	Store      Store
	Workspace  Workspace
	Executable string
	UIArgs     []string

	listenerMu sync.Mutex
	listener   net.Listener
	masterMu   sync.Mutex
	master     *os.File
	attachMu   sync.Mutex
	attached   net.Conn
	history    ringBuffer
}

func (r *RunnerServer) Run(ctx context.Context) error {
	if err := os.MkdirAll(filepathDir(r.Workspace.SocketPath), 0o755); err != nil {
		return err
	}
	_ = os.Remove(r.Workspace.SocketPath)
	listener, err := net.Listen("unix", r.Workspace.SocketPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(r.Workspace.SocketPath)
	}()
	r.listener = listener
	r.history.limit = 256 * 1024

	cmd := exec.CommandContext(ctx, r.Executable, r.UIArgs...)
	cmd.Dir = r.Workspace.CWD
	master, err := pty.Start(cmd)
	if err != nil {
		return err
	}
	defer master.Close()
	r.master = master

	r.Workspace.RunnerPID = os.Getpid()
	r.Workspace.State = StateLive
	if err := r.Store.Save(r.Workspace); err != nil {
		return err
	}

	childDone := make(chan error, 1)
	go func() {
		childDone <- cmd.Wait()
	}()
	go r.streamPTY()

	serveDone := make(chan error, 1)
	go func() {
		serveDone <- r.serve()
	}()

	select {
	case <-ctx.Done():
		_ = r.shutdown()
		return ctx.Err()
	case err := <-childDone:
		r.Workspace.State = StateStopped
		_ = r.Store.Save(r.Workspace)
		_ = r.shutdown()
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		return nil
	case err := <-serveDone:
		_ = r.shutdown()
		return err
	}
}

func (r *RunnerServer) serve() error {
	for {
		conn, err := r.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go r.handleConn(conn)
	}
}

func (r *RunnerServer) handleConn(conn net.Conn) {
	reader := bufio.NewReader(conn)
	var request controlRequest
	if err := json.NewDecoder(reader).Decode(&request); err != nil {
		_ = conn.Close()
		return
	}
	switch request.Type {
	case "status":
		_ = json.NewEncoder(conn).Encode(controlResponse{OK: true, State: StateLive, RunnerPID: os.Getpid()})
		_ = conn.Close()
	case "stop":
		_ = json.NewEncoder(conn).Encode(controlResponse{OK: true, State: StateStopped, RunnerPID: os.Getpid()})
		_ = conn.Close()
		_ = r.shutdown()
	case "resize":
		err := r.resize(request.Rows, request.Cols)
		response := controlResponse{OK: err == nil, State: StateLive, RunnerPID: os.Getpid()}
		if err != nil {
			response.Error = err.Error()
		}
		_ = json.NewEncoder(conn).Encode(response)
		_ = conn.Close()
	case "attach":
		r.attach(conn)
	default:
		_ = json.NewEncoder(conn).Encode(controlResponse{OK: false, Error: fmt.Sprintf("unknown control request %q", request.Type)})
		_ = conn.Close()
	}
}

func (r *RunnerServer) attach(conn net.Conn) {
	r.attachMu.Lock()
	if r.attached != nil {
		_ = r.attached.Close()
	}
	r.attached = conn
	r.attachMu.Unlock()

	r.Workspace.LastAttachedAt = nowUTC()
	r.Workspace.State = StateLive
	_ = r.Store.Save(r.Workspace)

	if snapshot := r.history.Snapshot(); len(snapshot) > 0 {
		_, _ = conn.Write(snapshot)
	}

	go func(current net.Conn) {
		defer current.Close()
		buf := make([]byte, 32*1024)
		for {
			n, err := current.Read(buf)
			if n > 0 {
				r.masterMu.Lock()
				master := r.master
				r.masterMu.Unlock()
				if master != nil {
					_, _ = master.Write(buf[:n])
				}
			}
			if err != nil {
				r.attachMu.Lock()
				if r.attached == current {
					r.attached = nil
				}
				r.attachMu.Unlock()
				return
			}
		}
	}(conn)
}

func (r *RunnerServer) streamPTY() {
	buf := make([]byte, 32*1024)
	for {
		r.masterMu.Lock()
		master := r.master
		r.masterMu.Unlock()
		if master == nil {
			return
		}
		n, err := master.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			r.history.Append(chunk)
			r.attachMu.Lock()
			current := r.attached
			r.attachMu.Unlock()
			if current != nil {
				_, _ = current.Write(chunk)
			}
		}
		if err != nil {
			return
		}
	}
}

func (r *RunnerServer) resize(rows, cols int) error {
	r.masterMu.Lock()
	defer r.masterMu.Unlock()
	if r.master == nil || rows <= 0 || cols <= 0 {
		return nil
	}
	return pty.Setsize(r.master, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

func (r *RunnerServer) shutdown() error {
	r.listenerMu.Lock()
	if r.listener != nil {
		_ = r.listener.Close()
		r.listener = nil
	}
	r.listenerMu.Unlock()

	r.attachMu.Lock()
	if r.attached != nil {
		_ = r.attached.Close()
		r.attached = nil
	}
	r.attachMu.Unlock()

	r.masterMu.Lock()
	if r.master != nil {
		_ = r.master.Close()
		r.master = nil
	}
	r.masterMu.Unlock()
	r.Workspace.State = StateStopped
	return r.Store.Save(r.Workspace)
}

func filepathDir(path string) string {
	dir := "."
	if value := filepath.Dir(path); value != "" {
		dir = value
	}
	return dir
}

func nowUTC() time.Time {
	return time.Now().UTC()
}
