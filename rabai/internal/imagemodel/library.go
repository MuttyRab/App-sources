package imagemodel

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"rabai/internal/store"
)

// Entry is an image model RabAI knows about.
type Entry struct {
	Path      string            `json:"path"`
	Name      string            `json:"name"`
	FileName  string            `json:"fileName"`
	Family    string            `json:"family"`
	Recipe    string            `json:"recipe"`
	Kind      string            `json:"kind"` // readable family, e.g. "SDXL Turbo"
	Size      int64             `json:"size"` // all parts together
	Parts     map[string]string `json:"parts"`
	Overrides map[string]string `json:"overrides,omitempty"`
	Missing   []MissingSlot     `json:"missing,omitempty"`
	Edit      bool              `json:"edit"`     // can change an attached picture
	TextOnly  bool              `json:"textOnly"` // makes pictures from words
	Settings  Settings          `json:"settings"`
	Added     time.Time         `json:"added"`
	LastUsed  time.Time         `json:"lastUsed,omitempty"`
	Learned   Learned           `json:"learned"`
	Gone      bool              `json:"missingFile,omitempty"`
}

// Learned is what RabAI measured about a model on this PC.
type Learned struct {
	Device    string  `json:"device,omitempty"` // GPU it was tested with
	Mode      string  `json:"mode,omitempty"`   // "cpu" or "gpu"
	CPUSecs   float64 `json:"cpuSecs,omitempty"`
	GPUSecs   float64 `json:"gpuSecs,omitempty"`
	Reason    string  `json:"reason,omitempty"`
	LastSecs  float64 `json:"lastSecs,omitempty"` // last full picture, any device
	LastSteps int     `json:"lastSteps,omitempty"`
}

// Ready reports whether every required part is present.
func (e *Entry) Ready() bool { return len(e.Missing) == 0 && !e.Gone }

// ErrNotImageModel explains why a file can't be used to make pictures.
type ErrNotImageModel struct{ Reason string }

func (e ErrNotImageModel) Error() string { return e.Reason }

// Library is the list of image models, saved as JSON.
type Library struct {
	path string
	mu   sync.Mutex
	data struct {
		Models  map[string]*Entry `json:"models"`
		Default string            `json:"default"`
	}
	// ExtraParts returns more files to consider as parts (RabAI's chat
	// models, which can double as text encoders).
	ExtraParts func() []string
}

// Open loads the library at path (a missing file is an empty library).
func Open(path string) *Library {
	l := &Library{path: path}
	l.data.Models = map[string]*Entry{}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &l.data)
		if l.data.Models == nil {
			l.data.Models = map[string]*Entry{}
		}
	}
	return l
}

func (l *Library) saveLocked() {
	b, _ := json.MarshalIndent(l.data, "", "  ")
	store.WriteFileAtomic(l.path, b)
}

var cleanName = regexp.MustCompile(`(?i)[-_. ](q\d(_[a-z0-9]+)*|f16|f32|bf16|fp16|fp8(_e4m3fn)?|fp32|gguf|safetensors|pruned|emaonly|ema)\b`)

func nameFromFile(path string) string {
	n := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	for i := 0; i < 3; i++ {
		n = cleanName.ReplaceAllString(n, "")
	}
	n = strings.NewReplacer("_", " ", "-", " ").Replace(n)
	words := strings.Fields(n)
	for i, w := range words {
		lw := strings.ToLower(w)
		switch {
		case upperWords[lw]:
			words[i] = strings.ToUpper(w)
		case lw == w && len(w) > 0:
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	n = strings.Join(words, " ")
	if n == "" {
		n = filepath.Base(path)
	}
	return n
}

// upperWords are written in capitals in model names.
var upperWords = map[string]bool{"sd": true, "sdxl": true, "xl": true, "sd3": true, "sd35": true, "flux": true, "flux1": true, "flux2": true, "vae": true, "dmd": true, "dmd2": true, "lcm": true, "ssd": true, "hd": true, "4k": true}

// roleExplanations tell people what a part file is when they try to add it
// as a model.
var roleExplanations = map[string]string{
	RoleVAE:        "This is a VAE, a part of an image model. Add the main image model instead; RabAI finds its VAE automatically when it's in the same or a nearby folder.",
	RoleTAE:        "This is a tiny VAE (TAESD), a part of an image model. Add the main image model instead.",
	RoleCLIPL:      "This is a text encoder (CLIP), a part of an image model. Add the main image model instead; RabAI finds its text encoders automatically.",
	RoleCLIPG:      "This is a text encoder (CLIP-G), a part of an image model. Add the main image model instead.",
	RoleT5:         "This is a text encoder (T5), a part of an image model. Add the main image model instead; RabAI finds its text encoders automatically.",
	RoleLLMVision:  "This is a vision add-on for a text encoder. Add the main model instead.",
	RoleLoRA:       "This is a LoRA (a small add-on that changes a model's style), not a full model.",
	RoleControlNet: "This is a ControlNet add-on, not a full image model.",
	RoleUpscaler:   "This is an upscaler, not an image model.",
	RoleConnector:  "This is a text connector, a part of an image model. Add the main image model instead.",
}

// Inspect describes the image model at path without adding it.
func (l *Library) Inspect(path string) (*Entry, error) {
	f, err := ReadFile(path)
	if err != nil {
		if errors.Is(err, ErrUnknownFormat) {
			return nil, ErrNotImageModel{"This file isn't a model RabAI can read."}
		}
		return nil, ErrNotImageModel{"This model file looks incomplete or damaged (" + err.Error() + "). Try downloading it again."}
	}
	p := Classify(f)
	if msg, ok := roleExplanations[p.Role]; ok {
		return nil, ErrNotImageModel{msg}
	}
	if p.Role != RoleCheckpoint && p.Role != RoleDiffusion {
		return nil, ErrNotImageModel{"This file isn't an image model RabAI recognises."}
	}
	if VideoFamilies[p.Family] {
		return nil, ErrNotImageModel{"This is a video model. RabAI makes pictures, not videos."}
	}
	id := ChooseRecipe(p.Family, path, p.Params)
	r := RecipeByID(id)
	if r == nil {
		return nil, ErrNotImageModel{"This image model type isn't supported yet."}
	}
	e := &Entry{
		Path: path, FileName: filepath.Base(path), Name: nameFromFile(path),
		Family: p.Family, Recipe: id, Kind: r.Name,
		Edit:     r.HasFeature("ref_images") || strings.Contains(id, "edit") || id == "instruct-pix2pix" || id == "flux1-kontext",
		Settings: DefaultSettings(r, path),
		Added:    time.Now(),
	}
	e.TextOnly = !strings.Contains(id, "inpaint") && id != "instruct-pix2pix" && !strings.Contains(id, "edit") && id != "flux1-kontext" && id != "flux1-fill" && id != "flux1-canny" && id != "flux1-depth" && !strings.Contains(id, "controlnet")
	l.resolve(e, f, p, r)
	return e, nil
}

// resolve finds the entry's parts and totals its size.
func (l *Library) resolve(e *Entry, f *File, p Part, r *Recipe) {
	var extra []string
	if l.ExtraParts != nil {
		extra = l.ExtraParts()
	}
	res := Resolve(p, f, r, Candidates(e.Path, extra), e.Overrides)
	e.Parts, e.Missing = res.Parts, res.Missing
	e.Size = 0
	for _, path := range e.Parts {
		if st, err := os.Stat(path); err == nil {
			e.Size += st.Size()
		}
	}
}

// Add inspects path and saves it to the library.
func (l *Library) Add(path string) (*Entry, error) {
	e, err := l.Inspect(path)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if old, ok := l.data.Models[path]; ok {
		e.Added, e.LastUsed, e.Learned, e.Overrides = old.Added, old.LastUsed, old.Learned, old.Overrides
		if len(e.Overrides) > 0 {
			if f, err := ReadFile(path); err == nil {
				l.resolve(e, f, Classify(f), RecipeByID(e.Recipe))
			}
		}
		if old.Name != "" {
			e.Name = old.Name
		}
	}
	l.data.Models[path] = e
	if l.data.Default == "" || l.data.Models[l.data.Default] == nil {
		l.data.Default = path
	}
	l.saveLocked()
	cp := *e
	return &cp, nil
}

// Refresh re-checks a model's parts (after files were added nearby).
func (l *Library) Refresh(path string) (*Entry, error) {
	l.mu.Lock()
	_, ok := l.data.Models[path]
	l.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown image model")
	}
	return l.Add(path)
}

// SetPart sets (or with file "" clears) the user's choice for a part.
func (l *Library) SetPart(path, slot, file string) (*Entry, error) {
	l.mu.Lock()
	e, ok := l.data.Models[path]
	if !ok {
		l.mu.Unlock()
		return nil, fmt.Errorf("unknown image model")
	}
	if e.Overrides == nil {
		e.Overrides = map[string]string{}
	}
	if file == "" {
		delete(e.Overrides, slot)
	} else {
		e.Overrides[slot] = file
	}
	l.mu.Unlock()
	return l.Add(path)
}

// Get returns a copy of the entry for path, or nil.
func (l *Library) Get(path string) *Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.data.Models[path]; ok {
		cp := *e
		return &cp
	}
	return nil
}

// All lists the models, most recently used first.
func (l *Library) All() []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Entry, 0, len(l.data.Models))
	for _, e := range l.data.Models {
		cp := *e
		if _, err := os.Stat(e.Path); err != nil {
			cp.Gone = true
		}
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastUsed.Equal(out[j].LastUsed) {
			return out[i].LastUsed.After(out[j].LastUsed)
		}
		return out[i].Added.After(out[j].Added)
	})
	return out
}

// Remove forgets a model (its files stay on disk).
func (l *Library) Remove(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.data.Models, path)
	if l.data.Default == path {
		l.data.Default = ""
		var best *Entry
		for _, e := range l.data.Models {
			if best == nil || e.LastUsed.After(best.LastUsed) {
				best = e
			}
		}
		if best != nil {
			l.data.Default = best.Path
		}
	}
	l.saveLocked()
}

// Default is the path of the image model chats use, or "".
func (l *Library) Default() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.data.Default
}

// SetDefault chooses the image model chats use.
func (l *Library) SetDefault(path string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if path != "" && l.data.Models[path] == nil {
		return fmt.Errorf("unknown image model")
	}
	l.data.Default = path
	l.saveLocked()
	return nil
}

// Learn updates measurements for a model.
func (l *Library) Learn(path string, f func(*Learned)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.data.Models[path]; ok {
		f(&e.Learned)
		l.saveLocked()
	}
}

// Touch records that a model was used.
func (l *Library) Touch(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.data.Models[path]; ok {
		e.LastUsed = time.Now()
		l.saveLocked()
	}
}

// Rename sets a model's display name.
func (l *Library) Rename(path, name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.data.Models[path]; ok && strings.TrimSpace(name) != "" {
		e.Name = strings.TrimSpace(name)
		l.saveLocked()
	}
}
