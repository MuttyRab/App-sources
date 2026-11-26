package imagemodel

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fakeSafetensors writes a safetensors file whose header lists the given
// tensors (shapes outermost first, as PyTorch stores them) and no data.
func fakeSafetensors(t *testing.T, path string, tensors map[string][]int64) {
	t.Helper()
	hdr := map[string]any{}
	for n, s := range tensors {
		hdr[n] = map[string]any{"dtype": "F16", "shape": s, "data_offsets": []int{0, 0}}
	}
	b, _ := json.Marshal(hdr)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var n [8]byte
	binary.LittleEndian.PutUint64(n[:], uint64(len(b)))
	f.Write(n[:])
	f.Write(b)
}

func TestFluxComfyLayout(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "models", "unet", "flux1-schnell-fp8.safetensors")
	fakeSafetensors(t, main, map[string][]int64{
		"double_blocks.0.img_attn.qkv.weight": {9216, 3072},
		"img_in.weight":                       {3072, 64},
		"txt_in.weight":                       {3072, 4096},
	})
	fakeSafetensors(t, filepath.Join(root, "models", "clip", "clip_l.safetensors"), map[string][]int64{
		"text_model.embeddings.token_embedding.weight": {49408, 768},
		"text_model.encoder.layers.0.mlp.fc1.weight":   {3072, 768},
	})
	fakeSafetensors(t, filepath.Join(root, "models", "clip", "t5xxl_fp8_e4m3fn.safetensors"), map[string][]int64{
		"encoder.block.0.layer.0.SelfAttention.q.weight": {4096, 4096},
		"shared.weight": {32128, 4096},
	})
	fakeSafetensors(t, filepath.Join(root, "models", "vae", "ae.safetensors"), map[string][]int64{
		"decoder.conv_in.weight": {512, 16, 3, 3},
	})
	fakeSafetensors(t, filepath.Join(root, "models", "vae", "sdxl_vae.safetensors"), map[string][]int64{
		"decoder.conv_in.weight": {512, 4, 3, 3},
	})

	f, err := ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	p := Classify(f)
	if p.Role != RoleDiffusion || p.Family != FamFlux {
		t.Fatalf("classified as %+v", p)
	}
	id := ChooseRecipe(p.Family, main, p.Params)
	if id != "flux1-schnell" {
		t.Fatalf("recipe %s", id)
	}
	r := RecipeByID(id)
	res := Resolve(p, f, r, Candidates(main, nil), nil)
	if len(res.Missing) != 0 {
		t.Fatalf("missing %+v (parts %+v)", res.Missing, res.Parts)
	}
	for slot, want := range map[string]string{"clip_l": "clip_l.safetensors", "t5xxl": "t5xxl_fp8_e4m3fn.safetensors", "vae": "ae.safetensors"} {
		if filepath.Base(res.Parts[slot]) != want {
			t.Errorf("%s = %s, want %s", slot, res.Parts[slot], want)
		}
	}
	s := DefaultSettings(r, main)
	if s.Steps != 4 || s.CFG != 1 || s.Width != 1024 {
		t.Errorf("settings %+v", s)
	}
}

func TestZImageMissingEncoder(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "z_image_turbo-Q4_K.safetensors")
	fakeSafetensors(t, main, map[string][]int64{
		"cap_embedder.0.weight":         {2560},
		"layers.0.attention.qkv.weight": {11520, 3840},
	})
	f, _ := ReadFile(main)
	p := Classify(f)
	if p.Family != FamZImage {
		t.Fatalf("family %q", p.Family)
	}
	r := RecipeByID(ChooseRecipe(p.Family, main, p.Params))
	if r == nil || r.ID != "z-image-turbo" {
		t.Fatalf("recipe %+v", r)
	}
	res := Resolve(p, f, r, Candidates(main, nil), nil)
	if len(res.Missing) != 2 {
		t.Fatalf("want the text encoder and VAE missing, got %+v", res.Missing)
	}
	// Add a Qwen3-4B encoder (and a Qwen3-8B that must not be chosen) and the VAE.
	fakeSafetensors(t, filepath.Join(dir, "qwen_3_8b.safetensors"), map[string][]int64{
		"model.embed_tokens.weight":              {151936, 4096},
		"model.layers.0.self_attn.q_proj.weight": {4096, 4096},
		"model.layers.0.self_attn.q_norm.weight": {128},
	})
	fakeSafetensors(t, filepath.Join(dir, "qwen_3_4b.safetensors"), map[string][]int64{
		"model.embed_tokens.weight":              {151936, 2560},
		"model.layers.0.self_attn.q_proj.weight": {4096, 2560},
		"model.layers.0.self_attn.q_norm.weight": {128},
	})
	fakeSafetensors(t, filepath.Join(dir, "ae.safetensors"), map[string][]int64{"decoder.conv_in.weight": {512, 16, 3, 3}})
	res = Resolve(p, f, r, Candidates(main, nil), nil)
	if len(res.Missing) != 0 || filepath.Base(res.Parts["llm"]) != "qwen_3_4b.safetensors" {
		t.Fatalf("resolution %+v", res)
	}
}

func TestSDTurboRealFile(t *testing.T) {
	path := os.Getenv("RABAI_TEST_SDTURBO")
	if path == "" {
		t.Skip("set RABAI_TEST_SDTURBO to an SD-Turbo GGUF")
	}
	f, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p := Classify(f)
	if p.Role != RoleCheckpoint || p.Family != FamSD2 {
		t.Fatalf("got %+v", p)
	}
	if id := ChooseRecipe(p.Family, path, p.Params); id != "sd-turbo" {
		t.Fatalf("recipe %s", id)
	}
}

func TestTAE(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "taesd.safetensors")
	fakeSafetensors(t, path, map[string][]int64{"decoder.layers.0.weight": {64, 4, 3, 3}, "encoder.layers.0.weight": {64, 3, 3, 3}})
	p, err := Inspect(path)
	if err != nil || p.Role != RoleTAE || p.Latent != 4 {
		t.Fatalf("got %+v %v", p, err)
	}
}

func TestEveryRecipeLoads(t *testing.T) {
	for _, id := range []string{"sd1-checkpoint", "sd-turbo", "sdxl-turbo", "flux1-dev", "z-image-turbo", "qwen-image", "flux2-klein-4b"} {
		if RecipeByID(id) == nil {
			t.Errorf("missing recipe %s", id)
		}
	}
}

func TestNameFromFile(t *testing.T) {
	for in, want := range map[string]string{
		"sd_turbo_q8_0.gguf":                      "SD Turbo",
		"stable-diffusion-xl-1.0-turbo-Q4_0.gguf": "Stable Diffusion XL 1.0 Turbo",
		"z_image_turbo-Q3_K.gguf":                 "Z Image Turbo",
		"flux1-schnell-fp8.safetensors":           "FLUX1 Schnell",
		"DreamShaper_8_pruned.safetensors":        "DreamShaper 8",
	} {
		if got := nameFromFile(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestTurboFineTuneSettings(t *testing.T) {
	r := RecipeByID("sdxl-turbo")
	if s := DefaultSettings(r, "sd_xl_turbo_1.0_fp16.safetensors"); s.Steps != 1 || s.Width != 512 {
		t.Errorf("official SDXL Turbo: %+v", s)
	}
	if s := DefaultSettings(r, "dreamshaper-xl-v2-turbo-Q4_K.gguf"); s.Steps != 6 || s.CFG != 1.5 || s.Width != 1024 {
		t.Errorf("DreamShaper XL Turbo: %+v", s)
	}
	if s := DefaultSettings(RecipeByID("sd1-checkpoint"), "DreamShaper8_LCM_q8_0.gguf"); s.Steps != 6 || s.Sampler != "lcm" {
		t.Errorf("LCM: %+v", s)
	}
}
