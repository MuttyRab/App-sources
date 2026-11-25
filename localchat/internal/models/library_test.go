package models

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localchat/internal/profile"
)

// miniGGUF builds a tiny but valid GGUF header with the given string/uint32 keys.
func miniGGUF(kv map[string]any) []byte {
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	str := func(s string) { w(uint64(len(s))); b.WriteString(s) }
	b.WriteString("GGUF")
	w(uint32(3))
	w(uint64(1))
	w(uint64(len(kv)))
	for k, v := range kv {
		str(k)
		switch x := v.(type) {
		case string:
			w(uint32(8))
			str(x)
		case uint32:
			w(uint32(4))
			w(x)
		case uint16:
			w(uint32(2))
			w(x)
		}
	}
	str("token_embd.weight")
	w(uint32(1))
	w(uint64(1000))
	w(uint32(0))
	w(uint64(0))
	return b.Bytes()
}

func write(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func model(extra map[string]any) []byte {
	kv := map[string]any{"general.architecture": "llama", "llama.block_count": uint32(2), "general.name": "Tiny"}
	for k, v := range extra {
		kv[k] = v
	}
	return miniGGUF(kv)
}

func TestInspect_SplitAndClassify(t *testing.T) {
	dir := t.TempDir()
	p1 := write(t, dir, "big-00001-of-00002.gguf", model(map[string]any{"split.count": uint16(2), "split.no": uint16(0)}))
	write(t, dir, "big-00002-of-00002.gguf", model(map[string]any{"split.count": uint16(2), "split.no": uint16(1)}))
	e, _, err := Inspect(p1)
	if err != nil {
		t.Fatal(err)
	}
	if e.Size <= int64(len(model(nil))) {
		t.Fatalf("split size not summed: %d", e.Size)
	}
	_, _, err = Inspect(filepath.Join(dir, "big-00002-of-00002.gguf"))
	var nc ErrNotChatModel
	if !errors.As(err, &nc) || !strings.Contains(nc.Reason, "part 2 of 2") {
		t.Fatalf("part 2: %v", err)
	}

	proj := write(t, dir, "mmproj-x.gguf", miniGGUF(map[string]any{"general.architecture": "clip", "general.type": "mmproj"}))
	if _, _, err := Inspect(proj); !errors.As(err, &nc) || !strings.Contains(nc.Reason, "image add-on") {
		t.Fatalf("mmproj: %v", err)
	}
	emb := write(t, dir, "emb.gguf", miniGGUF(map[string]any{"general.architecture": "bert", "bert.block_count": uint32(2), "bert.pooling_type": uint32(1)}))
	if _, _, err := Inspect(emb); !errors.As(err, &nc) || !strings.Contains(nc.Reason, "embedding") {
		t.Fatalf("embedding: %v", err)
	}
	junk := write(t, dir, "junk.gguf", []byte("hello world, not a model"))
	if _, _, err := Inspect(junk); !errors.As(err, &nc) || !strings.Contains(nc.Reason, "isn't a GGUF") {
		t.Fatalf("junk: %v", err)
	}
}

func TestPairMMProj_PrefersMatchingName(t *testing.T) {
	dir := t.TempDir()
	m := write(t, dir, "gemma-3-4b-it-Q4_K_M.gguf", model(nil))
	write(t, dir, "mmproj-other-model-f16.gguf", []byte("x"))
	want := write(t, dir, "mmproj-gemma-3-4b-it-f16.gguf", []byte("xx"))
	if got := PairMMProj(m); got != want {
		t.Fatalf("paired %q", got)
	}
}

func TestLibrary_ScanAddLearn(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "models", "tiny")
	os.MkdirAll(sub, 0o755)
	write(t, sub, "tiny-Q4_K_M.gguf", model(nil))
	write(t, sub, "mmproj-tiny.gguf", miniGGUF(map[string]any{"general.architecture": "clip"}))
	os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755)
	write(t, filepath.Join(dir, "node_modules"), "hidden.gguf", model(nil))

	ollama := filepath.Join(dir, "ollama")
	os.MkdirAll(filepath.Join(ollama, "blobs"), 0o755)
	os.MkdirAll(filepath.Join(ollama, "manifests", "registry.ollama.ai", "library", "tiny"), 0o755)
	write(t, filepath.Join(ollama, "blobs"), "sha256-abc", model(nil))
	write(t, filepath.Join(ollama, "manifests", "registry.ollama.ai", "library", "tiny"), "latest",
		[]byte(`{"layers":[{"mediaType":"application/vnd.ollama.image.model","digest":"sha256:abc"}]}`))

	lib := Open(filepath.Join(dir, "models.json"))
	found := lib.Scan([]string{dir}, ollama)
	if len(found) != 2 {
		t.Fatalf("found %d: %+v", len(found), found)
	}
	var local, oll *Entry
	for i := range found {
		if found[i].Source == "ollama" {
			oll = &found[i]
		} else {
			local = &found[i]
		}
	}
	if local == nil || local.MMProj == "" || local.Quant != "Q4_K_M" {
		t.Fatalf("local = %+v", local)
	}
	if oll == nil || oll.Name != "tiny:latest" {
		t.Fatalf("ollama = %+v", oll)
	}
	if _, _, err := lib.Add(local.Path); err != nil {
		t.Fatal(err)
	}
	lib.Learn(local.Path, func(l *profile.Learned) { l.TokensPerSec, l.BandwidthGBs = 10, 20 })
	if lib.Bandwidth() != 20 {
		t.Fatalf("bandwidth = %v", lib.Bandwidth())
	}
	lib.Add(local.Path)
	reopened := Open(filepath.Join(dir, "models.json"))
	if e := reopened.Get(local.Path); e == nil || e.Learned.TokensPerSec != 10 || e.LastUsed == 0 {
		t.Fatalf("learned values not persisted: %+v", e)
	}
	if all := reopened.All(); len(all) != 2 || all[0].Path != local.Path {
		t.Fatalf("recent order: %+v", all)
	}
}
