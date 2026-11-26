package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"rabai/internal/accel"
	"rabai/internal/imagegen"
	"rabai/internal/imagemodel"
	"rabai/internal/store"
)

// imagePrefs are the user's picture settings.
type imagePrefs struct {
	GPU     string `json:"gpu"`     // "auto", "on", "off" (shared with chat models)
	Quality string `json:"quality"` // "auto" (fast decoder on the processor), "fast", "best"
}

func (s *Server) imagePrefs() imagePrefs {
	var st struct {
		Perf struct {
			GPU string `json:"gpu"`
		} `json:"perf"`
		Images struct {
			Quality string `json:"quality"`
		} `json:"images"`
	}
	if raw, _ := s.cfg.Store.ReadJSON("settings.json"); raw != nil {
		json.Unmarshal(raw, &st)
	}
	return imagePrefs{GPU: st.Perf.GPU, Quality: st.Images.Quality}
}

type imageView struct {
	imagemodel.Entry
	Default bool    `json:"default"`
	Ready   bool    `json:"ready"`
	EstSecs float64 `json:"estSecs"` // expected seconds per picture, 0 = unknown
	Device  string  `json:"device,omitempty"`
}

func (s *Server) imageView(e imagemodel.Entry) imageView {
	v := imageView{Entry: e, Default: e.Path == s.cfg.ImageLib.Default(), Ready: e.Ready()}
	l := e.Learned
	switch {
	case l.LastSecs > 0:
		v.EstSecs = l.LastSecs
	case l.Mode == "gpu" && l.GPUSecs > 0:
		v.EstSecs = l.GPUSecs * float64(e.Settings.Steps)
	case l.CPUSecs > 0:
		v.EstSecs = l.CPUSecs * float64(e.Settings.Steps)
	}
	if l.Mode != "" && l.Mode != "cpu" {
		v.Device = l.Device
	}
	return v
}

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	all := s.cfg.ImageLib.All()
	out := make([]imageView, 0, len(all))
	for _, e := range all {
		out = append(out, s.imageView(e))
	}
	writeJSON(w, map[string]any{
		"available": s.cfg.Images != nil && s.cfg.Images.Available(),
		"models":    out,
		"default":   s.cfg.ImageLib.Default(),
		"running":   s.imageRunning(),
	})
}

func (s *Server) imageRunning() string {
	if s.cfg.Images == nil {
		return ""
	}
	return s.cfg.Images.Running()
}

func (s *Server) imageAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"` // default | remove | refresh | rename | part
		Path   string `json:"path"`
		Name   string `json:"name"`
		Slot   string `json:"slot"`
		File   string `json:"file"`
		Pick   bool   `json:"pick"`
	}
	if readJSON(r, &req) != nil {
		jsonError(w, 400, "bad request")
		return
	}
	lib := s.cfg.ImageLib
	switch req.Action {
	case "default":
		if err := lib.SetDefault(req.Path); err != nil {
			jsonError(w, 404, err.Error())
			return
		}
	case "remove":
		if s.imageRunning() == req.Path {
			s.cfg.Images.Stop()
		}
		lib.Remove(req.Path)
	case "refresh":
		if _, err := lib.Refresh(req.Path); err != nil {
			jsonError(w, 404, err.Error())
			return
		}
	case "rename":
		lib.Rename(req.Path, req.Name)
	case "part":
		file := req.File
		if req.Pick {
			p, err := s.cfg.Native.PickModelFile()
			if err != nil {
				jsonError(w, 500, err.Error())
				return
			}
			if p == "" {
				writeJSON(w, map[string]any{"cancelled": true})
				return
			}
			file = p
		}
		e, err := lib.SetPart(req.Path, req.Slot, file)
		if err != nil {
			jsonError(w, 404, err.Error())
			return
		}
		if s.imageRunning() == req.Path {
			s.cfg.Images.Stop()
		}
		writeJSON(w, s.imageView(*e))
		return
	default:
		jsonError(w, 400, "unknown action")
		return
	}
	s.publish("images", nil)
	writeJSON(w, map[string]any{"ok": true})
}

// ---------- media ----------

func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := s.cfg.Store.MediaPath(id)
	if p == "" {
		http.NotFound(w, r)
		return
	}
	ext := strings.TrimPrefix(filepath.Ext(id), ".")
	w.Header().Set("Content-Type", store.MediaTypes[ext])
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, p)
}

func (s *Server) handleUploadMedia(w http.ResponseWriter, r *http.Request) {
	ext := strings.ToLower(r.URL.Query().Get("ext"))
	data, err := io.ReadAll(io.LimitReader(r.Body, 512<<20))
	if err != nil || len(data) == 0 {
		jsonError(w, 400, "empty upload")
		return
	}
	id, err := s.cfg.Store.SaveMedia(data, ext)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"id": id, "size": len(data)})
}

// mediaRefs finds media ids mentioned in a chat file.
var mediaRefRe = regexp.MustCompile(`[a-f0-9]{24}\.(?:png|jpg|jpeg|webp|gif|wav|mp3|m4a|ogg|flac|webm|mp4|mov)`)

// ---------- generation ----------

type imageJob struct {
	id     string
	cancel context.CancelFunc
}

type genRequest struct {
	Prompt   string   `json:"prompt"`
	Negative string   `json:"negative"`
	Size     string   `json:"size"`  // "square", "portrait", "landscape" or "WxH"
	Model    string   `json:"model"` // "" = the default image model
	Init     string   `json:"init"`  // media id or data URL of a picture to change
	Strength float64  `json:"strength"`
	Refs     []string `json:"refs"` // media ids or data URLs (edit models)
	Seed     int64    `json:"seed"`
	Steps    int      `json:"steps"`
}

func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	var req genRequest
	if readJSON(r, &req) != nil || strings.TrimSpace(req.Prompt) == "" {
		jsonError(w, 400, "prompt required")
		return
	}
	if s.cfg.Images == nil || !s.cfg.Images.Available() {
		jsonError(w, 503, "This version of RabAI has no image engine.")
		return
	}
	path := req.Model
	if path == "" {
		path = s.cfg.ImageLib.Default()
	}
	e := s.cfg.ImageLib.Get(path)
	if e == nil {
		jsonError(w, 409, "No image model is set up. Add one in Models → Image models.")
		return
	}
	if _, err := os.Stat(e.Path); err != nil {
		jsonError(w, 409, "The image model file "+e.FileName+" is missing. It may have been moved or deleted.")
		return
	}
	if len(e.Missing) > 0 {
		var names []string
		for _, m := range e.Missing {
			names = append(names, m.Label)
		}
		jsonError(w, 409, e.Name+" still needs: "+strings.Join(names, ", ")+". Choose the files in Models → Image models.")
		return
	}
	id := fmt.Sprintf("img%x", rand.Int63())
	ctx, cancel := context.WithCancel(context.Background())
	s.imgMu.Lock()
	if s.imgJobs == nil {
		s.imgJobs = map[string]*imageJob{}
	}
	s.imgJobs[id] = &imageJob{id: id, cancel: cancel}
	s.imgMu.Unlock()
	go s.runImageJob(ctx, id, e, req)
	writeJSON(w, map[string]any{"jobId": id, "model": e.Name})
}

func (s *Server) handleCancelImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobID string `json:"jobId"`
	}
	readJSON(r, &req)
	s.imgMu.Lock()
	j := s.imgJobs[req.JobID]
	s.imgMu.Unlock()
	if j != nil {
		j.cancel()
	}
	writeJSON(w, map[string]any{"ok": j != nil})
}

// imageEvent is sent to the UI as a job advances.
type imageEvent struct {
	JobID    string  `json:"jobId"`
	Status   string  `json:"status"` // running | done | error | cancelled
	Stage    string  `json:"stage,omitempty"`
	Detail   string  `json:"detail,omitempty"`
	Fraction float64 `json:"fraction"`
	Step     int     `json:"step,omitempty"`
	Steps    int     `json:"steps,omitempty"`
	Preview  string  `json:"preview,omitempty"` // data URL
	Media    string  `json:"media,omitempty"`
	Width    int     `json:"width,omitempty"`
	Height   int     `json:"height,omitempty"`
	Seconds  float64 `json:"seconds,omitempty"`
	Seed     int64   `json:"seed,omitempty"`
	Model    string  `json:"model,omitempty"`
	Device   string  `json:"device,omitempty"`
	Error    string  `json:"error,omitempty"`
	ErrorDet string  `json:"errorDetail,omitempty"`
}

func (s *Server) runImageJob(ctx context.Context, id string, e *imagemodel.Entry, req genRequest) {
	defer func() {
		s.imgMu.Lock()
		delete(s.imgJobs, id)
		s.imgMu.Unlock()
	}()
	// One picture at a time: the engine and memory are shared.
	s.imgRun.Lock()
	defer s.imgRun.Unlock()

	send := func(ev imageEvent) {
		ev.JobID = id
		s.publish("image", ev)
	}
	send(imageEvent{Status: "running", Stage: "preparing", Model: e.Name})

	width, height := imageSize(e.Settings, req.Size)
	steps := e.Settings.Steps
	if req.Steps > 0 && req.Steps <= 150 {
		steps = req.Steps
	}
	seed := req.Seed
	if seed <= 0 {
		seed = rand.Int63n(1 << 31)
	}
	greq := imagegen.Request{
		Prompt: strings.TrimSpace(req.Prompt), Negative: req.Negative,
		Width: width, Height: height, Steps: steps, CFG: e.Settings.CFG,
		Sampler: e.Settings.Sampler, Scheduler: e.Settings.Scheduler,
		FlowShift: e.Settings.FlowShift, Guidance: e.Settings.Guidance,
		Seed: seed, Preview: true,
	}
	if req.Init != "" {
		if b, err := s.mediaBytes(req.Init); err == nil {
			greq.Init = b
			greq.Strength = req.Strength
			if greq.Strength <= 0 || greq.Strength > 1 {
				greq.Strength = 0.6
			}
			if w, h, ok := pictureSize(b); ok {
				greq.Width, greq.Height = fitSize(w, h, width*height, e.Settings.SizeMultiple)
			}
			// Changing a picture runs only strength × steps; few-step models
			// (Turbo, Lightning) need a few more steps to keep it.
			if greq.Steps < 4 {
				greq.Steps = 4
			}
		}
	}
	for _, ref := range req.Refs {
		if b, err := s.mediaBytes(ref); err == nil {
			greq.Refs = append(greq.Refs, b)
		}
	}

	mode, dev := s.imageMode(ctx, e, send)
	if ctx.Err() != nil {
		send(imageEvent{Status: "cancelled"})
		return
	}
	// On the processor, models made for 1024×1024 run at about 768×768
	// unless the user asked for the best quality: roughly twice as fast.
	if mode == "cpu" && s.imagePrefs().Quality != "best" && len(greq.Init) == 0 && greq.Width*greq.Height > 800*800 {
		greq.Width, greq.Height = fitSize(greq.Width, greq.Height, 768*768, e.Settings.SizeMultiple)
	}
	resume := s.makeRoomForImage(e, mode)
	defer resume()

	run := func(mode string) (*imagegen.Result, error) {
		cfg := s.imageConfig(e, mode, dev)
		var last time.Time
		return s.cfg.Images.Generate(ctx, cfg, greq, func(p imagegen.Progress) {
			ev := imageEvent{Status: "running", Stage: p.Stage, Fraction: p.Fraction, Step: p.Step, Steps: p.Steps, Model: e.Name}
			if mode != "cpu" && dev != nil {
				ev.Device = dev.Label()
			}
			if len(p.Preview) > 0 && time.Since(last) > 600*time.Millisecond {
				last = time.Now()
				ev.Preview = "data:image/png;base64," + base64.StdEncoding.EncodeToString(p.Preview)
			}
			send(ev)
		})
	}
	res, err := run(mode)
	if err != nil && ctx.Err() == nil && mode != "cpu" {
		// The graphics card failed: remember that and use the processor.
		log.Printf("images: %s failed on the GPU (%v); retrying on the processor", e.FileName, err)
		s.cfg.ImageLib.Learn(e.Path, func(l *imagemodel.Learned) {
			l.Mode, l.Reason = "cpu", "The graphics card couldn't run this image model, so it uses the processor."
			if dev != nil {
				l.Device = dev.Desc
			}
		})
		mode = "cpu"
		res, err = run(mode)
	}
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			send(imageEvent{Status: "cancelled"})
			return
		}
		ev := imageEvent{Status: "error", Error: err.Error()}
		var ie *imagegen.Error
		if errors.As(err, &ie) {
			ev.ErrorDet = ie.Detail
		}
		send(ev)
		return
	}
	if mode != "cpu" && blank(res.PNG) {
		// An all-black or flat picture from the GPU means a driver problem.
		s.cfg.ImageLib.Learn(e.Path, func(l *imagemodel.Learned) {
			l.Mode, l.Reason = "cpu", "The graphics card made blank pictures with this model (often an old driver), so it uses the processor."
		})
		mode = "cpu"
		if res, err = run(mode); err != nil {
			send(imageEvent{Status: "error", Error: err.Error()})
			return
		}
	}
	mediaID, err := s.cfg.Store.SaveMedia(res.PNG, "png")
	if err != nil {
		send(imageEvent{Status: "error", Error: "Couldn't save the picture: " + err.Error()})
		return
	}
	s.cfg.ImageLib.Touch(e.Path)
	s.cfg.ImageLib.Learn(e.Path, func(l *imagemodel.Learned) {
		l.LastSecs, l.LastSteps = res.Seconds, steps
	})
	ev := imageEvent{Status: "done", Media: mediaID, Width: greq.Width, Height: greq.Height, Seconds: res.Seconds, Seed: seed, Model: e.Name, Fraction: 1}
	if mode != "cpu" && dev != nil {
		ev.Device = dev.Label()
	}
	send(ev)
}

// imageSize turns a size request into dimensions for the model.
func imageSize(st imagemodel.Settings, size string) (int, int) {
	base := st.Width * st.Height
	if base <= 0 {
		base = 512 * 512
	}
	m := st.SizeMultiple
	if m <= 0 {
		m = 8
	}
	switch strings.ToLower(size) {
	case "portrait", "tall", "vertical":
		return fitSize(2, 3, base, m)
	case "landscape", "wide", "horizontal":
		return fitSize(3, 2, base, m)
	case "", "square", "auto":
		return fitSize(1, 1, base, m)
	}
	if a, b, ok := strings.Cut(strings.ToLower(size), "x"); ok {
		w, _ := strconv.Atoi(strings.TrimSpace(a))
		h, _ := strconv.Atoi(strings.TrimSpace(b))
		if w >= 64 && h >= 64 && w <= 4096 && h <= 4096 {
			return fitSize(w, h, base, m)
		}
	}
	return fitSize(1, 1, base, m)
}

// fitSize scales an aspect ratio to about area pixels, rounded to multiples.
func fitSize(aw, ah, area, mult int) (int, int) {
	if aw <= 0 || ah <= 0 {
		aw, ah = 1, 1
	}
	if mult <= 0 {
		mult = 8
	}
	k := math.Sqrt(float64(area) / float64(aw*ah))
	w := int(math.Round(float64(aw)*k/float64(mult))) * mult
	h := int(math.Round(float64(ah)*k/float64(mult))) * mult
	return max(w, mult), max(h, mult)
}

func pictureSize(b []byte) (int, int, bool) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

// mediaBytes reads a media id or a data URL.
func (s *Server) mediaBytes(ref string) ([]byte, error) {
	if strings.HasPrefix(ref, "data:") {
		_, data, ok := strings.Cut(ref, ",")
		if !ok {
			return nil, errors.New("bad data URL")
		}
		return base64.StdEncoding.DecodeString(data)
	}
	p := s.cfg.Store.MediaPath(ref)
	if p == "" {
		return nil, errors.New("unknown media")
	}
	return os.ReadFile(p)
}

// blank reports a picture with (almost) no variation: all black, all one
// colour, or NaN output from a misbehaving driver.
func blank(pngData []byte) bool {
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		return false
	}
	b := img.Bounds()
	var n, sum, sq float64
	for y := b.Min.Y; y < b.Max.Y; y += max(1, b.Dy()/48) {
		for x := b.Min.X; x < b.Max.X; x += max(1, b.Dx()/48) {
			r, g, bb, _ := img.At(x, y).RGBA()
			l := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bb)) / 257
			sum += l
			sq += l * l
			n++
		}
	}
	if n == 0 {
		return false
	}
	mean := sum / n
	return sq/n-mean*mean < 4 // standard deviation under 2 grey levels
}

// ---------- devices, tuning and memory ----------

// imageEngineDLL is the image engine's GPU backend, or "".
func (s *Server) imageEngineDLL() string {
	if s.cfg.Images == nil || s.cfg.Images.Exe() == "" {
		return ""
	}
	dir := filepath.Join(filepath.Dir(s.cfg.Images.Exe()), "gpu")
	for _, n := range []string{"ggml-vulkan.dll", "libggml-vulkan.so"} {
		if st, err := os.Stat(filepath.Join(dir, n)); err == nil && !st.IsDir() {
			return filepath.Join(dir, n)
		}
	}
	return ""
}

// imageMode decides where model e runs: "cpu", "gpu" or "gpu_safe". The
// first time a model is used with a GPU present, a short test picture is
// made on both and the faster correct one is remembered.
func (s *Server) imageMode(ctx context.Context, e *imagemodel.Entry, send func(imageEvent)) (string, *accel.Device) {
	g := s.gpuInfo(20 * time.Second)
	prefs := s.imagePrefs()
	if g.Best == nil || prefs.GPU == "off" || s.imageEngineDLL() == "" {
		return "cpu", nil
	}
	dev := *g.Best
	if prefs.GPU == "on" {
		return "gpu", &dev
	}
	if l := e.Learned; l.Mode != "" && l.Device == dev.Desc {
		return l.Mode, &dev
	}
	send(imageEvent{Status: "running", Stage: "tuning", Detail: "Testing your graphics card for pictures", Model: e.Name})
	mode, reason, cpuS, gpuS := s.tuneImage(ctx, e, &dev)
	if ctx.Err() != nil {
		return "cpu", nil
	}
	s.cfg.ImageLib.Learn(e.Path, func(l *imagemodel.Learned) {
		l.Device, l.Mode, l.Reason, l.CPUSecs, l.GPUSecs = dev.Desc, mode, reason, cpuS, gpuS
	})
	log.Printf("images: %s on %s: %s (cpu %.2fs/step, gpu %.2fs/step)", e.FileName, dev.Desc, mode, cpuS, gpuS)
	return mode, &dev
}

// tuneImage makes a small test picture on the GPU and on the processor and
// returns which is better, with seconds per sampling step for each.
func (s *Server) tuneImage(ctx context.Context, e *imagemodel.Entry, dev *accel.Device) (mode, reason string, cpuS, gpuS float64) {
	steps := min(e.Settings.Steps, 2)
	w, h := fitSize(1, 1, 256*256, e.Settings.SizeMultiple)
	req := imagegen.Request{Prompt: "a red apple on a wooden table, photo", Width: w, Height: h, Steps: steps, CFG: e.Settings.CFG,
		Sampler: e.Settings.Sampler, Scheduler: e.Settings.Scheduler, FlowShift: e.Settings.FlowShift, Guidance: e.Settings.Guidance, Seed: 1}
	try := func(mode string, limit time.Duration) (float64, bool) {
		c, cancel := context.WithTimeout(ctx, limit)
		defer cancel()
		res, err := s.cfg.Images.Generate(c, s.imageConfig(e, mode, dev), req, nil)
		if err != nil {
			log.Printf("images: test on %s: %v", mode, err)
			return 0, false
		}
		if blank(res.PNG) {
			log.Printf("images: test on %s made a blank picture", mode)
			return 0, false
		}
		return res.Seconds / float64(steps), true
	}
	g, okG := try("gpu", 6*time.Minute)
	if !okG {
		g, okG = try("gpu_safe", 6*time.Minute)
		if okG {
			mode = "gpu_safe"
		}
	}
	if !okG {
		s.cfg.Images.Stop()
		return "cpu", "The graphics card couldn't make pictures with this model, so it uses the processor.", 0, 0
	}
	if mode == "" {
		mode = "gpu"
	}
	// The processor gets at most four times as long as the graphics card took.
	limit := time.Duration(math.Max(g*float64(steps)*4, 20)) * time.Second
	c, okC := try("cpu", limit+90*time.Second)
	s.cfg.Images.Stop()
	switch {
	case !okC:
		return mode, "The graphics card is much faster for pictures.", 0, g
	case g*1.15 < c:
		return mode, "The graphics card is faster for pictures.", c, g
	}
	return "cpu", "The processor is as fast as the graphics card for this image model, so it uses the processor.", c, g
}

// imageConfig builds the image engine configuration for e on a device.
func (s *Server) imageConfig(e *imagemodel.Entry, mode string, dev *accel.Device) imagegen.Config {
	h := s.currentHW()
	cfg := imagegen.Config{Model: e.Path, Parts: map[string]string{}, Threads: max(1, min(h.Physical, 16))}
	for k, v := range e.Parts {
		cfg.Parts[k] = v
	}
	if mode != "cpu" && dev != nil {
		cfg.Device, cfg.GPUDLL, cfg.Safe = dev.Name, s.imageEngineDLL(), mode == "gpu_safe"
	}
	// On the processor, a tiny decoder makes pictures about three times
	// faster with nearly the same look.
	q := s.imagePrefs().Quality
	if _, has := cfg.Parts["tae"]; !has && (q == "fast" || (q != "best" && mode == "cpu")) {
		if t := s.bundledTAE(e.Recipe); t != "" {
			cfg.Parts["tae"] = t
		}
	}
	return cfg
}

// bundledTAE returns a tiny decoder shipped with RabAI for a recipe.
func (s *Server) bundledTAE(recipe string) string {
	if s.cfg.Images == nil {
		return ""
	}
	name := ""
	switch {
	case strings.HasPrefix(recipe, "sd1") || strings.HasPrefix(recipe, "sd2") || recipe == "sd-turbo" || recipe == "sdxs" || recipe == "instruct-pix2pix":
		name = "taesd.safetensors"
	case strings.HasPrefix(recipe, "sdxl") || recipe == "cosxl-edit":
		name = "taesdxl.safetensors"
	default:
		return ""
	}
	p := filepath.Join(filepath.Dir(s.cfg.Images.Exe()), "tae", name)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// makeRoomForImage pauses the chat model when both won't fit in memory and
// returns a function that brings it back.
func (s *Server) makeRoomForImage(e *imagemodel.Entry, mode string) func() {
	s.mu.Lock()
	lm := s.loaded
	s.mu.Unlock()
	if lm == nil {
		return func() {}
	}
	h := s.currentHW()
	total := float64(h.TotalRAM)
	usable := total - math.Max(3<<30, total*0.3)
	imgNeed := float64(e.Size)*1.15 + 700<<20
	if mode != "cpu" {
		imgNeed = float64(e.Size)*0.4 + 500<<20 // most weights live in video memory
	}
	chatNeed := float64(lm.Profile.NeedBytes)
	if lm.Profile.Accel == "gpu" || lm.Profile.Accel == "gpu_safe" {
		chatNeed *= 0.4
	}
	if chatNeed+imgNeed <= usable {
		return func() {}
	}
	log.Printf("images: pausing the chat model to make room (%.1f + %.1f GB > %.1f GB)", chatNeed/(1<<30), imgNeed/(1<<30), usable/(1<<30))
	mode0, devName := lm.Profile.Accel, lm.Profile.Device
	s.cfg.Engine.Prepare(lm.Entry.Path, "Paused while making a picture")
	return func() {
		s.mu.Lock()
		still := s.loaded == lm && s.pending == nil
		if still {
			s.loaded, s.pending = nil, lm
		}
		s.mu.Unlock()
		if !still {
			return
		}
		// Free the image engine first so the chat model has the memory.
		s.cfg.Images.Stop()
		var dev *accel.Device
		if g := s.gpuInfo(0); g.Best != nil && g.Best.Desc == devName {
			d := *g.Best
			dev = &d
		}
		if mode0 == "" || mode0 == "tune" {
			mode0 = "cpu"
		}
		s.startModel(lm.Entry, lm, mode0, dev, s.idleSleepSeconds())
	}
}

// imageState guards image jobs.
type imageState struct {
	imgMu   sync.Mutex
	imgJobs map[string]*imageJob
	imgRun  sync.Mutex
}
