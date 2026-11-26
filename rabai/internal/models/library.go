// Package models finds GGUF models on disk, describes them, pairs vision
// projectors with their models and remembers per-model measurements.
package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rabai/internal/gguf"
	"rabai/internal/profile"
	"rabai/internal/store"
)

// Entry describes one chat model on disk.
type Entry struct {
	Path         string          `json:"path"`
	Name         string          `json:"name"`
	FileName     string          `json:"fileName"`
	Source       string          `json:"source,omitempty"` // "ollama" for Ollama blobs
	Arch         string          `json:"arch"`
	Quant        string          `json:"quant"`
	SizeLabel    string          `json:"sizeLabel"`
	Size         int64           `json:"size"` // all split parts
	Params       uint64          `json:"params"`
	ParamsActive uint64          `json:"paramsActive"`
	CtxTrain     uint32          `json:"ctxTrain"`
	Reasoning    bool            `json:"reasoning"`
	MMProj       string          `json:"mmproj,omitempty"`
	MMProjSize   int64           `json:"mmprojSize,omitempty"`
	ModTime      int64           `json:"modTime"`
	LastUsed     int64           `json:"lastUsed,omitempty"`
	Learned      profile.Learned `json:"learned"`
	Missing      bool            `json:"missing,omitempty"`
}

// Library is the persistent list of known models.
type Library struct {
	// ScanOther, when set, receives files Scan finds that aren't chat models
	// but may be image models (non-chat GGUFs, big safetensors/ckpt files).
	ScanOther func(path string)

	path string
	mu   sync.Mutex
	data libraryFile
}

type libraryFile struct {
	Models       map[string]*Entry `json:"models"`
	BandwidthGBs float64           `json:"bandwidthGBs"`
	Folders      []string          `json:"folders"`
}

// Open loads the library file (missing or damaged files start empty).
func Open(path string) *Library {
	l := &Library{path: path, data: libraryFile{Models: map[string]*Entry{}}}
	if b, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(b, &l.data) != nil || l.data.Models == nil {
			l.data = libraryFile{Models: map[string]*Entry{}}
		}
	}
	return l
}

func (l *Library) saveLocked() {
	b, err := json.MarshalIndent(l.data, "", "  ")
	if err == nil {
		store.WriteFileAtomic(l.path, b)
	}
}

// ErrNotChatModel explains why a GGUF file can't be used for chat.
type ErrNotChatModel struct{ Reason string }

func (e ErrNotChatModel) Error() string { return e.Reason }

var splitRe = regexp.MustCompile(`^(.*)-(\d{5})-of-(\d{5})\.gguf$`)

// Inspect reads a model file and returns its entry plus raw metadata.
func Inspect(path string) (*Entry, *gguf.Info, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("can't open the file: %w", err)
	}
	if st.IsDir() {
		return nil, nil, errors.New("that's a folder, not a model file")
	}
	info, err := gguf.ReadInfo(path)
	if err != nil {
		if errors.Is(err, gguf.ErrNotGGUF) {
			return nil, nil, ErrNotChatModel{"This file isn't a GGUF model."}
		}
		return nil, nil, ErrNotChatModel{"This model file looks incomplete or damaged (" + err.Error() + "). Try downloading it again."}
	}
	switch {
	case info.Type == "mmproj":
		return nil, info, ErrNotChatModel{"This is an image add-on (mmproj), not a chat model. Choose the main model file instead; RabAI pairs the add-on automatically when both are in the same folder."}
	case info.Type == "adapter":
		return nil, info, ErrNotChatModel{"This is a LoRA adapter, not a full model."}
	case info.Pooling:
		return nil, info, ErrNotChatModel{"This is an embedding model. It turns text into numbers for search and can't chat."}
	case info.SplitCount > 1 && info.SplitIndex != 0:
		return nil, info, ErrNotChatModel{fmt.Sprintf("This is part %d of %d of a split model. Choose the first part (…-00001-of-%05d.gguf).", info.SplitIndex+1, info.SplitCount, info.SplitCount)}
	case !info.IsChatModel():
		return nil, info, ErrNotChatModel{"This GGUF file doesn't contain a chat model."}
	}

	size := st.Size()
	if info.SplitCount > 1 {
		if m := splitRe.FindStringSubmatch(filepath.Base(path)); m != nil {
			n, _ := strconv.Atoi(m[3])
			for i := 2; i <= n; i++ {
				part := filepath.Join(filepath.Dir(path), fmt.Sprintf("%s-%05d-of-%s.gguf", m[1], i, m[3]))
				ps, err := os.Stat(part)
				if err != nil {
					return nil, info, ErrNotChatModel{fmt.Sprintf("Part %d of this split model is missing from the folder.", i)}
				}
				size += ps.Size()
			}
		}
	}
	e := &Entry{
		Path: path, Name: info.Name, FileName: filepath.Base(path), Arch: info.Arch, Quant: info.Quant,
		SizeLabel: info.SizeLabel, Size: size, Params: info.Params, ParamsActive: info.ParamsActive,
		CtxTrain: info.CtxTrain, Reasoning: profile.DetectReasoning(info), ModTime: st.ModTime().Unix(),
	}
	if e.SizeLabel == "" && info.Params > 0 {
		e.SizeLabel = paramLabel(info.Params)
	}
	if mm := PairMMProj(path); mm != "" {
		e.MMProj = mm
		if ms, err := os.Stat(mm); err == nil {
			e.MMProjSize = ms.Size()
		}
	}
	return e, info, nil
}

func paramLabel(p uint64) string {
	switch {
	case p >= 1e9:
		v := float64(p) / 1e9
		if v >= 10 {
			return fmt.Sprintf("%.0fB", v)
		}
		return strings.Replace(fmt.Sprintf("%.1fB", v), ".0B", "B", 1) // 7.6B, 8B
	case p >= 1e6:
		return fmt.Sprintf("%.0fM", float64(p)/1e6)
	}
	return ""
}

// PairMMProj finds a vision projector next to the model, preferring the one
// whose file name shares the longest prefix with the model's.
func PairMMProj(modelPath string) string {
	dir := filepath.Dir(modelPath)
	matches, _ := filepath.Glob(filepath.Join(dir, "*.gguf"))
	base := normalize(filepath.Base(modelPath))
	best, bestScore, bestSize := "", -1, int64(0)
	for _, m := range matches {
		name := strings.ToLower(filepath.Base(m))
		if !strings.Contains(name, "mmproj") || m == modelPath {
			continue
		}
		score := commonPrefix(base, normalize(strings.NewReplacer("mmproj-", "", "-mmproj", "", "mmproj", "").Replace(name)))
		st, err := os.Stat(m)
		if err != nil {
			continue
		}
		if score > bestScore || (score == bestScore && st.Size() < bestSize) {
			best, bestScore, bestSize = m, score, st.Size()
		}
	}
	return best
}

func normalize(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, s)
}

func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// Add inspects path and records it as recently used.
func (l *Library) Add(path string) (*Entry, *gguf.Info, error) {
	e, info, err := Inspect(path)
	if err != nil {
		return nil, info, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if old, ok := l.data.Models[path]; ok {
		e.Learned = old.Learned
		if old.Source != "" {
			e.Source, e.Name = old.Source, old.Name
		}
	}
	e.LastUsed = time.Now().Unix()
	l.data.Models[path] = e
	l.saveLocked()
	return e, info, nil
}

// Register inspects path and adds it to the list without marking it used.
func (l *Library) Register(path string) (*Entry, *gguf.Info, error) {
	e, info, err := Inspect(path)
	if err != nil {
		return nil, info, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e = l.upsertLocked(e)
	l.saveLocked()
	c := *e
	return &c, info, nil
}

// Upsert records an entry found by a scan without changing LastUsed.
func (l *Library) upsertLocked(e *Entry) *Entry {
	if old, ok := l.data.Models[e.Path]; ok {
		e.Learned, e.LastUsed = old.Learned, old.LastUsed
		if e.Source == "" {
			e.Source = old.Source
		}
	}
	l.data.Models[e.Path] = e
	return e
}

// Get returns the entry for path.
func (l *Library) Get(path string) *Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.data.Models[path]; ok {
		c := *e
		return &c
	}
	return nil
}

// All returns every known model, most recently used first, flagging files that
// no longer exist.
func (l *Library) All() []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Entry, 0, len(l.data.Models))
	for _, e := range l.data.Models {
		c := *e
		if _, err := os.Stat(c.Path); err != nil {
			c.Missing = true
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastUsed != out[j].LastUsed {
			return out[i].LastUsed > out[j].LastUsed
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// Forget removes a model from the list (the file is untouched).
func (l *Library) Forget(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.data.Models, path)
	l.saveLocked()
}

// Learn stores measurements for a model and updates the machine bandwidth.
func (l *Library) Learn(path string, f func(*profile.Learned)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.data.Models[path]
	if !ok {
		return
	}
	f(&e.Learned)
	if e.Learned.BandwidthGBs > 0 {
		// Blend so one odd run doesn't swing every estimate.
		if l.data.BandwidthGBs == 0 {
			l.data.BandwidthGBs = e.Learned.BandwidthGBs
		} else {
			l.data.BandwidthGBs = 0.7*l.data.BandwidthGBs + 0.3*e.Learned.BandwidthGBs
		}
	}
	l.saveLocked()
}

// Bandwidth returns the calibrated memory bandwidth in GB/s (0 = unknown).
func (l *Library) Bandwidth() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.data.BandwidthGBs
}

// Folders returns the extra folders the user asked RabAI to scan.
func (l *Library) Folders() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.data.Folders...)
}

// SetFolders replaces the extra scan folders.
func (l *Library) SetFolders(dirs []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.data.Folders = dirs
	l.saveLocked()
}

var skipDirs = map[string]bool{
	"node_modules": true, "$recycle.bin": true, "windows": true, "program files": true,
	"program files (x86)": true, "appdata": true, "system volume information": true, "__pycache__": true,
	".git": true, "site-packages": true,
}

// Scan walks dirs (to a limited depth) for chat models and Ollama's model
// store. It returns everything found and remembers it in the library.
func (l *Library) Scan(dirs []string, ollamaDir string) []Entry {
	deadline := time.Now().Add(8 * time.Second)
	seen := map[string]bool{}
	var found []*Entry
	for _, root := range dirs {
		rootDepth := strings.Count(filepath.Clean(root), string(filepath.Separator))
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || time.Now().After(deadline) {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				name := strings.ToLower(d.Name())
				depth := strings.Count(filepath.Clean(p), string(filepath.Separator)) - rootDepth
				if p != root && (strings.HasPrefix(name, ".") && name != ".lmstudio" && name != ".cache" || skipDirs[name] || depth > 5) {
					return fs.SkipDir
				}
				return nil
			}
			name := strings.ToLower(d.Name())
			if l.ScanOther != nil && !seen[p] && (strings.HasSuffix(name, ".safetensors") || strings.HasSuffix(name, ".sft") || strings.HasSuffix(name, ".ckpt")) {
				// Image models: big weight files that aren't GGUF chat models.
				if info, err := d.Info(); err == nil && info.Size() >= 900<<20 {
					seen[p] = true
					l.ScanOther(p)
				}
				return nil
			}
			if !strings.HasSuffix(name, ".gguf") || strings.Contains(name, "mmproj") || seen[p] {
				return nil
			}
			if m := splitRe.FindStringSubmatch(d.Name()); m != nil && m[2] != "00001" {
				return nil
			}
			seen[p] = true
			if e, _, err := Inspect(p); err == nil {
				found = append(found, e)
			} else if l.ScanOther != nil {
				var nc ErrNotChatModel
				if errors.As(err, &nc) && strings.Contains(nc.Reason, "doesn't contain a chat model") {
					l.ScanOther(p)
				}
			}
			return nil
		})
	}
	for _, e := range scanOllama(ollamaDir) {
		if !seen[e.Path] {
			seen[e.Path] = true
			found = append(found, e)
		}
	}
	l.mu.Lock()
	out := make([]Entry, 0, len(found))
	for _, e := range found {
		out = append(out, *l.upsertLocked(e))
	}
	l.saveLocked()
	l.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// scanOllama reads Ollama's manifests and returns its GGUF model blobs.
func scanOllama(root string) []*Entry {
	if root == "" {
		return nil
	}
	manifests := filepath.Join(root, "manifests")
	var out []*Entry
	filepath.WalkDir(manifests, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil || len(b) > 1<<20 {
			return nil
		}
		var mf struct {
			Layers []struct {
				MediaType string `json:"mediaType"`
				Digest    string `json:"digest"`
			} `json:"layers"`
		}
		if json.Unmarshal(b, &mf) != nil {
			return nil
		}
		var model, proj string
		for _, ly := range mf.Layers {
			blob := filepath.Join(root, "blobs", strings.Replace(ly.Digest, ":", "-", 1))
			switch ly.MediaType {
			case "application/vnd.ollama.image.model":
				model = blob
			case "application/vnd.ollama.image.projector":
				proj = blob
			}
		}
		if model == "" {
			return nil
		}
		e, _, err := Inspect(model)
		if err != nil {
			return nil
		}
		// manifests/<registry>/<namespace>/<model>/<tag>
		rel, _ := filepath.Rel(manifests, p)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) >= 2 {
			e.Name = parts[len(parts)-2] + ":" + parts[len(parts)-1]
		}
		e.Source = "ollama"
		e.MMProj, e.MMProjSize = "", 0
		if proj != "" {
			if ps, err := os.Stat(proj); err == nil {
				e.MMProj, e.MMProjSize = proj, ps.Size()
			}
		}
		out = append(out, e)
		return nil
	})
	return out
}
