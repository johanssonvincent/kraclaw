package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// StdioTransport connects to an MCP server via stdio.
type StdioTransport struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderr    io.ReadCloser
	log       Logger
	closeOnce sync.Once
	closed    bool
}

// NewStdioTransport creates a transport that runs an MCP server as a subprocess.
func NewStdioTransport(command string, args []string, env []string, log Logger) *StdioTransport {
	// Default to inheriting parent environment if none specified.
	if env == nil {
		env = os.Environ()
	}

	return &StdioTransport{
		cmd: &exec.Cmd{
			Path: command,
			Args: append([]string{command}, args...),
			Env:  env,
		},
		log: log,
	}
}

// Start launches the subprocess and opens stdio pipes.
func (t *StdioTransport) Start(ctx context.Context) error {
	var err error

	t.stdin, err = t.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdio: stdin pipe: %w", err)
	}

	t.stdout, err = t.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdio: stdout pipe: %w", err)
	}

	t.stderr, err = t.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stdio: stderr pipe: %w", err)
	}

	if err := t.cmd.Start(); err != nil {
		return fmt.Errorf("stdio: start command %s: %w", t.cmd.Path, err)
	}

	t.log.Debug("stdio transport started", "command", t.cmd.Path, "args", t.cmd.Args, "pid", t.cmd.Process.Pid)

	return nil
}

// Close terminates the subprocess.
func (t *StdioTransport) Close() error {
	var errs []error

	t.closeOnce.Do(func() {
		t.closed = true

		// Close stdin to signal EOF.
		if t.stdin != nil {
			errs = append(errs, t.stdin.Close())
		}

		// Wait for process to exit with timeout.
		done := make(chan error, 1)

		go func() {
			done <- t.cmd.Wait()
		}()

		select {
		case err := <-done:
			if err != nil {
				errs = append(errs, fmt.Errorf("stdio: wait: %w", err))
			}
		case <-time.After(5 * time.Second):
			// Force kill.
			if t.cmd.Process != nil {
				t.cmd.Process.Kill()
			}

			<-done
		}
	})

	if len(errs) > 0 {
		return fmt.Errorf("stdio: close: %w", errs[0])
	}

	return nil
}

// Send writes a JSON-RPC message to the server's stdin.
func (t *StdioTransport) Send(ctx context.Context, msg json.RawMessage) error {
	if t.closed {
		return fmt.Errorf("stdio: transport closed")
	}

	// Add newline delimiter.
	data := append(msg, '\n')

	if _, err := t.stdin.Write(data); err != nil {
		return fmt.Errorf("stdio: write: %w", err)
	}

	return nil
}

// Receive reads a JSON-RPC message from the server's stdout.
func (t *StdioTransport) Receive(ctx context.Context) (json.RawMessage, error) {
	if t.closed {
		return nil, fmt.Errorf("stdio: transport closed")
	}

	reader := &lineReader{r: t.stdout}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		line, err := reader.ReadLine()
		if err != nil {
			return nil, fmt.Errorf("stdio: read: %w", err)
		}

		// Skip empty lines.
		if len(line) == 0 {
			continue
		}

		// Validate JSON.
		var _ json.RawMessage
		if !json.Valid(line) {
			t.log.Debug("stdio: non-JSON line, skipping", "line", string(line))
			continue
		}

		return json.RawMessage(line), nil
	}
}

// lineReader provides line-by-line reading with context support.
type lineReader struct {
	r   io.Reader
	buf []byte
}

func (lr *lineReader) ReadLine() ([]byte, error) {
	for {
		i := indexOfByte(lr.buf, '\n')
		if i >= 0 {
			line := lr.buf[:i]
			lr.buf = lr.buf[i+1:]
			return line, nil
		}

		if len(lr.buf) >= 1024*1024 {
			// Line too long.
			return nil, fmt.Errorf("stdio: line exceeds 1MB")
		}

		// Grow buffer if needed.
		if len(lr.buf) == cap(lr.buf) {
			newCap := cap(lr.buf)
			if newCap == 0 {
				newCap = 4096
			} else {
				newCap *= 2
			}
			newBuf := make([]byte, len(lr.buf), newCap)
			copy(newBuf, lr.buf)
			lr.buf = newBuf
		}

		n, err := lr.r.Read(lr.buf[len(lr.buf):cap(lr.buf)])
		if n > 0 {
			lr.buf = lr.buf[:len(lr.buf)+n]
		}

		if err != nil {
			return lr.buf, err
		}
	}
}

func indexOfByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}

	return -1
}
