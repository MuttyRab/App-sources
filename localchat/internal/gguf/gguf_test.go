package gguf

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// testModelDir points at real GGUF files used for integration-style tests.
// Tests that need them are skipped when the directory is absent.
func testModelDir(t *testing.T) string {
	dir := os.Getenv("LOCALCHAT_TEST_MODELS")
	if dir == "" {
		t.Skip("LOCALCHAT_TEST_MODELS not set")
	}
	return dir
}

func within(got, want uint64, pct float64) bool {
	d := float64(got) - float64(want)
	if d < 0 {
		d = -d
	}
	return d <= float64(want)*pct/100
}

func TestReadInfo_Qwen3(t *testing.T) {
	dir := testModelDir(t)
	info, err := ReadInfo(filepath.Join(dir, "Qwen3-0.6B-Q4_0.gguf"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Arch != "qwen3" || info.CtxTrain != 40960 || info.Type != "model" {
		t.Fatalf("unexpected info: arch=%q ctx=%d type=%q", info.Arch, info.CtxTrain, info.Type)
	}
	if info.Params < 550e6 || info.Params > 800e6 {
		t.Fatalf("params = %d", info.Params)
	}
	if info.Quant != "Q4_0" || !info.HasTemplate || !info.IsChatModel() {
		t.Fatalf("quant=%q template=%v chat=%v", info.Quant, info.HasTemplate, info.IsChatModel())
	}
	if info.Layers != 28 || info.HeadCountKV != 8 || info.KeyLen != 128 {
		t.Fatalf("layers=%d kv=%d keylen=%d", info.Layers, info.HeadCountKV, info.KeyLen)
	}
	// 28 layers * 8 kv heads * (128+128) * 2 bytes = 114688 bytes per token.
	if got := info.KVBytesPerToken(2); got != 114688 {
		t.Fatalf("kv bytes/token = %v", got)
	}
}

func TestReadInfo_Gemma3(t *testing.T) {
	dir := testModelDir(t)
	info, err := ReadInfo(filepath.Join(dir, "gemma-3-270m-it-Q8_0.gguf"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Arch != "gemma3" || info.CtxTrain != 32768 || info.SlidingWindow == 0 {
		t.Fatalf("arch=%q ctx=%d swa=%d", info.Arch, info.CtxTrain, info.SlidingWindow)
	}
	if !within(info.Params, 268e6, 5) {
		t.Fatalf("params = %d", info.Params)
	}
}

func TestReadInfo_MMProj(t *testing.T) {
	dir := testModelDir(t)
	info, err := ReadInfo(filepath.Join(dir, "vision", "mmproj-SmolVLM-256M-Instruct-Q8_0.gguf"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Type != "mmproj" || info.IsChatModel() {
		t.Fatalf("type=%q chat=%v", info.Type, info.IsChatModel())
	}
}

func TestReadInfo_Garbage(t *testing.T) {
	dir := t.TempDir()
	rnd := make([]byte, 4096)
	rand.New(rand.NewSource(1)).Read(rnd)
	p := filepath.Join(dir, "random.gguf")
	if err := os.WriteFile(p, rnd, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadInfo(p); err == nil {
		t.Fatal("expected error for random bytes")
	}

	// Valid magic and version, then truncated.
	var b bytes.Buffer
	b.WriteString("GGUF")
	binary.Write(&b, binary.LittleEndian, uint32(3))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	p2 := filepath.Join(dir, "trunc.gguf")
	os.WriteFile(p2, b.Bytes(), 0o644)
	if _, err := ReadInfo(p2); err == nil {
		t.Fatal("expected error for truncated file")
	}

	// Huge declared string length must not allocate.
	b.Reset()
	b.WriteString("GGUF")
	binary.Write(&b, binary.LittleEndian, uint32(3))
	binary.Write(&b, binary.LittleEndian, uint64(0))
	binary.Write(&b, binary.LittleEndian, uint64(1))
	binary.Write(&b, binary.LittleEndian, uint64(1<<62))
	p3 := filepath.Join(dir, "huge.gguf")
	os.WriteFile(p3, b.Bytes(), 0o644)
	if _, err := ReadInfo(p3); err == nil {
		t.Fatal("expected error for huge string")
	}
}

func TestReadInfo_Synthetic(t *testing.T) {
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	str := func(s string) { w(uint64(len(s))); b.WriteString(s) }
	b.WriteString("GGUF")
	w(uint32(3))
	w(uint64(2)) // tensors
	w(uint64(6)) // kv
	str("general.architecture")
	w(uint32(tString))
	str("llama")
	str("general.type")
	w(uint32(tString))
	str("model")
	str("llama.block_count")
	w(uint32(tUint32))
	w(uint32(2))
	str("llama.expert_count")
	w(uint32(tUint32))
	w(uint32(8))
	str("llama.expert_used_count")
	w(uint32(tUint32))
	w(uint32(2))
	str("tokenizer.ggml.tokens")
	w(uint32(tArray))
	w(uint32(tString))
	w(uint64(3))
	str("a")
	str("b")
	str("c")
	// tensors
	str("blk.0.ffn_up_exps.weight")
	w(uint32(3))
	w(uint64(10))
	w(uint64(10))
	w(uint64(8))
	w(uint32(0))
	w(uint64(0))
	str("token_embd.weight")
	w(uint32(2))
	w(uint64(10))
	w(uint64(20))
	w(uint32(0))
	w(uint64(0))

	info, err := Read(&b)
	if err != nil {
		t.Fatal(err)
	}
	if info.Params != 1000 {
		t.Fatalf("params = %d", info.Params)
	}
	// experts 800 -> active 200, dense 200 -> 400 active.
	if info.ParamsActive != 400 {
		t.Fatalf("active = %d", info.ParamsActive)
	}
}

func TestQuantFromFileName(t *testing.T) {
	cases := map[string]string{
		"Qwen3-8B-UD-Q4_K_XL.gguf":       "Q4_K_XL",
		"gemma-3-4b-it-Q4_K_M.gguf":      "Q4_K_M",
		"model-IQ3_XXS.gguf":             "IQ3_XXS",
		"Llama-3.2-3B-Instruct-f16.gguf": "F16",
		"gpt-oss-20b-MXFP4.gguf":         "MXFP4",
		"big-Q8_0-00001-of-00003.gguf":   "Q8_0",
		"plain.gguf":                     "",
	}
	for in, want := range cases {
		if got := quantFromFileName(in); got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
	if got := nameFromFileName("gemma-3-4b-it-Q4_K_M.gguf"); got != "gemma 3 4b it" {
		t.Errorf("name = %q", got)
	}
}
