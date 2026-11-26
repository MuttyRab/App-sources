package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"rabai/internal/accel"
	"rabai/internal/engine"
	"rabai/internal/imagemodel"
	"rabai/internal/models"
	"rabai/internal/profile"
)

// gpuState is what RabAI knows about the graphics cards.
type gpuState struct {
	Checked bool           `json:"checked"`
	Devices []accel.Device `json:"devices"`
	Best    *accel.Device  `json:"best,omitempty"` // the card models may use
	Error   string         `json:"error,omitempty"`
}

// detectGPUs asks the engine which graphics cards it can use. It runs once in
// the background at start; a crash or hang (a broken driver) means "none".
func (s *Server) detectGPUs() {
	st := gpuState{Checked: true}
	if s.cfg.Engine == nil || s.cfg.Engine.Exe() == "" {
		s.setGPU(st)
		return
	}
	dll := s.gpuDLL()
	if dll == "" {
		s.setGPU(st)
		return
	}
	devs, err := accel.ListDevices(context.Background(), s.cfg.Engine.Exe(), append(os.Environ(), accel.GPUEnv(dll)...))
	if err != nil {
		st.Error = err.Error()
		log.Printf("gpu: device check failed: %v", err)
	}
	st.Devices = devs
	if d, ok := accel.Best(devs); ok {
		st.Best = &d
	}
	s.setGPU(st)
}

func (s *Server) setGPU(st gpuState) {
	s.gpuMu.Lock()
	s.gpu = st
	s.gpuMu.Unlock()
	close(s.gpuReady)
	s.publish("gpu", st)
}

// gpuInfo waits briefly for detection, then returns what is known.
func (s *Server) gpuInfo(wait time.Duration) gpuState {
	select {
	case <-s.gpuReady:
	case <-time.After(wait):
	}
	s.gpuMu.Lock()
	defer s.gpuMu.Unlock()
	return s.gpu
}

// gpuDLL is the engine's GPU backend, or "" when this build has none.
func (s *Server) gpuDLL() string {
	if s.cfg.Engine == nil || s.cfg.Engine.Exe() == "" {
		return ""
	}
	dir := filepath.Join(filepath.Dir(s.cfg.Engine.Exe()), "gpu")
	for _, n := range []string{"ggml-vulkan.dll", "libggml-vulkan.so"} {
		if st, err := os.Stat(filepath.Join(dir, n)); err == nil && !st.IsDir() {
			return filepath.Join(dir, n)
		}
	}
	return ""
}

// attempt is one way to start a model; the engine tries them in order.
type attempt struct {
	args     []string
	env      []string
	mode     string // accel mode
	safeKV   bool   // uncompressed cache and smaller context
	noVision bool
}

func gpuArgs(dev string) []string { return []string{"-dev", dev, "-ngl", "auto", "-fit", "on"} }

var cpuArgs = []string{"-dev", "none", "-ngl", "0"}

// planAttempts lists the ways to start model e with profile p: the chosen
// device first, then safer variants, ending with the processor.
func planAttempts(p profile.Profile, mode, dev, dll string, sleep int) []attempt {
	safe := p
	safe.CacheK, safe.CacheV = "f16", "f16"
	if safe.Ctx > 4096 {
		safe.Ctx = 4096
	}
	noVision := safe
	noVision.MMProj = ""

	var out []attempt
	add := func(pr profile.Profile, args, env []string, m string, safeKV, nv bool) {
		a := append(append([]string{}, pr.Args()...), args...)
		out = append(out, attempt{args: withSleep(a, sleep), env: env, mode: m, safeKV: safeKV, noVision: nv})
	}
	switch mode {
	case accel.ModeGPU:
		add(p, gpuArgs(dev), accel.GPUEnv(dll), accel.ModeGPU, false, false)
		add(p, gpuArgs(dev), accel.GPUEnv(dll, accel.SafeEnv...), accel.ModeGPUSafe, false, false)
	case accel.ModeGPUSafe:
		add(p, gpuArgs(dev), accel.GPUEnv(dll, accel.SafeEnv...), accel.ModeGPUSafe, false, false)
	}
	add(p, cpuArgs, nil, accel.ModeCPU, false, false)
	add(safe, cpuArgs, nil, accel.ModeCPU, true, false)
	if p.MMProj != "" {
		add(noVision, cpuArgs, nil, accel.ModeCPU, true, true)
	}
	return out
}

// accelFor decides how model e should run: "cpu", "gpu", "gpu_safe", or
// "tune" when it should be measured first. dev is the GPU's engine name.
func (s *Server) accelFor(e *models.Entry, prefs profile.Prefs) (mode string, dev *accel.Device) {
	g := s.gpuInfo(20 * time.Second)
	if g.Best == nil || prefs.GPU == "off" {
		return accel.ModeCPU, nil
	}
	d := *g.Best
	if prefs.GPU == "on" {
		return accel.ModeGPU, &d
	}
	if dec := e.Learned.Accel; dec != nil && dec.Device == d.Desc {
		return dec.Mode, &d
	}
	return "tune", &d
}

// tune measures model e on the graphics card and on the processor, records
// which is better, and then starts it that way.
func (s *Server) tune(e *models.Entry, lm *loadedModel, dev accel.Device, sleep int) {
	update := s.cfg.Engine.Prepare(e.Path, "Testing your graphics card")
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if s.tuneCancel != nil {
		s.tuneCancel()
	}
	s.tuneCancel = cancel
	s.mu.Unlock()
	defer cancel()

	// Keep the test short: a small context is enough and loads faster.
	test := lm.Profile
	test.Ctx = 2048
	test.CacheRAMMiB = 0
	test.MMProj = "" // the image add-on doesn't affect text speed
	base := test.Args()
	bench := func(stage string, prog float64, args, env []string) *accel.Bench {
		if !update(stage, prog) {
			cancel()
			return nil
		}
		var b *accel.Bench
		err := s.cfg.Engine.Probe(ctx, e.Path, append(append([]string{}, base...), args...), env, func(url string) error {
			var err error
			b, err = accel.Run(ctx, url, 32)
			return err
		})
		if err != nil {
			log.Printf("gpu: %s: %v", stage, err)
			return nil
		}
		return b
	}

	dec := &accel.Decision{Device: dev.Desc, Checked: time.Now()}
	dll := s.gpuDLL()
	dec.GPU = bench("Testing your graphics card", 0.05, gpuArgs(dev.Name), accel.GPUEnv(dll))
	gpuMode := accel.ModeGPU
	dec.CPU = bench("Testing your processor", 0.5, cpuArgs, nil)
	if ctx.Err() != nil {
		return // another model was chosen meanwhile
	}
	mode, reason := accel.Choose(dec.CPU, dec.GPU)
	if mode == accel.ModeCPU && dec.GPU != nil && dec.CPU != nil && (accel.Garbage(dec.GPU.Text) || !accel.Agree(dec.CPU.Text, dec.GPU.Text)) {
		// Wrong results: try again without the features old drivers get wrong.
		if safe := bench("Testing your graphics card in safe mode", 0.8, gpuArgs(dev.Name), accel.GPUEnv(dll, accel.SafeEnv...)); safe != nil {
			if m, r := accel.Choose(dec.CPU, safe); m == accel.ModeGPU {
				dec.GPU, mode, reason, gpuMode = safe, accel.ModeGPUSafe, r+" (safe mode)", accel.ModeGPUSafe
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
	if mode == accel.ModeGPU {
		mode = gpuMode
	}
	dec.Mode, dec.Reason = mode, reason
	if dec.CPU == nil && dec.GPU == nil {
		dec.Mode, dec.Reason = accel.ModeCPU, "The speed test couldn't run, so the model uses the processor."
	}
	log.Printf("gpu: %s on %s: %s (cpu %+v, gpu %+v)", e.FileName, dev.Desc, dec.Mode, benchSummary(dec.CPU), benchSummary(dec.GPU))
	s.cfg.Library.Learn(e.Path, func(l *profile.Learned) { l.Accel = dec })
	e.Learned.Accel = dec

	s.mu.Lock()
	still := s.pending == lm
	s.mu.Unlock()
	if !still {
		return
	}
	s.startModel(e, lm, dec.Mode, &dev, sleep)
}

func benchSummary(b *accel.Bench) string {
	if b == nil {
		return "failed"
	}
	return fmt.Sprintf("prompt %.0f t/s, gen %.1f t/s", b.PromptTPS, b.GenTPS)
}

// startModel hands the model to the engine with the attempts for mode.
func (s *Server) startModel(e *models.Entry, lm *loadedModel, mode string, dev *accel.Device, sleep int) {
	name := ""
	if dev != nil {
		name = dev.Name
		if mode != accel.ModeCPU {
			lm.Profile.Device = dev.Desc
		}
	}
	lm.Profile.Accel = mode
	lm.attempts = planAttempts(lm.Profile, mode, name, s.gpuDLL(), sleep)
	req := engine.LoadRequest{ModelPath: e.Path, Tag: lm}
	for i, a := range lm.attempts {
		if i == 0 {
			req.Args, req.Env = a.args, a.env
			continue
		}
		req.Fallbacks = append(req.Fallbacks, a.args)
		req.FallbackEnv = append(req.FallbackEnv, a.env)
	}
	s.cfg.Engine.Load(req)
}

// handleRetestGPU forgets every GPU decision so models are measured again.
func (s *Server) handleRetestGPU(w http.ResponseWriter, r *http.Request) {
	for _, e := range s.cfg.Library.All() {
		s.cfg.Library.Learn(e.Path, func(l *profile.Learned) { l.Accel = nil })
	}
	for _, e := range s.cfg.ImageLib.All() {
		s.cfg.ImageLib.Learn(e.Path, func(l *imagemodel.Learned) { l.Mode, l.Device, l.Reason, l.CPUSecs, l.GPUSecs = "", "", "", 0, 0 })
	}
	writeJSON(w, map[string]any{"ok": true})
}
