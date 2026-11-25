// Package hw detects the hardware facts LocalChat tunes the engine for.
package hw

import (
	"runtime"

	"golang.org/x/sys/cpu"
)

// Info describes the machine. Memory figures are in bytes.
type Info struct {
	TotalRAM  uint64 `json:"totalRam"`
	AvailRAM  uint64 `json:"availRam"`
	Logical   int    `json:"logical"`
	Physical  int    `json:"physical"`
	PerfCores int    `json:"perfCores"` // physical performance cores (equals Physical on non-hybrid CPUs)
	CPUName   string `json:"cpuName"`
	AVX       bool   `json:"avx"`
	AVX2      bool   `json:"avx2"`
	AVX512    bool   `json:"avx512"`
	FMA       bool   `json:"fma"`
	OnBattery bool   `json:"onBattery"`
	OS        string `json:"os"`
}

// Detect gathers hardware information. It never fails: unknown values fall
// back to conservative defaults.
func Detect() Info {
	h := Info{
		Logical: runtime.NumCPU(),
		AVX:     cpu.X86.HasAVX,
		AVX2:    cpu.X86.HasAVX2,
		AVX512:  cpu.X86.HasAVX512F,
		FMA:     cpu.X86.HasFMA,
		OS:      runtime.GOOS,
	}
	detectPlatform(&h)
	if h.Physical <= 0 {
		h.Physical = h.Logical
		if h.Logical >= 4 {
			h.Physical = h.Logical / 2
		}
	}
	if h.PerfCores <= 0 || h.PerfCores > h.Physical {
		h.PerfCores = h.Physical
	}
	if h.TotalRAM == 0 {
		h.TotalRAM = 8 << 30
	}
	if h.AvailRAM == 0 || h.AvailRAM > h.TotalRAM {
		h.AvailRAM = h.TotalRAM / 2
	}
	return h
}

// Hybrid reports whether the CPU mixes performance and efficiency cores.
func (h Info) Hybrid() bool { return h.PerfCores < h.Physical }
