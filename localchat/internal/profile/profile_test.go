package profile

import (
	"slices"
	"testing"

	"localchat/internal/gguf"
	"localchat/internal/hw"
)

// llama3_8B mimics Llama 3.1 8B Instruct Q4_K_M: 32 layers, 8 KV heads, 128-dim heads.
func llama3_8B() *gguf.Info {
	return &gguf.Info{
		Arch: "llama", Name: "Meta Llama 3.1 8B Instruct", Type: "model",
		Layers: 32, HeadCount: 32, HeadCountKV: 8, KeyLen: 128, ValueLen: 128,
		CtxTrain: 131072, Params: 8_030_000_000, ParamsActive: 8_030_000_000,
		FileSize: 4_920_000_000,
	}
}

func laptop(ramGiB float64) hw.Info {
	return hw.Info{TotalRAM: uint64(ramGiB * GiB), AvailRAM: uint64(ramGiB * GiB / 2), Logical: 8, Physical: 4, PerfCores: 4, AVX2: true}
}

func TestCompute_8GB_8BModel_IsTight(t *testing.T) {
	m := llama3_8B()
	p := Compute(m, m.FileSize, 0, "", laptop(8), Prefs{}, Learned{})
	if p.Fit != "tight" && p.Fit != "too_big" {
		t.Fatalf("fit = %s, need %d usable %d", p.Fit, p.NeedBytes, p.UsableBytes)
	}
	if p.Ctx < minCtx {
		t.Fatalf("ctx = %d", p.Ctx)
	}
	if p.CacheK != "q8_0" {
		t.Fatalf("expected compressed KV on 8 GB, got %s", p.CacheK)
	}
	if p.Batch != 512 || p.UBatch != 256 {
		t.Fatalf("batch %d/%d", p.Batch, p.UBatch)
	}
}

func TestCompute_16GB_8BModel_Fits(t *testing.T) {
	m := llama3_8B()
	p := Compute(m, m.FileSize, 0, "", laptop(16), Prefs{}, Learned{})
	if p.Fit != "fits" || p.Ctx != 8192 || p.CacheK != "f16" {
		t.Fatalf("got fit=%s ctx=%d cache=%s", p.Fit, p.Ctx, p.CacheK)
	}
	if p.Threads != 4 || p.ThreadsBatch != 4 {
		t.Fatalf("threads %d/%d", p.Threads, p.ThreadsBatch)
	}
	if p.Sampling["temperature"] != 0.6 {
		t.Fatalf("llama 3 sampling not applied: %v", p.Sampling)
	}
	args := p.Args()
	if !slices.Contains(args, "8192") || !slices.Contains(args, "--no-mmproj") {
		t.Fatalf("args = %v", args)
	}
}

func TestCompute_ModelBiggerThanRAM(t *testing.T) {
	m := llama3_8B()
	m.FileSize = 12 << 30
	p := Compute(m, m.FileSize, 0, "", laptop(8), Prefs{}, Learned{})
	if p.Fit != "too_big" || p.Ctx != minCtx || p.Speed != "very_slow" {
		t.Fatalf("fit=%s ctx=%d speed=%s", p.Fit, p.Ctx, p.Speed)
	}
	if len(p.Notes) == 0 {
		t.Fatal("expected a note explaining the problem")
	}
}

func TestCompute_ReasoningModelGetsLongerContext(t *testing.T) {
	m := &gguf.Info{
		Arch: "qwen3", Name: "Qwen3 0.6B", Type: "model", Layers: 28, HeadCount: 16, HeadCountKV: 8,
		KeyLen: 128, ValueLen: 128, CtxTrain: 40960, Params: 750e6, ParamsActive: 750e6,
		FileSize: 430e6, ChatTemplate: "{% if enable_thinking %}<think>{% endif %}",
	}
	p := Compute(m, m.FileSize, 0, "", laptop(16), Prefs{}, Learned{})
	if !p.Reasoning || p.Ctx != 16384 {
		t.Fatalf("reasoning=%v ctx=%d", p.Reasoning, p.Ctx)
	}
	if p.Sampling["top_k"] != 20 || p.SamplingNoThink["top_p"] != 0.8 {
		t.Fatalf("qwen3 sampling: %v / %v", p.Sampling, p.SamplingNoThink)
	}
	if p.Speed != "fast" || p.ModelTitles {
		t.Fatalf("speed=%s titles=%v (tiny models should use simple titles)", p.Speed, p.ModelTitles)
	}
}

func TestCompute_HybridCPUThreads(t *testing.T) {
	h := hw.Info{TotalRAM: 16 * GiB, Logical: 12, Physical: 10, PerfCores: 2, AVX2: true}
	m := llama3_8B()
	p := Compute(m, m.FileSize, 0, "", h, Prefs{}, Learned{})
	if p.Threads != 2 || p.ThreadsBatch != 10 {
		t.Fatalf("threads %d/%d", p.Threads, p.ThreadsBatch)
	}
	p = Compute(m, m.FileSize, 0, "", h, Prefs{Threads: 6}, Learned{})
	if p.Threads != 6 || p.ThreadsBatch != 10 {
		t.Fatalf("override threads %d/%d", p.Threads, p.ThreadsBatch)
	}
}

func TestCompute_MoEIsFasterThanDenseOfSameSize(t *testing.T) {
	dense := llama3_8B()
	moe := llama3_8B()
	moe.ParamsActive = moe.Params / 8
	pd := Compute(dense, dense.FileSize, 0, "", laptop(16), Prefs{}, Learned{})
	pm := Compute(moe, moe.FileSize, 0, "", laptop(16), Prefs{}, Learned{})
	if pm.EstTPS < pd.EstTPS*7 {
		t.Fatalf("moe %.1f vs dense %.1f", pm.EstTPS, pd.EstTPS)
	}
}

func TestCompute_LearnedValuesWin(t *testing.T) {
	m := llama3_8B()
	p := Compute(m, m.FileSize, 0, "", laptop(16), Prefs{}, Learned{TokensPerSec: 3.3, KVBytesPerToken: 1})
	if p.EstTPS != 3.3 || p.Speed != "slow" {
		t.Fatalf("est=%v speed=%s", p.EstTPS, p.Speed)
	}
	// With a tiny measured KV size the "long" preset reaches its full target.
	p = Compute(m, m.FileSize, 0, "", laptop(16), Prefs{Preset: "long"}, Learned{KVBytesPerToken: 1})
	if p.Ctx != 32768 {
		t.Fatalf("ctx = %d", p.Ctx)
	}
}

func TestCompute_VisionAddsProjector(t *testing.T) {
	m := llama3_8B()
	p := Compute(m, m.FileSize, 600<<20, "C:/m/mmproj.gguf", laptop(16), Prefs{}, Learned{})
	if !p.Vision || p.ImageMaxTokens != 512 {
		t.Fatalf("vision=%v max=%d", p.Vision, p.ImageMaxTokens)
	}
	if !slices.Contains(p.Args(), "--mmproj") {
		t.Fatal("missing --mmproj")
	}
}
