package accel

import "testing"

func TestParseDevices(t *testing.T) {
	out := `ggml_vulkan: Found 2 Vulkan devices:
Available devices:
  Vulkan0: NVIDIA GeForce MX450 (2048 MiB, 1830 MiB free)
  Vulkan1: Intel(R) Iris(R) Xe Graphics (8051 MiB, 7900 MiB free)
`
	d := ParseDevices(out)
	if len(d) != 2 || d[0].Name != "Vulkan0" || d[0].TotalMiB != 2048 || d[0].FreeMiB != 1830 || d[0].Integrated {
		t.Fatalf("got %+v", d)
	}
	if !d[1].Integrated {
		t.Fatalf("Iris Xe should be integrated")
	}
	best, ok := Best(d)
	if !ok || best.Name != "Vulkan0" {
		t.Fatalf("best = %+v", best)
	}
	if _, ok := Best(ParseDevices("Available devices:\n  (none)\n")); ok {
		t.Fatal("no devices should give no best")
	}
	if _, ok := Best(ParseDevices("  Vulkan0: llvmpipe (LLVM 17.0.6, 256 bits) (32000 MiB, 30000 MiB free)\n")); ok {
		t.Fatal("software renderer must not be used")
	}
}

func TestIsIntegrated(t *testing.T) {
	for desc, want := range map[string]bool{
		"NVIDIA GeForce GTX 1650":            false,
		"Intel(R) UHD Graphics 620":          true,
		"Intel(R) Arc(TM) Graphics":          true,
		"Intel(R) Arc(TM) A770 Graphics":     false,
		"AMD Radeon(TM) Graphics":            true,
		"AMD Radeon 780M Graphics":           true,
		"AMD Radeon RX 6600":                 false,
		"AMD Radeon RX 7900 XTX":             false,
		"Radeon Vega 8 Graphics":             true,
		"NVIDIA GeForce RTX 4060 Laptop GPU": false,
	} {
		if got := IsIntegrated(desc); got != want {
			t.Errorf("%s: got %v want %v", desc, got, want)
		}
	}
}

func TestAgreeAndChoose(t *testing.T) {
	cpu := &Bench{Text: " evaporation, condensation and precipitation. Evaporation is", Seconds: 40}
	same := &Bench{Text: " evaporation, condensation and precipitation. Evaporation happens", Seconds: 10}
	garbage := &Bench{Text: "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!", Seconds: 5}
	other := &Bench{Text: " the purple elephant dances wildly in Tokyo tonight forever", Seconds: 5}
	slow := &Bench{Text: cpu.Text, Seconds: 38}

	if m, _ := Choose(cpu, same); m != ModeGPU {
		t.Errorf("faster, same text: %s", m)
	}
	if m, _ := Choose(cpu, garbage); m != ModeCPU {
		t.Errorf("garbage: %s", m)
	}
	if m, _ := Choose(cpu, other); m != ModeCPU {
		t.Errorf("different text: %s", m)
	}
	if m, _ := Choose(cpu, slow); m != ModeCPU {
		t.Errorf("not clearly faster: %s", m)
	}
	if m, _ := Choose(cpu, nil); m != ModeCPU {
		t.Errorf("gpu failed: %s", m)
	}
	if m, _ := Choose(nil, same); m != ModeGPU {
		t.Errorf("cpu failed: %s", m)
	}
}
