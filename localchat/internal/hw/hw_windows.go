//go:build windows

package hw

import (
	"encoding/binary"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	kernel32                             = windows.NewLazySystemDLL("kernel32.dll")
	procGetLogicalProcessorInformationEx = kernel32.NewProc("GetLogicalProcessorInformationEx")
	procGetSystemPowerStatus             = kernel32.NewProc("GetSystemPowerStatus")
	procGlobalMemoryStatusEx             = kernel32.NewProc("GlobalMemoryStatusEx")
)

// memoryStatusEx mirrors MEMORYSTATUSEX.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

const relationProcessorCore = 0

func detectPlatform(h *Info) {
	var ms memoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms))); r != 0 {
		h.TotalRAM = ms.TotalPhys
		h.AvailRAM = ms.AvailPhys
	}

	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE); err == nil {
		if s, _, err := k.GetStringValue("ProcessorNameString"); err == nil {
			h.CPUName = strings.Join(strings.Fields(s), " ")
		}
		k.Close()
	}

	h.Physical, h.PerfCores = coreCounts()

	// SYSTEM_POWER_STATUS: ACLineStatus(1) BatteryFlag(1) BatteryLifePercent(1) ...
	var ps [12]byte
	if r, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&ps[0]))); r != 0 {
		h.OnBattery = ps[0] == 0
	}
}

// coreCounts returns the number of physical cores and of performance cores,
// using the EfficiencyClass reported for each core on Windows 10+.
func coreCounts() (physical, perf int) {
	if procGetLogicalProcessorInformationEx.Find() != nil {
		return 0, 0
	}
	var size uint32
	procGetLogicalProcessorInformationEx.Call(relationProcessorCore, 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 || size > 1<<24 {
		return 0, 0
	}
	buf := make([]byte, size)
	r, _, _ := procGetLogicalProcessorInformationEx.Call(relationProcessorCore, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		return 0, 0
	}
	classes := map[byte]int{}
	maxClass := byte(0)
	for off := uint32(0); off+10 <= size; {
		rel := binary.LittleEndian.Uint32(buf[off:])
		recSize := binary.LittleEndian.Uint32(buf[off+4:])
		if recSize == 0 {
			break
		}
		if rel == relationProcessorCore {
			class := buf[off+9] // PROCESSOR_RELATIONSHIP.EfficiencyClass
			classes[class]++
			if class > maxClass {
				maxClass = class
			}
			physical++
		}
		off += recSize
	}
	perf = classes[maxClass]
	if len(classes) <= 1 {
		perf = physical
	}
	return physical, perf
}
