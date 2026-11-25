// Package profile turns a model's metadata and the machine's hardware into
// llama-server settings that fit in memory and run as fast as the CPU allows.
package profile

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"rabai/internal/gguf"
	"rabai/internal/hw"
)

const (
	MiB = 1 << 20
	GiB = 1 << 30

	minCtx = 2048
	// q8_0 stores 32 values in 34 bytes; f16 stores them in 64.
	q8Ratio = 34.0 / 64.0
	// Fallback memory bandwidth (GB/s) for laptops with unknown DDR config;
	// deliberately conservative (single-channel DDR4 achieves ~12-15 GB/s).
	defaultBandwidth = 12.0
)

// Prefs are user choices that steer the automatic profile.
type Prefs struct {
	Preset  string `json:"preset"`  // "balanced" (default), "fast" (smaller context), "long" (more context)
	Ctx     int    `json:"ctx"`     // 0 = automatic
	Threads int    `json:"threads"` // 0 = automatic
	KVCache string `json:"kvCache"` // "auto", "f16", "q8_0"
}

// Learned holds measurements from earlier runs of this model on this PC.
type Learned struct {
	KVBytesPerToken float64 `json:"kvBytesPerToken"` // f16-equivalent, measured by the engine
	TokensPerSec    float64 `json:"tokensPerSec"`    // median generation speed
	BandwidthGBs    float64 `json:"bandwidthGBs"`    // machine-wide calibration
}

// Profile is the computed configuration plus what the UI shows about it.
type Profile struct {
	Ctx             int                `json:"ctx"`
	CacheK          string             `json:"cacheK"`
	CacheV          string             `json:"cacheV"`
	Threads         int                `json:"threads"`
	ThreadsBatch    int                `json:"threadsBatch"`
	Batch           int                `json:"batch"`
	UBatch          int                `json:"ubatch"`
	CacheRAMMiB     int                `json:"cacheRamMiB"`
	ImageMaxTokens  int                `json:"imageMaxTokens"`
	MMProj          string             `json:"mmproj,omitempty"`
	Fit             string             `json:"fit"` // fits | tight | too_big
	NeedBytes       uint64             `json:"needBytes"`
	UsableBytes     uint64             `json:"usableBytes"`
	EstTPS          float64            `json:"estTps"`
	Speed           string             `json:"speed"` // fast | good | slow | very_slow
	Reasoning       bool               `json:"reasoning"`
	Vision          bool               `json:"vision"`
	Sampling        map[string]float64 `json:"sampling,omitempty"`
	SamplingNoThink map[string]float64 `json:"samplingNoThink,omitempty"`
	ModelTitles     bool               `json:"modelTitles"`
	Notes           []string           `json:"notes,omitempty"`
}

// Compute derives the profile. totalSize is the model's size on disk (all
// split parts); mmprojSize is the size of a paired vision projector or 0.
func Compute(m *gguf.Info, totalSize, mmprojSize int64, mmproj string, h hw.Info, p Prefs, l Learned) Profile {
	var pr Profile
	pr.MMProj = mmproj
	pr.Vision = mmproj != ""
	pr.Reasoning = DetectReasoning(m)

	size := float64(totalSize)
	if size <= 0 {
		size = float64(m.FileSize)
	}
	total := float64(h.TotalRAM)
	reserve := math.Max(3*GiB, total*0.3) // Windows 11, the UI and other apps
	usable := math.Max(total-reserve, 1*GiB)
	pr.UsableBytes = uint64(usable)

	overhead := 256*MiB + 0.04*size + float64(mmprojSize)*1.3

	kvF16 := l.KVBytesPerToken
	if kvF16 <= 0 {
		kvF16 = m.KVBytesPerToken(2)
	}
	if kvF16 <= 0 {
		kvF16 = 4096 // recurrent/hybrid models with tiny per-token state
	}

	// Target context: enough for normal chats, more for models that think.
	target := 8192
	switch p.Preset {
	case "fast":
		target = 4096
	case "long":
		target = 32768
	}
	if pr.Reasoning && p.Preset != "long" {
		target *= 2
	}
	if m.CtxTrain > 0 && target > int(m.CtxTrain) {
		target = int(m.CtxTrain)
	}
	if target < minCtx {
		target = minCtx
	}

	budget := usable - size - overhead
	fitsAt := func(ctx int, perTok float64) bool { return float64(ctx)*perTok <= budget }

	cache := "f16"
	ctx := target
	switch {
	case p.Ctx > 0:
		ctx = p.Ctx
		if p.KVCache == "" || p.KVCache == "auto" {
			if !fitsAt(ctx, kvF16) {
				cache = "q8_0"
			}
		}
	case fitsAt(target, kvF16):
	case fitsAt(target, kvF16*q8Ratio):
		cache = "q8_0"
		pr.Notes = append(pr.Notes, "Conversation memory is compressed (q8_0) so the full context fits in RAM.")
	default:
		cache = "q8_0"
		ctx = minCtx
		for c := target / 2; c >= minCtx; c /= 2 {
			if fitsAt(c, kvF16*q8Ratio) {
				ctx = c
				break
			}
		}
		pr.Notes = append(pr.Notes, fmt.Sprintf("Context limited to %s tokens to fit in memory.", shortK(ctx)))
	}
	if p.KVCache == "f16" || p.KVCache == "q8_0" {
		cache = p.KVCache
	}
	pr.Ctx = roundCtx(ctx)
	pr.CacheK, pr.CacheV = cache, cache

	perTok := kvF16
	if cache == "q8_0" {
		perTok *= q8Ratio
	}
	need := size + overhead + float64(pr.Ctx)*perTok
	pr.NeedBytes = uint64(need)
	switch {
	case need <= usable:
		pr.Fit = "fits"
	case need <= total-1.5*GiB:
		pr.Fit = "tight"
		pr.Notes = append(pr.Notes, "Close other apps for the best speed: this model needs most of your memory.")
	default:
		pr.Fit = "too_big"
		pr.Notes = append(pr.Notes, fmt.Sprintf("This model needs about %.1f GB but this PC has %.1f GB. It will run very slowly; a smaller or more compressed version (for example Q4_K_M) will work better.", need/GiB, total/GiB))
	}

	// Threads: generation is memory-bound and suffers from slow efficiency
	// cores; prompt processing is compute-bound and benefits from all cores.
	pr.Threads = h.PerfCores
	if pr.Threads > 8 {
		pr.Threads = 8
	}
	if pr.Threads < 1 {
		pr.Threads = 1
	}
	pr.ThreadsBatch = h.Physical
	if pr.ThreadsBatch > 16 {
		pr.ThreadsBatch = 16
	}
	if pr.ThreadsBatch < pr.Threads {
		pr.ThreadsBatch = pr.Threads
	}
	if p.Threads > 0 {
		pr.Threads, pr.ThreadsBatch = p.Threads, max(p.Threads, pr.ThreadsBatch)
	}

	switch {
	case total <= 8.5*GiB:
		pr.Batch, pr.UBatch = 512, 256
	case total <= 16.5*GiB:
		pr.Batch, pr.UBatch = 1024, 512
	default:
		pr.Batch, pr.UBatch = 2048, 512
	}

	leftover := usable - need
	if leftover > 0 {
		cram := int(leftover * 0.5 / MiB)
		if cram > 2048 {
			cram = 2048
		}
		if cram >= 256 {
			pr.CacheRAMMiB = cram
		}
	}

	if pr.Vision {
		pr.ImageMaxTokens = 1024
		if h.Physical <= 4 {
			pr.ImageMaxTokens = 512
		}
	}

	// Speed: tokens/s ≈ memory bandwidth ÷ bytes read per token.
	bw := l.BandwidthGBs
	if bw <= 0 {
		bw = defaultBandwidth
		if !h.AVX2 {
			bw = 8
		}
	}
	bytesPerTok := size
	if m.Params > 0 && m.ParamsActive > 0 && m.ParamsActive < m.Params {
		bytesPerTok = size * float64(m.ParamsActive) / float64(m.Params)
	}
	pr.EstTPS = bw * 1e9 / math.Max(bytesPerTok, 1)
	if l.TokensPerSec > 0 {
		pr.EstTPS = l.TokensPerSec
	}
	if pr.Fit == "too_big" {
		pr.EstTPS = math.Min(pr.EstTPS, 0.5)
	}
	pr.Speed = SpeedLabel(pr.EstTPS)

	pr.Sampling, pr.SamplingNoThink = recommendedSampling(m)
	// Tiny models write poor titles; slow PCs shouldn't spend time on them.
	pr.ModelTitles = pr.EstTPS >= 8 && pr.CacheRAMMiB > 0 && m.Params >= 1_500_000_000
	return pr
}

// BandwidthFromRun calibrates the machine's effective memory bandwidth from a
// measured generation speed of a model of the given size.
func BandwidthFromRun(m *gguf.Info, totalSize int64, tps float64) float64 {
	bytesPerTok := float64(totalSize)
	if m.Params > 0 && m.ParamsActive > 0 && m.ParamsActive < m.Params {
		bytesPerTok = bytesPerTok * float64(m.ParamsActive) / float64(m.Params)
	}
	return tps * bytesPerTok / 1e9
}

// SpeedLabel buckets an expected generation speed.
func SpeedLabel(tps float64) string {
	switch {
	case tps >= 12:
		return "fast"
	case tps >= 5:
		return "good"
	case tps >= 2:
		return "slow"
	}
	return "very_slow"
}

// DetectReasoning guesses from the chat template whether the model can think
// before answering. The engine's /props capabilities refine this after load.
func DetectReasoning(m *gguf.Info) bool {
	if strings.HasPrefix(m.Arch, "gpt-oss") {
		return true
	}
	t := m.ChatTemplate
	for _, s := range []string{"enable_thinking", "<think>", "reasoning_effort", "<|channel|>analysis", "thinking_budget", "<seed:think>", "[THINK]"} {
		if strings.Contains(t, s) {
			return true
		}
	}
	return false
}

// recommendedSampling returns model-card sampling settings for well-known
// families when the GGUF does not carry general.sampling.* itself.
func recommendedSampling(m *gguf.Info) (think, noThink map[string]float64) {
	if len(m.Sampling) > 0 {
		out := map[string]float64{}
		for k, v := range m.Sampling {
			switch k {
			case "temp":
				out["temperature"] = v
			case "top_k", "top_p", "min_p":
				out[k] = v
			case "penalty_repeat":
				out["repeat_penalty"] = v
			}
		}
		return out, nil
	}
	name := strings.ToLower(m.Name)
	switch {
	case m.Arch == "qwen3" || m.Arch == "qwen3moe":
		return map[string]float64{"temperature": 0.6, "top_p": 0.95, "top_k": 20, "min_p": 0},
			map[string]float64{"temperature": 0.7, "top_p": 0.8, "top_k": 20, "min_p": 0}
	case strings.HasPrefix(m.Arch, "gemma3"):
		return map[string]float64{"temperature": 1.0, "top_k": 64, "top_p": 0.95, "min_p": 0}, nil
	case strings.HasPrefix(m.Arch, "gpt-oss"):
		return map[string]float64{"temperature": 1.0, "top_p": 1.0, "top_k": 0, "min_p": 0}, nil
	case strings.Contains(name, "r1") && strings.Contains(name, "distill"):
		return map[string]float64{"temperature": 0.6, "top_p": 0.95}, nil
	case strings.Contains(name, "mistral small") || strings.Contains(name, "mistral-small"):
		return map[string]float64{"temperature": 0.15}, nil
	case m.Arch == "llama" && strings.Contains(name, "llama 3"):
		return map[string]float64{"temperature": 0.6, "top_p": 0.9}, nil
	}
	return nil, nil
}

// Args renders the llama-server command-line flags for the profile.
func (pr Profile) Args() []string {
	a := []string{
		"-c", strconv.Itoa(pr.Ctx),
		"-t", strconv.Itoa(pr.Threads),
		"-tb", strconv.Itoa(pr.ThreadsBatch),
		"-b", strconv.Itoa(pr.Batch),
		"-ub", strconv.Itoa(pr.UBatch),
		"-ctk", pr.CacheK,
		"-ctv", pr.CacheV,
		"-cram", strconv.Itoa(pr.CacheRAMMiB),
	}
	if pr.MMProj != "" {
		a = append(a, "--mmproj", pr.MMProj)
		if pr.ImageMaxTokens > 0 {
			a = append(a, "--image-max-tokens", strconv.Itoa(pr.ImageMaxTokens))
		}
	} else {
		a = append(a, "--no-mmproj")
	}
	return a
}

func roundCtx(c int) int {
	if c < minCtx {
		return minCtx
	}
	return c / 256 * 256
}

func shortK(c int) string {
	if c >= 1024 {
		return fmt.Sprintf("%dK", c/1024)
	}
	return strconv.Itoa(c)
}
