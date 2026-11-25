// Package engine runs llama-server as a child process: one model at a time,
// with load progress, readiness, memory reports and friendly errors.
package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Lines exchanged with llama-server when it runs as a "router child".
// Setting LLAMA_SERVER_ROUTER_PORT makes it report load progress on stdout and
// exit when its stdin closes, so it never outlives LocalChat.
const (
	envRouterPort = "LLAMA_SERVER_ROUTER_PORT"
	stateLine     = "cmd_child_to_router:state:"
	exitCommand   = "cmd_router_to_child:exit"
)

// Status values.
const (
	StatusIdle    = "idle"
	StatusLoading = "loading"
	StatusReady   = "ready"
	StatusError   = "error"
)

// MemReport is the engine's own account of the memory it allocated.
type MemReport struct {
	NCtx         int    `json:"nCtx"`
	ModelBytes   uint64 `json:"modelBytes"`
	ContextBytes uint64 `json:"contextBytes"`
	ComputeBytes uint64 `json:"computeBytes"`
}

// State is a snapshot of the engine.
type State struct {
	Seq         int64      `json:"seq"`
	Status      string     `json:"status"`
	Progress    float64    `json:"progress"`
	Stage       string     `json:"stage,omitempty"`
	ModelPath   string     `json:"modelPath,omitempty"`
	Error       string     `json:"error,omitempty"`
	ErrorDetail string     `json:"errorDetail,omitempty"`
	Memory      *MemReport `json:"memory,omitempty"`
	Args        []string   `json:"args,omitempty"`
	Attempt     int        `json:"attempt"`
	port        int
}

// LoadRequest describes a model start. Fallbacks are alternative argument
// sets tried in order when the engine fails to start with Args.
type LoadRequest struct {
	ModelPath string
	Args      []string
	Fallbacks [][]string
	Tag       any // opaque value returned with OnReady
}

// Manager owns the engine process.
type Manager struct {
	exe     string
	logPath string
	appPort int

	mu    sync.Mutex
	state State
	gen   int64
	cur   *proc
	subs  map[chan State]struct{}

	// OnReady is called (in its own goroutine) when a model becomes ready.
	OnReady func(req LoadRequest, st State)
}

type proc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	done  chan struct{}
	tail  *ringBuffer
}

// New creates a manager for the llama-server executable at exe.
func New(exe, logDir string, appPort int) *Manager {
	return &Manager{
		exe:     exe,
		logPath: filepath.Join(logDir, "engine.log"),
		appPort: appPort,
		state:   State{Status: StatusIdle},
		subs:    map[chan State]struct{}{},
	}
}

// State returns the current state.
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// BaseURL returns the engine's HTTP address, or "" when no model is ready.
func (m *Manager) BaseURL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Status != StatusReady || m.state.port == 0 {
		return ""
	}
	return "http://127.0.0.1:" + strconv.Itoa(m.state.port)
}

// Subscribe returns a channel receiving every state change.
func (m *Manager) Subscribe() (<-chan State, func()) {
	ch := make(chan State, 16)
	m.mu.Lock()
	m.subs[ch] = struct{}{}
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		delete(m.subs, ch)
		m.mu.Unlock()
	}
}

// setLocked updates state and notifies subscribers. Caller holds m.mu.
func (m *Manager) setLocked(f func(*State)) {
	f(&m.state)
	m.state.Seq++
	st := m.state
	for ch := range m.subs {
		select {
		case ch <- st:
		default: // slow subscriber: it will catch up from the next update
		}
	}
}

// Load stops any running model and starts req asynchronously.
func (m *Manager) Load(req LoadRequest) {
	m.mu.Lock()
	m.gen++
	gen := m.gen
	old := m.cur
	m.cur = nil
	m.setLocked(func(s *State) {
		*s = State{Seq: s.Seq, Status: StatusLoading, ModelPath: req.ModelPath, Stage: "starting"}
	})
	m.mu.Unlock()

	go func() {
		stopProc(old)
		attempts := append([][]string{req.Args}, req.Fallbacks...)
		for i, args := range attempts {
			if !m.current(gen) {
				return
			}
			ok := m.run(gen, req, args, i)
			if ok || !m.current(gen) {
				return
			}
		}
	}()
}

// Unload stops the running model.
func (m *Manager) Unload() {
	m.mu.Lock()
	m.gen++
	old := m.cur
	m.cur = nil
	m.setLocked(func(s *State) { *s = State{Seq: s.Seq, Status: StatusIdle} })
	m.mu.Unlock()
	stopProc(old)
}

// Shutdown stops the engine synchronously (used on app exit).
func (m *Manager) Shutdown() { m.Unload() }

func (m *Manager) current(gen int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gen == gen
}

// run starts one attempt and blocks until the model is ready (true) or the
// attempt failed (false).
func (m *Manager) run(gen int64, req LoadRequest, extra []string, attempt int) bool {
	port, err := freePort()
	if err != nil {
		m.fail(gen, "Couldn't reserve a local port for the engine.", err.Error())
		return false
	}
	args := append([]string{
		"-m", req.ModelPath,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--no-webui",
		"-np", "1",
		"--jinja",
		"-fa", "auto",
		"--log-colors", "off",
		"--no-log-timestamps",
	}, extra...)

	logf, _ := os.Create(m.logPath)
	if logf != nil {
		fmt.Fprintf(logf, "%s %s\n\n", m.exe, strings.Join(args, " "))
	}
	tail := newRingBuffer(60)

	cmd := exec.Command(m.exe, args...)
	cmd.Dir = filepath.Dir(m.exe)
	cmd.Env = append(os.Environ(), envRouterPort+"="+strconv.Itoa(m.appPort))
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	prepareCmd(cmd)
	if err := cmd.Start(); err != nil {
		if logf != nil {
			logf.Close()
		}
		m.fail(gen, "The engine couldn't start.", err.Error())
		return false
	}
	afterStart(cmd)

	p := &proc{cmd: cmd, stdin: stdin, done: make(chan struct{}), tail: tail}
	m.mu.Lock()
	if m.gen != gen {
		m.mu.Unlock()
		stopProc(p)
		return false
	}
	m.cur = p
	m.setLocked(func(s *State) {
		s.Status, s.Stage, s.Progress, s.Args, s.Attempt, s.port = StatusLoading, "loading", 0, extra, attempt, port
		s.Error, s.ErrorDetail = "", ""
	})
	m.mu.Unlock()

	var logMu sync.Mutex
	writeLog := func(line string) {
		tail.Add(line)
		if logf != nil {
			logMu.Lock()
			logf.WriteString(line + "\n")
			logMu.Unlock()
		}
	}

	readyCh := make(chan *MemReport, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 8<<20)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, stateLine) {
				writeLog(line)
				continue
			}
			var msg struct {
				State   string          `json:"state"`
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal([]byte(line[len(stateLine):]), &msg) != nil {
				continue
			}
			switch msg.State {
			case "loading":
				var pl struct {
					Current string  `json:"current"`
					Value   float64 `json:"value"`
				}
				json.Unmarshal(msg.Payload, &pl)
				m.mu.Lock()
				if m.gen == gen && m.state.Status == StatusLoading {
					m.setLocked(func(s *State) {
						if pl.Current != "" {
							s.Stage = pl.Current
						}
						if pl.Current == "" || pl.Current == "text_model" {
							s.Progress = pl.Value
						}
					})
				}
				m.mu.Unlock()
			case "ready":
				select {
				case readyCh <- parseMemory(msg.Payload):
				default:
				}
			}
		}
	}()
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			writeLog(sc.Text())
		}
	}()
	go func() {
		cmd.Wait()
		close(p.done)
		if logf != nil {
			logMu.Lock()
			logf.Close()
			logMu.Unlock()
		}
	}()

	// Wait for the ready report (or /health, for engines without the protocol).
	healthy := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		url := "http://127.0.0.1:" + strconv.Itoa(port) + "/health"
		client := &http.Client{Timeout: 2 * time.Second}
		for ctx.Err() == nil {
			if resp, err := client.Get(url); err == nil {
				resp.Body.Close()
				if resp.StatusCode == 200 {
					close(healthy)
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(400 * time.Millisecond):
			}
		}
	}()

	var mem *MemReport
	select {
	case mem = <-readyCh:
		select { // the HTTP side flips to ready right after the report
		case <-healthy:
		case <-time.After(30 * time.Second):
		case <-p.done:
		}
	case <-healthy:
		select {
		case mem = <-readyCh:
		case <-time.After(300 * time.Millisecond):
		}
	case <-p.done:
	}

	select {
	case <-p.done:
		// Give the stderr reader a moment to drain the last lines.
		time.Sleep(150 * time.Millisecond)
		msg, detail := Explain(tail.Lines())
		m.mu.Lock()
		if m.gen == gen {
			m.cur = nil
			last := attempt
			m.setLocked(func(s *State) {
				s.Status, s.Error, s.ErrorDetail, s.Attempt = StatusError, msg, detail, last
			})
		}
		m.mu.Unlock()
		return false
	default:
	}

	m.mu.Lock()
	if m.gen != gen {
		m.mu.Unlock()
		stopProc(p)
		return true
	}
	m.setLocked(func(s *State) {
		s.Status, s.Progress, s.Stage, s.Memory = StatusReady, 1, "", mem
	})
	st := m.state
	m.mu.Unlock()

	// Watch for a crash after the model was ready.
	go func() {
		<-p.done
		m.mu.Lock()
		if m.gen == gen && m.cur == p {
			m.cur = nil
			msg, detail := Explain(tail.Lines())
			if msg == genericError {
				msg = "The engine stopped unexpectedly."
			}
			m.setLocked(func(s *State) { s.Status, s.Error, s.ErrorDetail = StatusError, msg, detail })
		}
		m.mu.Unlock()
	}()

	if m.OnReady != nil {
		go m.OnReady(req, st)
	}
	return true
}

func (m *Manager) fail(gen int64, msg, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gen == gen {
		m.setLocked(func(s *State) { s.Status, s.Error, s.ErrorDetail = StatusError, msg, detail })
	}
}

// stopProc asks the engine to exit, then kills it if it does not.
func stopProc(p *proc) {
	if p == nil {
		return
	}
	if p.stdin != nil {
		io.WriteString(p.stdin, exitCommand+"\n")
		p.stdin.Close()
	}
	select {
	case <-p.done:
		return
	case <-time.After(3 * time.Second):
	}
	if p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		log.Printf("engine: process did not exit after kill")
	}
}

func parseMemory(raw json.RawMessage) *MemReport {
	var pl struct {
		Memory struct {
			NCtx    int `json:"n_ctx"`
			Devices []struct {
				Kind    string `json:"kind"`
				Model   uint64 `json:"model"`
				Context uint64 `json:"context"`
				Compute uint64 `json:"compute"`
			} `json:"devices"`
		} `json:"memory"`
	}
	if json.Unmarshal(raw, &pl) != nil || pl.Memory.NCtx == 0 {
		return nil
	}
	r := &MemReport{NCtx: pl.Memory.NCtx}
	for _, d := range pl.Memory.Devices {
		r.ModelBytes += d.Model
		r.ContextBytes += d.Context
		r.ComputeBytes += d.Compute
	}
	return r
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

const genericError = "The engine couldn't load this model."

// Explain turns the engine's last log lines into a short message for people
// and a detail string for the "show details" disclosure.
func Explain(lines []string) (msg, detail string) {
	detail = strings.Join(lines, "\n")
	low := strings.ToLower(detail)
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(low, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("unknown model architecture"):
		arch := ""
		if i := strings.Index(low, "unknown model architecture: '"); i >= 0 {
			rest := detail[i+len("unknown model architecture: '"):]
			if j := strings.IndexByte(rest, '\''); j > 0 {
				arch = rest[:j]
			}
		}
		if arch != "" {
			return fmt.Sprintf("This model type (%s) isn't supported by the built-in engine yet.", arch), detail
		}
		return "This model type isn't supported by the built-in engine yet.", detail
	case has("failed to allocate", "out of memory", "bad_alloc", "unable to allocate", "cannot allocate", "not enough memory", "failed to create context"):
		return "Not enough memory to load this model. Close other apps, lower the context size in Settings, or use a smaller or more compressed version of the model.", detail
	case has("invalid magic", "unexpectedly reached end of file", "tensor data is not within", "failed to read tensor", "corrupted", "data is not within the file bounds", "file is too small"):
		return "The model file looks incomplete or damaged. Try downloading it again.", detail
	case has("mmproj", "clip_model_load", "failed to load multimodal"):
		return "The image add-on (mmproj) for this model couldn't be loaded.", detail
	case has("no such file", "failed to open", "cannot find the file"):
		return "The model file couldn't be opened. It may have been moved or deleted.", detail
	}
	return genericError, detail
}

// ErrNoEngine is returned when the engine executable is missing.
var ErrNoEngine = errors.New("engine executable not found")

type ringBuffer struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func newRingBuffer(max int) *ringBuffer { return &ringBuffer{max: max} }

func (r *ringBuffer) Add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lines) == r.max {
		copy(r.lines, r.lines[1:])
		r.lines = r.lines[:r.max-1]
	}
	r.lines = append(r.lines, s)
}

func (r *ringBuffer) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}
