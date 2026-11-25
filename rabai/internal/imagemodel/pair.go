package imagemodel

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// partCache remembers classified files by path, size and modification time.
var partCache = struct {
	sync.Mutex
	m map[string]cachedPart
}{m: map[string]cachedPart{}}

type cachedPart struct {
	mod  time.Time
	size int64
	part Part
	err  error
}

// Inspect classifies the file at path (cached).
func Inspect(path string) (Part, error) {
	st, err := os.Stat(path)
	if err != nil {
		return Part{}, err
	}
	partCache.Lock()
	c, ok := partCache.m[path]
	partCache.Unlock()
	if ok && c.mod.Equal(st.ModTime()) && c.size == st.Size() {
		return c.part, c.err
	}
	var p Part
	f, err := ReadFile(path)
	if err == nil {
		p = Classify(f)
	}
	partCache.Lock()
	partCache.m[path] = cachedPart{mod: st.ModTime(), size: st.Size(), part: p, err: err}
	partCache.Unlock()
	return p, err
}

// companionDirs are folder names that commonly hold an image model's parts
// (ComfyUI, Stable Diffusion WebUI, stable-diffusion.cpp layouts).
var companionDirs = map[string]bool{
	"vae": true, "vae_approx": true, "text_encoders": true, "text_encoder": true, "text_encoder_2": true,
	"clip": true, "t5": true, "encoders": true, "llm": true, "unet": true, "diffusion_models": true,
	"checkpoints": true, "stable-diffusion": true, "tokenizers": true, "tokenizer": true, "models": true,
	"clip_vision": true, "connectors": true,
}

// Candidates lists files near a model that may be its parts: the model's own
// folder, its parent, and the usual sub-folders (vae, text_encoders…) of
// both. Extra paths (e.g. chat models RabAI knows) are added as given.
func Candidates(mainPath string, extra []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		k := strings.ToLower(p)
		if !seen[k] && k != strings.ToLower(mainPath) {
			seen[k] = true
			out = append(out, p)
		}
	}
	scan := func(dir string, depth int) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			full := filepath.Join(dir, e.Name())
			if e.IsDir() {
				if depth > 0 && companionDirs[strings.ToLower(e.Name())] {
					subScan(full, add)
				}
				continue
			}
			if HasModelExt(e.Name()) || strings.EqualFold(e.Name(), "tokenizer.json") {
				add(full)
			}
		}
	}
	dir := filepath.Dir(mainPath)
	scan(dir, 1)
	parent := filepath.Dir(dir)
	if parent != dir {
		scan(parent, 1)
		if gp := filepath.Dir(parent); gp != parent && companionDirs[strings.ToLower(filepath.Base(dir))] {
			scan(gp, 1) // models/<kind>/file → models/vae, models/text_encoders…
		}
	}
	for _, p := range extra {
		add(p)
	}
	if len(out) > 600 {
		out = out[:600]
	}
	return out
}

// subScan adds weight files in dir and one level of sub-folders.
func subScan(dir string, add func(string)) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if sub, err := os.ReadDir(full); err == nil {
				for _, s := range sub {
					if !s.IsDir() && (HasModelExt(s.Name()) || strings.EqualFold(s.Name(), "tokenizer.json")) {
						add(filepath.Join(full, s.Name()))
					}
				}
			}
			continue
		}
		if HasModelExt(e.Name()) || strings.EqualFold(e.Name(), "tokenizer.json") {
			add(full)
		}
	}
}

// Resolution is the outcome of finding a model's parts.
type Resolution struct {
	Parts   map[string]string `json:"parts"`   // slot → file
	Missing []MissingSlot     `json:"missing"` // required parts not found
}

// MissingSlot names a part the user still has to provide.
type MissingSlot struct {
	Slot  string `json:"slot"`
	Label string `json:"label"`
}

// SlotLabel is a readable name for a slot.
func SlotLabel(r *Recipe, slot string) string {
	if s := r.Slot(slot); s != nil && s.Label != "" {
		return s.Label
	}
	switch slot {
	case "clip_l":
		return "CLIP-L text encoder"
	case "clip_g":
		return "CLIP-G text encoder"
	case "t5xxl":
		return "T5-XXL text encoder"
	case "llm":
		return "Text encoder"
	case "llm_vision":
		return "Vision projector"
	case "vae":
		return "VAE"
	case "tae":
		return "Tiny VAE (TAESD)"
	case "tokenizer":
		return "tokenizer.json"
	case "embeddings_connectors":
		return "Text connector"
	case "control_net":
		return "ControlNet"
	case "uncond_diffusion_model":
		return "Unconditional diffusion model"
	}
	return slot
}

// Resolve finds the parts recipe r needs for the main model, choosing from
// candidates. Parts the main file already contains (all-in-one checkpoints)
// count as present. overrides are the user's own choices and win.
func Resolve(main Part, mainFile *File, r *Recipe, candidates []string, overrides map[string]string) Resolution {
	res := Resolution{Parts: map[string]string{}}
	res.Parts[r.MainSlot()] = main.Path
	var parts []Part
	for _, c := range candidates {
		if p, err := Inspect(c); err == nil && p.Role != RoleUnknown {
			parts = append(parts, p)
		} else if strings.EqualFold(filepath.Base(c), "tokenizer.json") {
			parts = append(parts, Part{Path: c, Role: RoleTokenizer})
		}
	}
	inMain := func(slot string) bool {
		if mainFile == nil {
			return false
		}
		switch slot {
		case "vae":
			return mainFile.hasPrefix("first_stage_model.") || mainFile.hasPrefix("vae.")
		case "clip_l":
			return mainFile.hasPrefix("text_encoders.clip_l.") || mainFile.hasPrefix("cond_stage_model.") || mainFile.hasPrefix("conditioner.embedders.0.")
		case "clip_g":
			return mainFile.hasPrefix("text_encoders.clip_g.") || mainFile.hasPrefix("conditioner.embedders.1.")
		case "t5xxl":
			return mainFile.hasPrefix("text_encoders.t5xxl.")
		case "llm":
			return mainFile.hasPrefix("text_encoders.llm.") || mainFile.hasPrefix("model.language_model.")
		}
		return false
	}

	want := map[string]bool{}
	for _, s := range r.Slots {
		if s.ID == r.MainSlot() {
			continue
		}
		if s.Required || s.ID == "vae" || s.ID == "llm_vision" {
			want[s.ID] = true
		}
	}
	for _, group := range r.RequiredAny {
		for _, id := range group {
			if id == "vae" {
				want["vae"] = true
			}
		}
	}

	var llmPath string
	order := []string{"clip_l", "clip_g", "t5xxl", "llm", "llm_vision", "vae", "tokenizer", "embeddings_connectors", "uncond_diffusion_model", "control_net"}
	for _, slot := range order {
		if !want[slot] {
			continue
		}
		if p, ok := overrides[slot]; ok && p != "" {
			res.Parts[slot] = p
			if slot == "llm" {
				llmPath = p
			}
			continue
		}
		if inMain(slot) {
			continue
		}
		best := pick(slot, r, main, parts, llmPath)
		if best != "" {
			res.Parts[slot] = best
			if slot == "llm" {
				llmPath = best
			}
		}
	}

	for _, s := range r.Slots {
		if !s.Required || s.ID == r.MainSlot() {
			continue
		}
		if _, ok := res.Parts[s.ID]; !ok && !inMain(s.ID) {
			res.Missing = append(res.Missing, MissingSlot{Slot: s.ID, Label: SlotLabel(r, s.ID)})
		}
	}
	for _, group := range r.RequiredAny {
		ok := false
		for _, id := range group {
			if _, have := res.Parts[id]; have || inMain(id) {
				ok = true
			}
		}
		if !ok && len(group) > 0 {
			res.Missing = append(res.Missing, MissingSlot{Slot: group[0], Label: SlotLabel(r, group[0])})
		}
	}
	return res
}

// pick chooses the best candidate for slot.
func pick(slot string, r *Recipe, main Part, parts []Part, llmPath string) string {
	type scored struct {
		path  string
		score int
	}
	var c []scored
	mainDir := strings.ToLower(filepath.Dir(main.Path))
	words := familyWords(r.ID)
	label := ""
	if s := r.Slot(slot); s != nil {
		label = s.Label
	}
	spec := specFromLabel(label)
	for _, p := range parts {
		score := 0
		name := baseLower(p.Path)
		switch slot {
		case "clip_l":
			if p.Role != RoleCLIPL {
				continue
			}
		case "clip_g":
			if p.Role != RoleCLIPG {
				continue
			}
		case "t5xxl":
			if p.Role != RoleT5 {
				continue
			}
			if p.Vocab > 200000 { // UMT5 belongs to video models
				continue
			}
			if r.ID == "minit2i" {
				if p.Hidden != 0 && p.Hidden != 1024 {
					continue
				}
			} else if p.Hidden != 0 && p.Hidden != 4096 {
				continue
			}
		case "llm":
			if p.Role != RoleLLM {
				continue
			}
			if !archMatches(p.Arch, spec.arch) {
				continue
			}
			if spec.hidden > 0 && p.Hidden > 0 {
				if p.Hidden != spec.hidden {
					continue
				}
				score += 20
			}
			if spec.vision && strings.Contains(name, "vl") {
				score += 5
			}
			if strings.Contains(name, "abliterated") || strings.Contains(name, "uncensored") {
				score -= 1
			}
		case "llm_vision":
			if p.Role != RoleLLMVision || llmPath == "" {
				continue
			}
			// The projector must belong to the chosen encoder: same folder.
			if !strings.EqualFold(filepath.Dir(p.Path), filepath.Dir(llmPath)) {
				continue
			}
		case "vae":
			if p.Role != RoleVAE {
				continue
			}
			if l := wantedLatent(r.ID); l > 0 && p.Latent > 0 && p.Latent != l {
				continue
			}
			isQwen := strings.HasPrefix(r.ID, "qwen-image")
			if p.Conv3D != isQwen && p.Latent > 0 {
				continue
			}
		case "tokenizer":
			if p.Role != RoleTokenizer {
				continue
			}
		case "embeddings_connectors":
			if p.Role != RoleConnector && !strings.Contains(name, "connector") {
				continue
			}
		case "uncond_diffusion_model":
			if p.Role != RoleDiffusion || !strings.Contains(name, "uncond") {
				continue
			}
		case "control_net":
			if p.Role != RoleControlNet {
				continue
			}
		default:
			continue
		}
		for _, w := range words {
			if strings.Contains(name, w) {
				score += 4
				break
			}
		}
		if strings.ToLower(filepath.Dir(p.Path)) == mainDir {
			score += 2
		}
		c = append(c, scored{p.Path, score})
	}
	if len(c) == 0 {
		return ""
	}
	sort.SliceStable(c, func(i, j int) bool { return c[i].score > c[j].score })
	return c[0].path
}
