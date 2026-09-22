package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/domain"
	"github.com/devthefuture-org/quotadeck/internal/runner"
)

// conn frames JSON-RPC over a `codex app-server --stdio` child process.
//
// Four contracts hold it together, each one a failure mode of the one-shot
// path this replaces:
//
//   - An answer is delivered to its caller whether it arrives after the waiter
//     is registered or before it. No timing of the reader decides whether a
//     response survives.
//   - A single goroutine reads stdout. Notifications are routed without ever
//     blocking that reader: a caller that stops draining them must not stall
//     every pending response.
//   - Writes are serialized, so two callers cannot interleave their JSON.
//   - Closing wakes every waiter, and registrations made afterwards fail at
//     once. No call is left hanging on a dead child.
type conn struct {
	command *exec.Cmd
	stdin   writeCloser
	stdout  readCloser
	stderr  *syncBuffer
	home    string

	reap        func()
	stopClosing func() bool

	writeMu sync.Mutex
	encoder *json.Encoder

	mu      sync.Mutex
	waiters map[int]chan rpcResult
	pending map[int]rpcResult
	failure error

	notifs chan notification
	done   chan struct{}
}

type writeCloser interface {
	Write([]byte) (int, error)
	Close() error
}

type readCloser interface {
	Read([]byte) (int, error)
	Close() error
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

type notification struct {
	Method string
	Params json.RawMessage
}

// notificationBuffer holds notifications for a caller that is busy. Codex emits
// unsolicited status messages (remoteControl/status/changed) on every session,
// so a caller that never reads them is the normal case, not an error.
const notificationBuffer = 32

func openConn(ctx context.Context, binary, home string) (*conn, error) {
	childContext, cancel := context.WithCancel(ctx)
	resolved, err := runner.LookPath(binary)
	if err != nil {
		cancel()
		return nil, &domain.CodedError{Code: "codex_not_found", Err: errors.New("codex executable not found")}
	}
	command := exec.CommandContext(childContext, resolved, "app-server", "--stdio")
	command.Env = envWith(runner.CommandEnvironment(), "CODEX_HOME", home)
	// npm launchers spawn the real Codex binary. Cancel that whole process
	// group so a surviving child cannot hold the RPC pipes open indefinitely.
	configureProcessGroup(command)
	command.WaitDelay = time.Second
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, &domain.CodedError{Code: "codex_start_failed", Err: errors.New("open Codex stdin")}
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		_ = stdin.Close()
		return nil, &domain.CodedError{Code: "codex_start_failed", Err: errors.New("open Codex stdout")}
	}
	stderr := &syncBuffer{}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		cancel()
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, &domain.CodedError{Code: "codex_start_failed", Err: errors.New("start Codex app-server")}
	}
	c := &conn{
		command: command, stdin: stdin, stdout: stdout, stderr: stderr, home: home,
		encoder: json.NewEncoder(stdin),
		waiters: make(map[int]chan rpcResult),
		pending: make(map[int]rpcResult),
		notifs:  make(chan notification, notificationBuffer),
		done:    make(chan struct{}),
	}
	// exec copies the child's stderr from a goroutine of its own, and only Wait
	// guarantees that copy is complete. Reading the buffer before then races the
	// copier and yields whatever happened to have arrived.
	var reaped sync.Once
	c.reap = func() {
		reaped.Do(func() {
			cancel()
			_ = stdin.Close()
			_ = stdout.Close()
			_ = command.Wait()
		})
	}
	// Stdio reads/writes do not observe context cancellation themselves.
	// Close them explicitly even if a descendant escaped the process group.
	c.stopClosing = context.AfterFunc(childContext, func() {
		_ = stdin.Close()
		_ = stdout.Close()
	})
	go c.read()
	return c, nil
}

// expect returns the channel carrying the answer for id. Registering before
// writing is the rule, but the answer is honoured even when it arrives first:
// nothing about the reader's timing may decide whether a response is kept.
func (c *conn) expect(id int) <-chan rpcResult {
	channel := make(chan rpcResult, 1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if result, ok := c.pending[id]; ok {
		delete(c.pending, id)
		channel <- result
		return channel
	}
	if c.failure != nil {
		channel <- rpcResult{err: c.failure}
		return channel
	}
	c.waiters[id] = channel
	return channel
}

// send writes one message. Writes are serialized: two goroutines encoding into
// the same pipe would interleave their JSON.
func (c *conn) send(message any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.encoder.Encode(message)
}

// call registers, sends and waits, which is the ordering every request needs.
func (c *conn) call(id int, method string, params any) (json.RawMessage, error) {
	waiter := c.expect(id)
	message := map[string]any{"id": id, "method": method}
	if params != nil {
		message["params"] = params
	}
	if err := c.send(message); err != nil {
		c.resolve(id, rpcResult{err: fmt.Errorf("write Codex %s request", method)})
	}
	result := <-waiter
	return result.result, result.err
}

func (c *conn) notifications() <-chan notification { return c.notifs }

// stderrTail reaps the child first: only then is exec's stderr copy complete.
func (c *conn) stderrTail() string {
	c.reap()
	return c.stderr.lastLine()
}

func (c *conn) close() {
	if c.stopClosing != nil {
		c.stopClosing()
	}
	c.reap()
	<-c.done
}

func (c *conn) read() {
	defer close(c.done)
	// This is the only sender, so closing here releases any consumer ranging
	// over the notifications instead of stranding it on a dead connection.
	defer close(c.notifs)
	scanner := bufio.NewScanner(c.stdout)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		var envelope struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			continue
		}
		if envelope.ID == nil {
			// Never block the reader on a caller that is not draining.
			select {
			case c.notifs <- notification{Method: envelope.Method, Params: envelope.Params}:
			default:
			}
			continue
		}
		result := rpcResult{result: envelope.Result}
		if envelope.Error != nil {
			result = rpcResult{err: &rpcError{Code: envelope.Error.Code, Message: envelope.Error.Message}}
		}
		c.resolve(*envelope.ID, result)
	}
	err := errors.New("Codex app-server closed before responding")
	if scanErr := scanner.Err(); scanErr != nil {
		err = fmt.Errorf("read Codex app-server response: %w", scanErr)
	}
	c.fail(err)
}

func (c *conn) resolve(id int, result rpcResult) {
	c.mu.Lock()
	channel, ok := c.waiters[id]
	if ok {
		delete(c.waiters, id)
	} else {
		// The answer outran its registration. Hold it for the caller instead of
		// dropping it, which would leave that caller waiting on a reply the
		// child already sent.
		c.pending[id] = result
	}
	c.mu.Unlock()
	if ok {
		channel <- result
	}
}

// fail wakes every outstanding waiter and makes later registrations fail the
// same way, so no caller can block on a stream that is already finished.
func (c *conn) fail(err error) {
	c.mu.Lock()
	if c.failure == nil {
		c.failure = err
	}
	pending := c.waiters
	c.waiters = make(map[int]chan rpcResult)
	c.mu.Unlock()
	for _, channel := range pending {
		channel <- rpcResult{err: err}
	}
}
