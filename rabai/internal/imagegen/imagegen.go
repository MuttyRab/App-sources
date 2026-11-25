// Package imagegen runs stable-diffusion.cpp's sd-server as a child process
// and makes pictures with it: one image model at a time, with progress,
// live previews and cancellation.
package imagegen

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rabai/internal/engine"
)

// slotFlags maps model parts to sd-server flags.
var slotFlags = map[string]string{
	"model":                  "--model",
	"diffusion_model":        "--diffusion-model",
	"clip_l":                 "--clip_l",
	"clip_g":                 "--clip_g",
	"t5xxl":                  "--t5xxl",
	"llm":                    "--llm",
	"llm_vision":             "--llm_vision",
	"vae":                    "--vae",
	"tae":                    "--tae",
	"tokenizer":              "--tokenizer",
	"embeddings_connectors":  "--embeddings-connectors",
	"uncond_diffusion_model": "--uncond-diffusion-model",
	"control_net":            "--control-net",
}

// Config is how to start the image engine for one model.
type Config struct {
	Model   string            // the model's main file (identifies it)
	Parts   map[string]string // slot → file
	Threads int
	Device  string // "" = processor; otherwise an engine device name, e.g. "Vulkan0"
	GPUDLL  string // the GPU backend to load when Device is set
	Safe    bool   // GPU safe mode (fewer driver features)
	TextCPU bool   // keep the text encoder on the processor (small graphics cards)
}

func (c Config) key() string {
	b, _ := json.Marshal(c)
	return string(b)
}

// Args renders the command line (without the listen address).
func (c Config) Args() []string {
	var a []string
	slots := make([]string, 0, len(c.Parts))
	for s := range c.Parts {
		slots = append(slots, s)
	}
	sort.Strings(slots)
	for _, s := range slots {
		if f, ok := slotFlags[s]; ok && c.Parts[s] != "" {
			a = append(a, f, c.Parts[s])
		}
	}
	if c.Threads > 0 {
		a = append(a, "-t", strconv.Itoa(c.Threads))
	}
	if c.Device == "" {
		a = append(a, "--backend", "cpu")
	} else {
		b := strings.ToLower(c.Device)
		if c.TextCPU {
			b = "te=cpu," + b
		}
		a = append(a, "--backend", b)
	}
	return a
}

// Env returns extra environment variables for the engine.
func (c Config) Env() []string {
	if c.Device == "" || c.GPUDLL == "" {
		return nil
	}
	env := []string{"GGML_BACKEND_PATH=" + c.GPUDLL}
	if c.Safe {
		env = append(env, "GGML_VK_DISABLE_COOPMAT=1", "GGML_VK_DISABLE_COOPMAT2=1", "GGML_VK_DISABLE_INTEGER_DOT_PRODUCT=1", "GGML_VK_DISABLE_BFLOAT16=1", "GGML_VK_DISABLE_F16=1")
	}
	return env
}

// Request is one picture to make.
type Request struct {
	Prompt    string
	Negative  string
	Width     int
	Height    int
	Steps     int
	CFG       float64
	Sampler   string
	Scheduler string
	FlowShift float64
	Guidance  float64
	Seed      int64
	Init      []byte   // picture to change (img2img), PNG/JPEG
	Strength  float64  // how much to change Init (0–1)
	Refs      [][]byte // reference pictures for edit models
	Preview   bool
}

// Progress is a snapshot of a running job.
type Progress struct {
	Stage     string  `json:"stage"` // loading, conditioning, sampling, decoding, saving
	Step      int     `json:"step"`  // sampling step done
	Steps     int     `json:"steps"` // total sampling steps
	Fraction  float64 `json:"fraction"`
	ElapsedMs int64   `json:"elapsedMs"`
	Preview   []byte  `json:"-"` // PNG of the picture so far
}

// Result is a finished picture.
type Result struct {
	PNG     []byte
	Seconds float64 // generation time, excluding loading the model
	Total   float64 // wall time including loading
}

// Manager owns the sd-server process.
type Manager struct {
	exe    string
	logDir string

	mu     sync.Mutex
	cur    *proc
	curKey string
	busy   int
	last   time.Time

	// IdleStop stops the engine (freeing memory) after this long unused.
	IdleStop time.Duration
}

type proc struct {
	cmd  *exec.Cmd
	url  string
	done chan struct{}
	tail *ring
	cfg  Config
}

// New creates a manager for the sd-server executable at exe ("" = none).
func New(exe, logDir string) *Manager {
	m := &Manager{exe: exe, logDir: logDir, IdleStop: 10 * time.Minute}
	go m.reaper()
	return m
}

// Available reports whether this build has an image engine.
func (m *Manager) Available() bool {
	if m.exe == "" {
		return false
	}
	_, err := os.Stat(m.exe)
	return err == nil
}

// Exe is the engine executable.
func (m *Manager) Exe() string { return m.exe }

// Running returns the model the engine has loaded, or "".
func (m *Manager) Running() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return ""
	}
	return m.cur.cfg.Model
}

// Busy reports whether a picture is being made.
func (m *Manager) Busy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.busy > 0
}

// Stop ends the engine process (frees its memory).
func (m *Manager) Stop() {
	m.mu.Lock()
	p := m.cur
	m.cur, m.curKey = nil, ""
	m.mu.Unlock()
	stop(p)
}

func (m *Manager) reaper() {
	for range time.Tick(30 * time.Second) {
		m.mu.Lock()
		idle := m.cur != nil && m.busy == 0 && m.IdleStop > 0 && time.Since(m.last) > m.IdleStop
		m.mu.Unlock()
		if idle {
			log.Printf("imagegen: stopping idle engine")
			m.Stop()
		}
	}
}

func stop(p *proc) {
	if p == nil {
		return
	}
	if p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// ensure starts the engine for cfg (unless it already runs it) and returns
// its address.
func (m *Manager) ensure(ctx context.Context, cfg Config) (*proc, error) {
	m.mu.Lock()
	if m.cur != nil && m.curKey == cfg.key() {
		select {
		case <-m.cur.done:
			m.cur, m.curKey = nil, ""
		default:
			p := m.cur
			m.mu.Unlock()
			return p, nil
		}
	}
	old := m.cur
	m.cur, m.curKey = nil, ""
	m.mu.Unlock()
	stop(old)

	if !m.Available() {
		return nil, errors.New("this version of RabAI has no image engine")
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	args := append([]string{"--listen-ip", "127.0.0.1", "--listen-port", strconv.Itoa(port)}, cfg.Args()...)
	logPath := filepath.Join(m.logDir, "image-engine.log")
	logf, _ := os.Create(logPath)
	if logf != nil {
		fmt.Fprintf(logf, "%s %s\n%s\n\n", m.exe, strings.Join(args, " "), strings.Join(cfg.Env(), " "))
	}
	cmd := exec.Command(m.exe, args...)
	cmd.Dir = filepath.Dir(m.exe)
	cmd.Env = append(os.Environ(), cfg.Env()...)
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout
	engine.PrepareCmd(cmd)
	if err := cmd.Start(); err != nil {
		if logf != nil {
			logf.Close()
		}
		return nil, fmt.Errorf("the image engine couldn't start: %w", err)
	}
	engine.AfterStart(cmd)
	p := &proc{cmd: cmd, url: "http://127.0.0.1:" + strconv.Itoa(port), done: make(chan struct{}), tail: &ring{max: 80}, cfg: cfg}
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64*1024), 4<<20)
		for sc.Scan() {
			line := sc.Text()
			p.tail.add(line)
			if logf != nil {
				logf.WriteString(line + "\n")
			}
		}
	}()
	go func() {
		cmd.Wait()
		if logf != nil {
			logf.Close()
		}
		close(p.done)
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if resp, err := client.Get(p.url + "/sdcpp/v1/capabilities"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		select {
		case <-p.done:
			return nil, explain(p.tail.lines())
		case <-ctx.Done():
			stop(p)
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			stop(p)
			return nil, errors.New("the image engine took too long to start")
		}
	}
	m.mu.Lock()
	m.cur, m.curKey, m.last = p, cfg.key(), time.Now()
	m.mu.Unlock()
	return p, nil
}

// Generate makes one picture. onProgress is called as the job advances.
func (m *Manager) Generate(ctx context.Context, cfg Config, req Request, onProgress func(Progress)) (*Result, error) {
	m.mu.Lock()
	m.busy++
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.busy--
		m.last = time.Now()
		m.mu.Unlock()
	}()
	start := time.Now()
	if onProgress != nil {
		onProgress(Progress{Stage: "loading"})
	}
	p, err := m.ensure(ctx, cfg)
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"prompt":          req.Prompt,
		"negative_prompt": req.Negative,
		"width":           req.Width,
		"height":          req.Height,
		"seed":            req.Seed,
		"batch_count":     1,
		"output_format":   "png",
		"preview":         "none",
		"sample_params": map[string]any{
			"sample_steps": req.Steps,
			"guidance":     map[string]any{"txt_cfg": req.CFG},
		},
		"embed_image_metadata": true,
	}
	sp := body["sample_params"].(map[string]any)
	if req.Sampler != "" {
		sp["sample_method"] = req.Sampler
	}
	if req.Scheduler != "" {
		sp["scheduler"] = req.Scheduler
	}
	if req.FlowShift > 0 {
		sp["flow_shift"] = req.FlowShift
	}
	if req.Guidance > 0 {
		sp["guidance"].(map[string]any)["distilled_guidance"] = req.Guidance
	}
	if req.Preview {
		body["preview"] = "proj"
		body["preview_interval"] = 1
	}
	if len(req.Init) > 0 {
		body["init_image"] = base64.StdEncoding.EncodeToString(req.Init)
		if req.Strength > 0 {
			body["strength"] = req.Strength
		}
	}
	if len(req.Refs) > 0 {
		refs := make([]string, len(req.Refs))
		for i, r := range req.Refs {
			refs[i] = base64.StdEncoding.EncodeToString(r)
		}
		body["ref_images"] = refs
	}
	b, _ := json.Marshal(body)
	hreq, _ := http.NewRequestWithContext(ctx, "POST", p.url+"/sdcpp/v1/img_gen", bytes.NewReader(b))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		return nil, m.engineErr(p, err)
	}
	var sub struct {
		ID    string `json:"id"`
		Error any    `json:"error"`
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	json.Unmarshal(raw, &sub)
	if resp.StatusCode >= 300 || sub.ID == "" {
		return nil, fmt.Errorf("the image engine refused the request: %s", strings.TrimSpace(string(raw)))
	}

	cancelled := false
	client := &http.Client{Timeout: 15 * time.Second}
	for {
		select {
		case <-ctx.Done():
			if !cancelled {
				cancelled = true
				if r, err := client.Post(p.url+"/sdcpp/v1/jobs/"+sub.ID+"/cancel", "application/json", nil); err == nil {
					r.Body.Close()
				}
			}
		case <-p.done:
			return nil, explain(p.tail.lines())
		case <-time.After(350 * time.Millisecond):
		}
		r, err := client.Get(p.url + "/sdcpp/v1/jobs/" + sub.ID)
		if err != nil {
			if cancelled {
				return nil, ctx.Err()
			}
			continue
		}
		var job jobStatus
		err = json.NewDecoder(r.Body).Decode(&job)
		r.Body.Close()
		if err != nil {
			continue
		}
		if onProgress != nil {
			onProgress(job.progress())
		}
		switch job.Status {
		case "completed":
			if job.Result == nil || len(job.Result.Images) == 0 {
				return nil, errors.New("the image engine returned no picture")
			}
			png, err := base64.StdEncoding.DecodeString(job.Result.Images[0].B64)
			if err != nil {
				return nil, err
			}
			res := &Result{PNG: png, Total: time.Since(start).Seconds()}
			if job.Started > 0 && job.Completed > job.Started {
				res.Seconds = float64(job.Completed-job.Started) / 1000
			} else {
				res.Seconds = res.Total
			}
			return res, nil
		case "failed":
			msg := "the image engine failed"
			if job.Error != nil && job.Error.Message != "" {
				msg = job.Error.Message
			}
			return nil, friendly(msg, p.tail.lines())
		case "cancelled":
			return nil, context.Canceled
		}
	}
}

func (m *Manager) engineErr(p *proc, err error) error {
	select {
	case <-p.done:
		return explain(p.tail.lines())
	default:
	}
	return fmt.Errorf("the image engine didn't respond: %w", err)
}

type jobStatus struct {
	Status    string `json:"status"`
	Started   int64  `json:"started"`
	Completed int64  `json:"completed"`
	Progress  struct {
		Stage     string `json:"stage"`
		Completed *int   `json:"completed"`
		Total     *int   `json:"total"`
		Unit      string `json:"unit"`
		ElapsedMs int64  `json:"elapsed_ms"`
	} `json:"progress"`
	Preview *struct {
		B64 string `json:"b64_json"`
	} `json:"preview"`
	Result *struct {
		Images []struct {
			B64 string `json:"b64_json"`
		} `json:"images"`
	} `json:"result"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (j jobStatus) progress() Progress {
	pr := Progress{ElapsedMs: j.Progress.ElapsedMs}
	switch j.Progress.Stage {
	case "queued", "waiting", "preparing", "loading", "":
		pr.Stage = "loading"
	case "conditioning":
		pr.Stage = "conditioning"
	case "sampling":
		pr.Stage = "sampling"
	case "vae_decode", "vae_encode", "upscaling", "adetailer":
		pr.Stage = "decoding"
	case "encoding":
		pr.Stage = "saving"
	default:
		pr.Stage = j.Progress.Stage
	}
	if j.Progress.Stage == "sampling" && j.Progress.Completed != nil && j.Progress.Total != nil && *j.Progress.Total > 0 {
		pr.Step, pr.Steps = *j.Progress.Completed, *j.Progress.Total
		pr.Fraction = float64(pr.Step) / float64(pr.Steps)
	}
	if j.Preview != nil && j.Preview.B64 != "" {
		pr.Preview, _ = base64.StdEncoding.DecodeString(j.Preview.B64)
	}
	return pr
}

// explain turns the engine's last log lines into an error people understand.
func explain(lines []string) error {
	return friendly("", lines)
}

func friendly(msg string, lines []string) error {
	low := strings.ToLower(msg + "\n" + strings.Join(lines, "\n"))
	switch {
	case strings.Contains(low, "out of memory") || strings.Contains(low, "failed to allocate") || strings.Contains(low, "bad_alloc") || strings.Contains(low, "cannot allocate"):
		return &Error{"Not enough memory to make this picture. Close other apps, or try a smaller picture size or a smaller image model.", strings.Join(lines, "\n")}
	case strings.Contains(low, "get sd version from file failed") || strings.Contains(low, "unknown model") || strings.Contains(low, "unsupported model"):
		return &Error{"The image engine doesn't recognise this model file.", strings.Join(lines, "\n")}
	case strings.Contains(low, "failed to load") || strings.Contains(low, "load tensors from model loader failed") || strings.Contains(low, "init model loader from file failed"):
		return &Error{"The image model or one of its parts couldn't be loaded. A part may be the wrong type for this model, or a file may be damaged.", strings.Join(lines, "\n")}
	}
	if msg == "" {
		msg = "The image engine stopped unexpectedly."
	}
	return &Error{msg, strings.Join(lines, "\n")}
}

// Error is an image engine failure with its log for "Show details".
type Error struct {
	Msg    string
	Detail string
}

func (e *Error) Error() string { return e.Msg }

type ring struct {
	mu  sync.Mutex
	l   []string
	max int
}

func (r *ring) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.l) == r.max {
		copy(r.l, r.l[1:])
		r.l = r.l[:r.max-1]
	}
	r.l = append(r.l, s)
}

func (r *ring) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.l...)
}
