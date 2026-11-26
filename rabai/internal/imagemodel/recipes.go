package imagemodel

import (
	_ "embed"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
)

// Recipe describes a model family's parts and generation defaults. The table
// is generated from stable-diffusion.cpp's built-in server recipes.
type Recipe struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Slots        []Slot     `json:"slots"`
	RequiredAny  [][]string `json:"required_any,omitempty"`
	Defaults     Defaults   `json:"defaults"`
	Features     []string   `json:"features,omitempty"`
	SizeMultiple int        `json:"size_multiple,omitempty"`
}

// Slot is one file a model needs (or can use).
type Slot struct {
	ID       string `json:"id"`
	Required bool   `json:"required"`
	Label    string `json:"label,omitempty"`
}

// Defaults are recommended generation settings.
type Defaults struct {
	Width        int       `json:"width,omitempty"`
	Height       int       `json:"height,omitempty"`
	Steps        int       `json:"steps,omitempty"`
	CFGScale     float64   `json:"cfg_scale,omitempty"`
	Sampler      string    `json:"sampling_method,omitempty"`
	Scheduler    string    `json:"scheduler,omitempty"`
	FlowShift    float64   `json:"flow_shift,omitempty"`
	Guidance     float64   `json:"distilled_guidance,omitempty"`
	ImgCFGScale  float64   `json:"img_cfg_scale,omitempty"`
	Strength     float64   `json:"strength,omitempty"`
	CustomSigmas []float64 `json:"custom_sigmas,omitempty"`
}

//go:embed recipes.json
var recipesJSON []byte

var recipes = func() map[string]*Recipe {
	var doc struct {
		Recipes []*Recipe `json:"recipes"`
	}
	if err := json.Unmarshal(recipesJSON, &doc); err != nil {
		panic("imagemodel: bad recipes.json: " + err.Error())
	}
	m := map[string]*Recipe{}
	for _, r := range doc.Recipes {
		m[r.ID] = r
	}
	return m
}()

// RecipeByID returns a recipe, or nil.
func RecipeByID(id string) *Recipe { return recipes[id] }

// Slot returns the recipe's slot with id, or nil.
func (r *Recipe) Slot(id string) *Slot {
	for i := range r.Slots {
		if r.Slots[i].ID == id {
			return &r.Slots[i]
		}
	}
	return nil
}

// MainSlot is "model" (all-in-one checkpoints) or "diffusion_model".
func (r *Recipe) MainSlot() string {
	if r.Slot("model") != nil {
		return "model"
	}
	return "diffusion_model"
}

// HasFeature reports whether the recipe enables a feature (e.g. "ref_images"
// for models that edit pictures).
func (r *Recipe) HasFeature(f string) bool {
	for _, x := range r.Features {
		if x == f {
			return true
		}
	}
	return false
}

var officialSDXLTurbo = regexp.MustCompile(`(?i)(sd_?xl_?turbo|sdxl-turbo|stable-diffusion-xl-1\.0-turbo|sdxl_turbo)`)

var fewStep = regexp.MustCompile(`(?i)(turbo|lightning|hyper|dmd2?|lcm|flash|schnell|distill)`)

// ChooseRecipe picks the recipe for a family, using the file name to tell
// variants apart that share weights layouts (e.g. FLUX dev vs schnell). It
// returns "" for families RabAI can't use to make pictures from text.
func ChooseRecipe(fam, path string, params int64) string {
	n := strings.ToLower(filepath.Base(path))
	has := func(s ...string) bool {
		for _, x := range s {
			if strings.Contains(n, x) {
				return true
			}
		}
		return false
	}
	switch fam {
	case FamSD1, FamSD1Tiny:
		return "sd1-checkpoint"
	case FamSDXS512, FamSDXS09:
		return "sdxs"
	case FamSD1Inpaint:
		return "sd1-inpainting"
	case FamSD1Pix2Pix, FamSDXLPix2Pix:
		return "instruct-pix2pix"
	case FamSD2, FamSD2Tiny:
		switch {
		case has("turbo"):
			return "sd-turbo"
		case has("768", "-v.", "_v.", "-v-"):
			return "sd2-768-checkpoint"
		}
		return "sd2-512-checkpoint"
	case FamSD2Inpaint:
		return "sd2-inpainting"
	case FamSDXL, FamSDXLSSD1B, FamSDXLVega:
		switch {
		case has("cosxl") && has("edit"):
			return "cosxl-edit"
		case has("turbo"):
			return "sdxl-turbo"
		}
		return "sdxl-checkpoint"
	case FamSDXLInpaint:
		return "sdxl-inpainting"
	case FamSD3:
		switch {
		case has("large"):
			return "sd35-large"
		case has("3.5", "35", "3_5"):
			return "sd35-medium"
		}
		return "sd3-medium"
	case FamFlux:
		switch {
		case has("kontext"):
			return "flux1-kontext"
		case has("schnell"):
			return "flux1-schnell"
		}
		return "flux1-dev"
	case FamFluxFill:
		return "flux1-fill"
	case FamFluxControls:
		if has("canny") {
			return "flux1-canny"
		}
		return "flux1-depth"
	case FamFlex2:
		return "flex2"
	case FamChroma:
		return "chroma"
	case FamChromaRad:
		return "chroma-radiance"
	case FamLongcat:
		if has("edit") {
			return "longcat-image-edit"
		}
		return "longcat-image"
	case FamFlux2:
		return "flux2-dev"
	case FamFlux2Klein:
		big := has("9b") || params > 6_000_000_000
		switch {
		case has("base") && big:
			return "flux2-klein-base-9b"
		case has("base"):
			return "flux2-klein-base-4b"
		case big:
			return "flux2-klein-9b"
		}
		return "flux2-klein-4b"
	case FamFlux3:
		if has("base") {
			return "flux3-klein-base"
		}
		return "flux3-klein"
	case FamQwenImage:
		switch {
		case has("edit") && has("2511"):
			return "qwen-image-edit-2511"
		case has("edit") && has("2509"):
			return "qwen-image-edit-2509"
		case has("edit"):
			return "qwen-image-edit"
		}
		return "qwen-image"
	case FamQwenLayered:
		return "qwen-image-layered"
	case FamQwenImage21:
		if has("turbo") {
			return "qwen-image-21-turbo"
		}
		return "qwen-image-21"
	case FamZImage:
		if has("turbo") {
			return "z-image-turbo"
		}
		return "z-image"
	case FamZImageL2P:
		return "z-image-l2p"
	case FamAnima:
		if has("edit") {
			return "anima-edit"
		}
		return "anima"
	case FamOvis:
		return "ovis-image"
	case FamErnie:
		if has("turbo") {
			return "ernie-image-turbo"
		}
		return "ernie-image"
	case FamLens:
		if has("turbo") {
			return "lens-turbo"
		}
		return "lens"
	case FamMiniT2I:
		return "minit2i"
	case FamPiD:
		if has("1.5", "15", "1_5") {
			return "pid-15"
		}
		return "pid"
	case FamIdeogram4:
		return "ideogram4"
	case FamSefi:
		return "sefi-image"
	case FamKrea2:
		switch {
		case has("turbo") && has("edit"):
			return "krea2-turbo-edit"
		case has("turbo"):
			return "krea2-turbo"
		case has("edit"):
			return "krea2-raw-edit"
		}
		return "krea2-raw"
	case FamMageFlow:
		switch {
		case has("edit"):
			return "mage-flow-edit"
		case has("turbo"):
			return "mage-flow-turbo"
		}
		return "mage-flow"
	case FamSenseNova:
		return "sensenova-u15"
	case FamLLaDA:
		if has("turbo") {
			return "llada-image-turbo"
		}
		return "llada-image"
	case FamPixArt:
		switch {
		case has("sigma"):
			return "pixart-sigma"
		case has("512"):
			return "pixart-alpha-512"
		}
		return "pixart-alpha-1024"
	case FamMing:
		return "ming-image"
	case FamIris:
		return "iris-3b"
	case FamMoss:
		if has("sft") {
			return "moss-image-sft"
		}
		return "moss-image-base"
	case FamFibo2:
		if has("edit") {
			return "fibo2-edit-turbo"
		}
		return "fibo2-dev"
	case FamHiDream:
		return "hidream-o1-dev"
	case FamBoogu:
		if has("edit") {
			return "boogu-image-edit"
		}
		return "boogu-image"
	case FamMiniMaxImg:
		return "minimax-h3-image"
	case FamPH3:
		return "p-h3-image"
	}
	return ""
}

// Settings are the generation settings RabAI uses for a model.
type Settings struct {
	Width, Height int
	Steps         int
	CFG           float64
	Sampler       string
	Scheduler     string
	FlowShift     float64
	Guidance      float64
	SizeMultiple  int
}

// DefaultSettings returns a recipe's settings, adjusted for few-step model
// variants recognised from the file name (Turbo, Lightning, LCM…) that share
// a recipe with their slower base model.
func DefaultSettings(r *Recipe, path string) Settings {
	d := r.Defaults
	s := Settings{
		Width: d.Width, Height: d.Height, Steps: d.Steps, CFG: d.CFGScale,
		Sampler: d.Sampler, Scheduler: d.Scheduler, FlowShift: d.FlowShift, Guidance: d.Guidance,
		SizeMultiple: r.SizeMultiple,
	}
	if s.SizeMultiple <= 0 {
		s.SizeMultiple = 8
	}
	if s.Width == 0 || s.Height == 0 {
		s.Width, s.Height = 1024, 1024
		if strings.HasPrefix(r.ID, "sd1") || strings.HasPrefix(r.ID, "sd2") || r.ID == "instruct-pix2pix" {
			s.Width, s.Height = 512, 512
		}
	}
	if s.Steps == 0 {
		s.Steps = 20
	}
	if s.CFG == 0 {
		s.CFG = 7
	}
	n := strings.ToLower(filepath.Base(path))
	if r.ID == "sdxl-turbo" && !officialSDXLTurbo.MatchString(n) {
		// Community "XL Turbo" fine-tunes (DreamShaper XL Turbo, …) are made
		// for a few steps at full size, unlike Stability's one-step model.
		s.Width, s.Height, s.Steps, s.CFG, s.Sampler, s.Scheduler = 1024, 1024, 6, 1.5, "euler", ""
		return s
	}
	if strings.HasPrefix(r.ID, "sd1") || strings.HasPrefix(r.ID, "sdxl") || strings.HasPrefix(r.ID, "sd2") {
		switch {
		case strings.Contains(n, "lcm"):
			s.Steps, s.CFG, s.Sampler, s.Scheduler = 6, 1.5, "lcm", ""
		case fewStep.MatchString(n) && r.ID != "sd-turbo" && r.ID != "sdxl-turbo":
			// SDXL Lightning / Hyper / DMD2 and similar distilled checkpoints.
			s.Steps, s.CFG, s.Sampler = 6, 1.5, "euler"
			if strings.HasPrefix(r.ID, "sdxl") {
				s.Scheduler = "sgm_uniform"
			}
		}
	}
	return s
}
