// Package accel finds graphics cards the engines can use and decides, by
// measuring, whether a model runs better on the graphics card or the CPU.
//
// Weak or badly-behaved GPUs are common on laptops: an old driver can crash,
// compute wrong results, or simply be slower than the CPU once a model
// doesn't fit in video memory. So nothing here assumes the GPU helps: a model
// uses it only after a short test run shows correct output that is faster.
package accel

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"rabai/internal/engine"
)

// Device is one GPU as the engine reports it.
type Device struct {
	Name       string `json:"name"`     // engine name, e.g. "Vulkan0"
	Desc       string `json:"desc"`     // e.g. "NVIDIA GeForce MX450"
	TotalMiB   int    `json:"totalMiB"` // video memory (shared memory for integrated GPUs)
	FreeMiB    int    `json:"freeMiB"`
	Integrated bool   `json:"integrated"` // shares system memory with the CPU
}

// Label is a short description for people.
func (d Device) Label() string {
	if d.Desc != "" {
		return d.Desc
	}
	return d.Name
}

var devLine = regexp.MustCompile(`^\s*([A-Za-z]+\d+):\s*(.*?)\s*\((\d+) MiB, (\d+) MiB free\)\s*$`)

// ParseDevices reads `llama-server --list-devices` output.
func ParseDevices(out string) []Device {
	var devs []Device
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		m := devLine.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		total, _ := strconv.Atoi(m[3])
		free, _ := strconv.Atoi(m[4])
		d := Device{Name: m[1], Desc: m[2], TotalMiB: total, FreeMiB: free}
		d.Integrated = IsIntegrated(d.Desc)
		// Only GPU backends matter here (the CPU is always available).
		if strings.HasPrefix(strings.ToLower(d.Name), "cpu") {
			continue
		}
		devs = append(devs, d)
	}
	return devs
}

// IsIntegrated guesses from the name whether a GPU is built into the CPU.
func IsIntegrated(desc string) bool {
	d := strings.ToLower(desc)
	switch {
	case IsSoftware(desc):
		return true
	case strings.Contains(d, "intel"):
		// UHD, Iris Xe and "Arc Graphics" in Core Ultra CPUs; the discrete
		// cards are named Arc A3xx–A7xx / B5xx.
		return !intelDiscrete.MatchString(d)
	case strings.Contains(d, "amd") || strings.Contains(d, "radeon"):
		// APUs are called "Radeon(TM) Graphics", "Radeon 780M Graphics",
		// "Radeon Vega 8 Graphics"; cards are "RX …", "Pro …".
		return strings.Contains(d, "graphics") && !amdDiscrete.MatchString(d)
	}
	return false
}

var (
	intelDiscrete = regexp.MustCompile(`arc(\(tm\))?\s+[ab]\d{3}`)
	amdDiscrete   = regexp.MustCompile(`\brx\b|\bpro\b|radeon\s+vii|\bw\d{4}`)
)

// IsSoftware reports a CPU-emulated "GPU", which is never worth using.
func IsSoftware(desc string) bool {
	d := strings.ToLower(desc)
	return strings.Contains(d, "llvmpipe") || strings.Contains(d, "lavapipe") || strings.Contains(d, "swiftshader") || strings.Contains(d, "microsoft basic")
}

// ListDevices asks the engine which GPUs it can use. A broken driver can make
// this crash or hang, so it runs in its own process with a time limit; any
// failure means "no usable GPU".
func ListDevices(ctx context.Context, exe string, env []string) ([]Device, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--list-devices")
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	engine.PrepareCmd(cmd)
	err := cmd.Run()
	devs := ParseDevices(out.String())
	if err != nil && len(devs) == 0 {
		return nil, err
	}
	return devs, nil
}

// Best picks the GPU most likely to help: the one with the most dedicated
// memory, preferring dedicated cards over integrated ones. Software renderers
// are ignored. ok is false when there is nothing worth trying.
func Best(devs []Device) (Device, bool) {
	var c []Device
	allowSoft := os.Getenv("RABAI_GPU_ALLOW_SOFTWARE") != "" // tests on machines without a GPU
	for _, d := range devs {
		if allowSoft || !IsSoftware(d.Desc) {
			c = append(c, d)
		}
	}
	if len(c) == 0 {
		return Device{}, false
	}
	sort.SliceStable(c, func(i, j int) bool {
		if c[i].Integrated != c[j].Integrated {
			return !c[i].Integrated
		}
		return c[i].TotalMiB > c[j].TotalMiB
	})
	return c[0], true
}

// Mode is how a model runs.
const (
	ModeCPU     = "cpu"
	ModeGPU     = "gpu"
	ModeGPUSafe = "gpu_safe" // GPU with driver-sensitive features turned off
)

// SafeEnv turns off the GPU features most often broken by old drivers.
var SafeEnv = []string{
	"GGML_VK_DISABLE_COOPMAT=1",
	"GGML_VK_DISABLE_COOPMAT2=1",
	"GGML_VK_DISABLE_INTEGER_DOT_PRODUCT=1",
	"GGML_VK_DISABLE_BFLOAT16=1",
	"GGML_VK_DISABLE_F16=1",
}

// GPUEnv makes the engine load the GPU backend from dll. The backend lives
// outside the engine's folder so that runs on the processor never load it
// and a broken graphics driver can't affect them.
func GPUEnv(dll string, extra ...string) []string {
	return append([]string{"GGML_BACKEND_PATH=" + dll}, extra...)
}

// Bench is one measured run.
type Bench struct {
	PromptTPS float64 `json:"promptTps"` // prompt tokens per second
	GenTPS    float64 `json:"genTps"`    // generated tokens per second
	Text      string  `json:"text"`      // greedy output, to compare devices
	Seconds   float64 `json:"seconds"`   // time for a typical turn (see TurnSeconds)
}

// TurnSeconds estimates the wait for a typical chat turn: reading a 600-token
// prompt and writing a 250-token reply.
func TurnSeconds(promptTPS, genTPS float64) float64 {
	if promptTPS <= 0 || genTPS <= 0 {
		return 1e9
	}
	return 600/promptTPS + 250/genTPS
}

// Decision is what was learned about a model on this PC.
type Decision struct {
	Device  string    `json:"device"` // GPU description it applies to
	Mode    string    `json:"mode"`   // ModeCPU, ModeGPU or ModeGPUSafe
	CPU     *Bench    `json:"cpu,omitempty"`
	GPU     *Bench    `json:"gpu,omitempty"`
	Reason  string    `json:"reason"` // for the settings screen
	Checked time.Time `json:"checked"`
}

// Agree reports whether two greedy outputs from the same prompt are the same
// computation. Numerical differences between devices may make long outputs
// drift apart, so only the start must match; a broken driver produces
// unrelated text or garbage from the first token.
func Agree(cpu, gpu string) bool {
	a, b := norm(cpu), norm(gpu)
	if a == "" || b == "" {
		return a == b
	}
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	short := min(len(a), len(b))
	if n >= 24 || n >= short*3/4 {
		return true
	}
	// Fall back to word overlap for the first dozen words.
	wa, wb := firstWords(a, 12), firstWords(b, 12)
	if len(wa) == 0 || len(wb) == 0 {
		return false
	}
	set := map[string]bool{}
	for _, w := range wa {
		set[w] = true
	}
	hit := 0
	for _, w := range wb {
		if set[w] {
			hit++
		}
	}
	return hit*10 >= len(wb)*7
}

// Garbage reports output that no working model produces: empty, replacement
// characters, or one short pattern repeated over and over.
func Garbage(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return true
	}
	if strings.Count(t, "�") > 2 {
		return true
	}
	if len([]rune(t)) >= 24 {
		uniq := map[rune]bool{}
		for _, r := range t {
			uniq[r] = true
		}
		if len(uniq) <= 3 {
			return true
		}
	}
	return false
}

func norm(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

func firstWords(s string, n int) []string {
	w := strings.Fields(s)
	if len(w) > n {
		w = w[:n]
	}
	return w
}

// Choose decides from the measurements. The GPU must produce the same text
// and be clearly faster (by 15%) to be worth its power use and the risk of
// driver trouble.
func Choose(cpu, gpu *Bench) (mode string, reason string) {
	switch {
	case gpu == nil:
		return ModeCPU, "The graphics card couldn't run this model, so it uses the processor."
	case cpu == nil:
		return ModeGPU, "Runs on the graphics card."
	case Garbage(gpu.Text) || !Agree(cpu.Text, gpu.Text):
		return ModeCPU, "The graphics card gave wrong results with this model (often an old driver), so it uses the processor."
	case gpu.Seconds*1.15 < cpu.Seconds:
		return ModeGPU, fmt.Sprintf("The graphics card is %.1f× faster for this model.", cpu.Seconds/gpu.Seconds)
	case cpu.Seconds < gpu.Seconds:
		return ModeCPU, "The processor is faster than the graphics card for this model, so it uses the processor."
	}
	return ModeCPU, "The processor is about as fast as the graphics card for this model, so it uses the processor."
}
