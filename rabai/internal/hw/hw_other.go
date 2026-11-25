//go:build !windows

package hw

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

func detectPlatform(h *Info) {
	if f, err := os.Open("/proc/meminfo"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 2 {
				continue
			}
			kb, _ := strconv.ParseUint(fields[1], 10, 64)
			switch fields[0] {
			case "MemTotal:":
				h.TotalRAM = kb << 10
			case "MemAvailable:":
				h.AvailRAM = kb << 10
			}
		}
		f.Close()
	}
	if f, err := os.Open("/proc/cpuinfo"); err == nil {
		cores := map[string]bool{}
		var phys, core string
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				if phys != "" || core != "" {
					cores[phys+"/"+core] = true
				}
				phys, core = "", ""
				continue
			}
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			switch k {
			case "model name":
				if h.CPUName == "" {
					h.CPUName = v
				}
			case "physical id":
				phys = v
			case "core id":
				core = v
			}
		}
		if phys != "" || core != "" {
			cores[phys+"/"+core] = true
		}
		f.Close()
		if len(cores) > 0 {
			h.Physical = len(cores)
		}
	}
}
