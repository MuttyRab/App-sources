// Package imagemodel recognises image generation model files (Stable
// Diffusion, SDXL, FLUX, Z-Image, Qwen Image and the other families
// stable-diffusion.cpp supports), works out which extra files each needs
// (text encoders, VAE…) and finds them on disk.
package imagemodel

import (
	"archive/zip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"rabai/internal/gguf"
)

// File is what RabAI reads from a model file without loading its weights.
type File struct {
	Path    string
	Format  string // "gguf", "safetensors", "ckpt"
	Size    int64
	Tensors map[string][]int64 // name → shape, innermost dimension first (ggml order)
	Arch    string             // GGUF general.architecture, if any
	GGUF    *gguf.Info         // GGUF metadata, if a GGUF file
}

// ErrUnknownFormat is returned for files that aren't model weights.
var ErrUnknownFormat = errors.New("not a model weights file")

// Extensions are the weight file types stable-diffusion.cpp loads.
var Extensions = []string{".gguf", ".safetensors", ".sft", ".ckpt", ".pt", ".pth"}

// HasModelExt reports whether path has a weights file extension.
func HasModelExt(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, e := range Extensions {
		if ext == e {
			return true
		}
	}
	return false
}

// ReadFile reads the tensor list of a GGUF, safetensors or PyTorch checkpoint.
func ReadFile(path string) (*File, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, errors.New("that's a folder, not a model file")
	}
	f := &File{Path: path, Size: st.Size(), Tensors: map[string][]int64{}}
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	var magic [8]byte
	if _, err := io.ReadFull(fh, magic[:]); err != nil {
		return nil, ErrUnknownFormat
	}
	switch {
	case string(magic[:4]) == "GGUF":
		info, err := gguf.ReadInfo(path)
		if err != nil {
			return nil, err
		}
		f.Format, f.GGUF, f.Arch = "gguf", info, info.Arch
		for _, t := range info.Tensors {
			dims := make([]int64, len(t.Dims))
			for i, d := range t.Dims {
				dims[i] = int64(d)
			}
			f.Tensors[t.Name] = dims
		}
		return f, nil
	case string(magic[:2]) == "PK":
		f.Format = "ckpt"
		if err := readCkptNames(path, f); err != nil {
			return nil, err
		}
		return f, nil
	}
	// safetensors: little-endian u64 header length, then a JSON header.
	n := binary.LittleEndian.Uint64(magic[:])
	if n < 2 || n > 200<<20 || int64(n)+8 > f.Size {
		return nil, ErrUnknownFormat
	}
	hdr := make([]byte, n)
	if _, err := io.ReadFull(fh, hdr); err != nil {
		return nil, fmt.Errorf("the file is incomplete: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(hdr, &raw); err != nil {
		return nil, ErrUnknownFormat
	}
	f.Format = "safetensors"
	for name, v := range raw {
		if name == "__metadata__" {
			continue
		}
		var t struct {
			Shape []int64 `json:"shape"`
		}
		if json.Unmarshal(v, &t) != nil {
			continue
		}
		// safetensors stores shapes outermost first; use ggml's order.
		dims := make([]int64, len(t.Shape))
		for i := range t.Shape {
			dims[i] = t.Shape[len(t.Shape)-1-i]
		}
		f.Tensors[name] = dims
	}
	if len(f.Tensors) == 0 {
		return nil, ErrUnknownFormat
	}
	return f, nil
}

// readCkptNames lists tensor names in a PyTorch zip checkpoint. Shapes live
// in the pickle, which isn't parsed; names are enough to tell model kinds
// apart and the engine checks the rest.
func readCkptNames(path string, f *File) error {
	z, err := zip.OpenReader(path)
	if err != nil {
		return ErrUnknownFormat
	}
	defer z.Close()
	for _, zf := range z.File {
		if !strings.HasSuffix(zf.Name, "data.pkl") {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(rc, 64<<20))
		rc.Close()
		if err != nil {
			return err
		}
		for _, name := range pickleStrings(data) {
			if strings.Count(name, ".") >= 1 && (strings.HasSuffix(name, ".weight") || strings.HasSuffix(name, ".bias")) {
				f.Tensors[name] = nil
			}
		}
		return nil
	}
	return ErrUnknownFormat
}

// pickleStrings extracts the unicode strings stored in a pickle stream.
func pickleStrings(b []byte) []string {
	var out []string
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case 'X': // BINUNICODE: u32 length
			if i+5 <= len(b) {
				n := int(binary.LittleEndian.Uint32(b[i+1:]))
				if n > 0 && n < 512 && i+5+n <= len(b) {
					out = append(out, string(b[i+5:i+5+n]))
					i += 4 + n
				}
			}
		case 0x8c: // SHORT_BINUNICODE: u8 length
			if i+2 <= len(b) {
				n := int(b[i+1])
				if i+2+n <= len(b) {
					out = append(out, string(b[i+2:i+2+n]))
					i += 1 + n
				}
			}
		}
	}
	return out
}

// has reports whether any tensor name contains sub.
func (f *File) has(sub string) bool {
	for n := range f.Tensors {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

// hasPrefix reports whether any tensor name starts with p.
func (f *File) hasPrefix(p string) bool {
	for n := range f.Tensors {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// dim returns dimension i (ggml order) of the first tensor whose name ends
// with suffix, or 0.
func (f *File) dim(suffix string, i int) int64 {
	for n, d := range f.Tensors {
		if strings.HasSuffix(n, suffix) && i < len(d) {
			return d[i]
		}
	}
	return 0
}
