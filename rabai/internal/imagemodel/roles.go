package imagemodel

import (
	"path/filepath"
	"strings"
)

// Roles a file can play.
const (
	RoleCheckpoint = "checkpoint"      // all-in-one image model (slot "model")
	RoleDiffusion  = "diffusion_model" // the main network alone
	RoleVAE        = "vae"
	RoleTAE        = "tae"
	RoleCLIPL      = "clip_l"
	RoleCLIPG      = "clip_g"
	RoleT5         = "t5xxl"
	RoleLLM        = "llm"        // a language model used as text encoder (also chat models)
	RoleLLMVision  = "llm_vision" // vision projector for an LLM text encoder
	RoleLoRA       = "lora"
	RoleControlNet = "control_net"
	RoleUpscaler   = "upscaler"
	RoleTokenizer  = "tokenizer"
	RoleConnector  = "embeddings_connectors"
	RoleUnknown    = ""
)

// Part is a file classified by the role it can play.
type Part struct {
	Path   string `json:"path"`
	Role   string `json:"role"`
	Family string `json:"family,omitempty"` // for main models
	Hidden int64  `json:"hidden,omitempty"` // text encoders: hidden size
	Vocab  int64  `json:"vocab,omitempty"`
	Arch   string `json:"arch,omitempty"`   // LLM encoders: architecture
	Latent int64  `json:"latent,omitempty"` // VAE/TAE: latent channels
	Conv3D bool   `json:"conv3d,omitempty"` // VAE: video-style 3D VAE
	Params int64  `json:"params,omitempty"`
	Size   int64  `json:"size"`
}

// Classify works out what a file is.
func Classify(f *File) Part {
	p := Part{Path: f.Path, Size: f.Size}
	for _, d := range f.Tensors {
		n := int64(1)
		for _, x := range d {
			n *= x
		}
		p.Params += n
	}
	if g := f.GGUF; g != nil {
		switch {
		case g.Type == "mmproj":
			p.Role = RoleLLMVision
			return p
		case g.Type == "adapter":
			p.Role = RoleLoRA
			return p
		case g.Arch == "t5" || g.Arch == "t5encoder" || g.Arch == "umt5":
			p.Role, p.Hidden, p.Vocab = RoleT5, int64(g.EmbdLen), int64(g.VocabSize)
			return p
		case g.Layers > 0 && g.Arch != "" && g.Arch != "clip":
			p.Role, p.Arch, p.Hidden, p.Vocab = RoleLLM, g.Arch, int64(g.EmbdLen), int64(g.VocabSize)
			return p
		}
	}
	if fam := Family(f); fam != "" {
		p.Family = fam
		p.Role = RoleDiffusion
		if f.hasPrefix("first_stage_model.") || f.hasPrefix("cond_stage_model.") || f.hasPrefix("conditioner.") || f.hasPrefix("text_encoders.") || f.hasPrefix("vae.") {
			p.Role = RoleCheckpoint
		}
		return p
	}
	switch {
	case f.has("lora_up") || f.has("lora_down") || f.has("lora_A.") || f.has("lora_B.") || f.has(".lora."):
		p.Role = RoleLoRA
	case f.hasPrefix("control_model.") || f.has("input_hint_block") || f.hasPrefix("controlnet_"):
		p.Role = RoleControlNet
	case f.has("RRDB_trunk") || f.has("body.0.rdb1") || (f.hasPrefix("model.0.") && f.has("model.1.sub.")):
		p.Role = RoleUpscaler
	case f.hasPrefix("decoder.layers.0.") || f.hasPrefix("taesd_decoder.") || (f.hasPrefix("decoder.") && f.has("decoder.layers.")):
		p.Role = RoleTAE
		p.Latent = inChannels(f, "decoder.layers.0.weight")
	case vaeConv(f) != "":
		p.Role = RoleVAE
		name := vaeConv(f)
		p.Latent = inChannels(f, name)
		p.Conv3D = len(f.Tensors[name]) == 5
	case f.has("text_model.embeddings.token_embedding.weight") || f.has("text_model.encoder.layers.0."):
		h := f.dim("text_model.embeddings.token_embedding.weight", 0)
		if h == 0 {
			h = f.dim("text_model.final_layer_norm.weight", 0)
		}
		p.Hidden = h
		switch h {
		case 1280:
			p.Role = RoleCLIPG
		default:
			p.Role = RoleCLIPL
		}
	case f.has("encoder.block.0.layer.0.SelfAttention.q.weight") || f.hasPrefix("enc.blk.0."):
		p.Role = RoleT5
		p.Hidden = f.dim("shared.weight", 0)
		p.Vocab = f.dim("shared.weight", 1)
		if p.Hidden == 0 {
			p.Hidden, p.Vocab = f.dim("token_embd.weight", 0), f.dim("token_embd.weight", 1)
		}
	case f.has("connector") && f.has("layers.0.self_attn"):
		p.Role = RoleConnector
	case f.has("model.layers.0.self_attn.q_proj.weight") || f.has("model.language_model.layers.0.") || f.has("language_model.model.layers.0."):
		p.Role = RoleLLM
		for _, k := range []string{"model.embed_tokens.weight", "model.language_model.embed_tokens.weight", "language_model.model.embed_tokens.weight"} {
			if h := f.dim(k, 0); h > 0 {
				p.Hidden, p.Vocab = h, f.dim(k, 1)
				break
			}
		}
		p.Arch = guessArch(f)
	case f.hasPrefix("visual.") || f.hasPrefix("vision_model.") || f.hasPrefix("vision_tower."):
		p.Role = RoleLLMVision
	}
	return p
}

// vaeConv returns the name of the VAE decoder's first convolution, if any.
func vaeConv(f *File) string {
	for _, n := range []string{
		"decoder.conv_in.weight", "first_stage_model.decoder.conv_in.weight", "vae.decoder.conv_in.weight",
		"decoder.conv1.weight", "first_stage_model.decoder.conv1.weight", // Wan / Qwen Image 3D VAE
		"decoder.conv_in.conv.weight", "decoder.conv_in.conv.weight",
	} {
		if _, ok := f.Tensors[n]; ok {
			return n
		}
	}
	return ""
}

// inChannels returns a convolution's input channels (ggml order: the
// second-outermost dimension).
func inChannels(f *File, name string) int64 {
	d := f.Tensors[name]
	if len(d) < 2 {
		return 0
	}
	return d[len(d)-2]
}

func guessArch(f *File) string {
	switch {
	case f.has("q_norm.weight") && f.hasPrefix("model.language_model."):
		return "qwen3vl"
	case f.has("q_norm.weight"):
		return "qwen3"
	case f.hasPrefix("visual.") || f.has("model.visual."):
		return "qwen2vl"
	}
	return ""
}

// llmSpec is what a recipe's text encoder label asks for.
type llmSpec struct {
	arch   string // architecture prefix ("qwen3", "qwen2", "mistral", "gemma2", "gpt-oss", …)
	hidden int64  // hidden size; 0 = any
	vision bool
}

// specFromLabel reads a recipe slot label like "Qwen 3 4B Text Encoder".
func specFromLabel(label string) llmSpec {
	l := strings.ToLower(label)
	s := llmSpec{}
	switch {
	case strings.Contains(l, "qwen 3 vl") || strings.Contains(l, "qwen3-vl"):
		s.arch, s.vision = "qwen3vl", true
	case strings.Contains(l, "qwen 3.5"):
		s.arch = "qwen35"
	case strings.Contains(l, "qwen 3"):
		s.arch = "qwen3"
	case strings.Contains(l, "qwen 2.5 vl"):
		s.arch, s.vision = "qwen2", true
	case strings.Contains(l, "ministral") || strings.Contains(l, "mistral"):
		s.arch = "mistral"
	case strings.Contains(l, "gemma 2"):
		s.arch = "gemma2"
	case strings.Contains(l, "gpt-oss"):
		s.arch = "gpt-oss"
	}
	sizes := map[string]int64{" 0.6b": 1024, " 4b": 2560, " 8b": 4096, " 7b": 3584, " 3b": 3072, " 24b": 5120, " 2b": 2304, " 20b": 2880, " 9b": 4096, " 32b": 5120}
	for k, v := range sizes {
		if strings.Contains(l, k+" ") || strings.HasSuffix(l, k) {
			s.hidden = v
		}
	}
	if s.arch == "gemma2" {
		s.hidden = 2304
	}
	return s
}

// archMatches compares a GGUF/safetensors architecture with a wanted family.
func archMatches(have, want string) bool {
	if want == "" || have == "" {
		return true
	}
	have, want = strings.ToLower(have), strings.ToLower(want)
	switch want {
	case "qwen2":
		return strings.HasPrefix(have, "qwen2")
	case "qwen3":
		return have == "qwen3" || have == "qwen3moe"
	case "qwen3vl":
		return strings.HasPrefix(have, "qwen3vl") || have == "qwen3"
	case "mistral":
		return strings.HasPrefix(have, "mistral") || have == "llama"
	}
	return strings.HasPrefix(have, want)
}

// familyWords are file-name hints that a VAE or encoder belongs to a family.
func familyWords(recipeID string) []string {
	switch {
	case strings.HasPrefix(recipeID, "flux2") || strings.HasPrefix(recipeID, "fibo2") || strings.HasPrefix(recipeID, "pid-flux2") || strings.HasPrefix(recipeID, "pid-15-flux2"):
		return []string{"flux2", "flux.2", "flux_2"}
	case strings.HasPrefix(recipeID, "flux1") || recipeID == "flex2" || strings.HasPrefix(recipeID, "chroma") || strings.HasPrefix(recipeID, "z-image") || recipeID == "pid" || recipeID == "pid-15" || recipeID == "ovis-image":
		return []string{"ae.", "ae_", "flux", "z_image", "z-image", "ultraflux"}
	case strings.HasPrefix(recipeID, "qwen-image-21"):
		return []string{"qwen_image_2", "qwen-image-2", "qi2", "qwen_image_21"}
	case strings.HasPrefix(recipeID, "qwen-image") || recipeID == "pid-qwen-image" || recipeID == "pid-15-qwen-image":
		return []string{"qwen", "wan2.1", "wan_2.1"}
	case strings.HasPrefix(recipeID, "sd3") || strings.HasPrefix(recipeID, "sd35") || recipeID == "pid-sd3" || recipeID == "pid-15-sd3":
		return []string{"sd3", "sd35", "sd3.5"}
	case strings.HasPrefix(recipeID, "sdxl") || recipeID == "cosxl-edit":
		return []string{"sdxl", "xl"}
	case strings.HasPrefix(recipeID, "sd1") || strings.HasPrefix(recipeID, "sd2") || recipeID == "sd-turbo":
		return []string{"sd", "vae-ft", "kl-f8"}
	}
	id := strings.SplitN(recipeID, "-", 2)[0]
	return []string{id}
}

// wantedLatent is the VAE latent channel count a recipe needs (0 = unknown).
func wantedLatent(recipeID string) int64 {
	switch {
	case strings.HasPrefix(recipeID, "flux2") || strings.HasPrefix(recipeID, "fibo2"):
		return 32
	case strings.HasPrefix(recipeID, "sd1") || strings.HasPrefix(recipeID, "sd2") || strings.HasPrefix(recipeID, "sdxl") || recipeID == "sd-turbo" || recipeID == "sdxs" || recipeID == "cosxl-edit" || recipeID == "instruct-pix2pix":
		return 4
	case strings.HasPrefix(recipeID, "flux1") || recipeID == "flex2" || strings.HasPrefix(recipeID, "chroma") || strings.HasPrefix(recipeID, "z-image") || strings.HasPrefix(recipeID, "sd3") || strings.HasPrefix(recipeID, "sd35") || strings.HasPrefix(recipeID, "qwen-image"):
		return 16
	}
	return 0
}

func baseLower(p string) string { return strings.ToLower(filepath.Base(p)) }
