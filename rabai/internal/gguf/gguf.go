// Package gguf reads the metadata header of GGUF model files without loading
// tensor data. It is defensive against truncated and corrupt files: every
// length read from the file is bounded before it is used.
package gguf

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Value types defined by the GGUF specification.
const (
	tUint8   = 0
	tInt8    = 1
	tUint16  = 2
	tInt16   = 3
	tUint32  = 4
	tInt32   = 5
	tFloat32 = 6
	tBool    = 7
	tString  = 8
	tArray   = 9
	tUint64  = 10
	tInt64   = 11
	tFloat64 = 12
)

const (
	maxStringLen   = 64 << 20 // 64 MiB: chat templates are KBs, nothing legit is near this
	maxArrayLen    = 64 << 20
	maxTensorCount = 1 << 20
	maxKVCount     = 1 << 16
	maxDims        = 8
)

// ErrNotGGUF is returned when the file does not start with the GGUF magic.
var ErrNotGGUF = errors.New("not a GGUF file")

// Info is the subset of GGUF metadata RabAI uses to describe and tune a model.
type Info struct {
	Path     string `json:"path"`
	FileName string `json:"fileName"`
	FileSize int64  `json:"fileSize"` // size of this file (for splits: sum of all parts, set by caller)
	Version  uint32 `json:"version"`

	Arch      string `json:"arch"`
	Name      string `json:"name"`
	SizeLabel string `json:"sizeLabel"`
	Type      string `json:"type"` // "model", "mmproj", "adapter", ...
	FileType  int    `json:"fileType"`
	Quant     string `json:"quant"`

	Params       uint64 `json:"params"`
	ParamsActive uint64 `json:"paramsActive"`

	Layers        uint32   `json:"layers"`
	CtxTrain      uint32   `json:"ctxTrain"`
	EmbdLen       uint32   `json:"embdLen"`
	HeadCount     uint32   `json:"headCount"`
	HeadCountKV   uint32   `json:"headCountKv"`
	HeadCountKVs  []uint32 `json:"-"` // per-layer KV head counts when the model varies them
	KeyLen        uint32   `json:"keyLen"`
	ValueLen      uint32   `json:"valueLen"`
	SlidingWindow uint32   `json:"slidingWindow"`
	ExpertCount   uint32   `json:"expertCount"`
	ExpertUsed    uint32   `json:"expertUsed"`
	KVLoraRank    uint32   `json:"kvLoraRank"`
	RopeDim       uint32   `json:"ropeDim"`
	VocabSize     uint32   `json:"vocabSize"`

	ChatTemplate string             `json:"-"`
	HasTemplate  bool               `json:"hasTemplate"`
	Sampling     map[string]float64 `json:"sampling,omitempty"`
	Pooling      bool               `json:"pooling"`

	SplitCount uint32 `json:"splitCount"`
	SplitIndex uint32 `json:"splitIndex"`

	// Tensors lists every tensor's name and shape (used to recognise image
	// generation models, which have no architecture metadata).
	Tensors []Tensor `json:"-"`
}

// Tensor is a tensor's name and shape, innermost dimension first (GGUF order).
type Tensor struct {
	Name string
	Dims []uint64
}

type reader struct {
	r   *bufio.Reader
	err error
}

func (r *reader) fail(err error) {
	if r.err == nil {
		r.err = err
	}
}

func (r *reader) read(n int) []byte {
	if r.err != nil {
		return nil
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r.r, buf); err != nil {
		r.fail(fmt.Errorf("file ends early: %w", err))
		return nil
	}
	return buf
}

func (r *reader) skip(n uint64) {
	if r.err != nil {
		return
	}
	for n > 0 {
		chunk := n
		if chunk > 1<<30 {
			chunk = 1 << 30
		}
		d, err := r.r.Discard(int(chunk))
		n -= uint64(d)
		if err != nil {
			r.fail(fmt.Errorf("file ends early: %w", err))
			return
		}
	}
}

func (r *reader) u8() uint8 {
	b := r.read(1)
	if b == nil {
		return 0
	}
	return b[0]
}
func (r *reader) u16() uint16 {
	b := r.read(2)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(b)
}
func (r *reader) u32() uint32 {
	b := r.read(4)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}
func (r *reader) u64() uint64 {
	b := r.read(8)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}

func (r *reader) str() string {
	n := r.u64()
	if r.err != nil {
		return ""
	}
	if n > maxStringLen {
		r.fail(fmt.Errorf("corrupt file: string length %d", n))
		return ""
	}
	return string(r.read(int(n)))
}

func (r *reader) skipStr() {
	n := r.u64()
	if n > maxStringLen {
		r.fail(fmt.Errorf("corrupt file: string length %d", n))
		return
	}
	r.skip(n)
}

func scalarSize(t uint32) int {
	switch t {
	case tUint8, tInt8, tBool:
		return 1
	case tUint16, tInt16:
		return 2
	case tUint32, tInt32, tFloat32:
		return 4
	case tUint64, tInt64, tFloat64:
		return 8
	}
	return 0
}

// scalar reads one scalar of type t and returns it as float64 (for numbers),
// bool, or string. Unknown types set an error.
func (r *reader) scalar(t uint32) any {
	switch t {
	case tUint8:
		return float64(r.u8())
	case tInt8:
		return float64(int8(r.u8()))
	case tUint16:
		return float64(r.u16())
	case tInt16:
		return float64(int16(r.u16()))
	case tUint32:
		return float64(r.u32())
	case tInt32:
		return float64(int32(r.u32()))
	case tFloat32:
		return float64(math.Float32frombits(r.u32()))
	case tBool:
		return r.u8() != 0
	case tString:
		return r.str()
	case tUint64:
		return float64(r.u64())
	case tInt64:
		return float64(int64(r.u64()))
	case tFloat64:
		return math.Float64frombits(r.u64())
	}
	r.fail(fmt.Errorf("corrupt file: unknown value type %d", t))
	return nil
}

// value reads a value of type t. Arrays of numbers are returned as []float64
// only when keep is true and they are short; other arrays are skipped.
func (r *reader) value(t uint32, keep bool) any {
	if t != tArray {
		if t == tString && !keep {
			r.skipStr()
			return nil
		}
		return r.scalar(t)
	}
	et := r.u32()
	n := r.u64()
	if r.err != nil {
		return nil
	}
	if n > maxArrayLen {
		r.fail(fmt.Errorf("corrupt file: array length %d", n))
		return nil
	}
	if keep && n <= 4096 && et != tString && et != tArray {
		out := make([]float64, 0, n)
		for i := uint64(0); i < n && r.err == nil; i++ {
			if f, ok := r.scalar(et).(float64); ok {
				out = append(out, f)
			}
		}
		return out
	}
	switch et {
	case tString:
		for i := uint64(0); i < n && r.err == nil; i++ {
			r.skipStr()
		}
	case tArray:
		for i := uint64(0); i < n && r.err == nil; i++ {
			r.value(tArray, false)
		}
	default:
		sz := scalarSize(et)
		if sz == 0 {
			r.fail(fmt.Errorf("corrupt file: unknown array type %d", et))
			return nil
		}
		r.skip(n * uint64(sz))
	}
	return nil
}

func num(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}

// ReadInfo parses the GGUF header, metadata and tensor descriptors of path.
func ReadInfo(path string) (*Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	info, err := Read(f)
	if err != nil {
		return nil, err
	}
	info.Path = path
	info.FileName = filepath.Base(path)
	info.FileSize = st.Size()
	if info.Quant == "" {
		info.Quant = quantFromFileName(info.FileName)
	}
	if info.Name == "" {
		info.Name = nameFromFileName(info.FileName)
	}
	return info, nil
}

// Read parses GGUF metadata from an io.Reader positioned at the start of the file.
func Read(src io.Reader) (*Info, error) {
	r := &reader{r: bufio.NewReaderSize(src, 1<<20)}
	magic := r.read(4)
	if r.err != nil {
		return nil, ErrNotGGUF
	}
	if string(magic) != "GGUF" {
		return nil, ErrNotGGUF
	}
	info := &Info{FileType: -1}
	info.Version = r.u32()
	if r.err == nil && (info.Version < 2 || info.Version > 3) {
		return nil, fmt.Errorf("unsupported GGUF version %d", info.Version)
	}
	nTensors := r.u64()
	nKV := r.u64()
	if r.err != nil {
		return nil, r.err
	}
	if nTensors > maxTensorCount || nKV > maxKVCount {
		return nil, fmt.Errorf("corrupt file: %d tensors, %d keys", nTensors, nKV)
	}

	kv := make(map[string]any, nKV)
	for i := uint64(0); i < nKV && r.err == nil; i++ {
		key := r.str()
		t := r.u32()
		kv[key] = r.value(t, wanted(key))
	}
	if r.err != nil {
		return nil, r.err
	}
	info.apply(kv)

	var total, experts uint64
	for i := uint64(0); i < nTensors && r.err == nil; i++ {
		name := r.str()
		nd := r.u32()
		if nd > maxDims {
			return nil, fmt.Errorf("corrupt file: tensor %q has %d dims", name, nd)
		}
		count := uint64(1)
		dims := make([]uint64, nd)
		for d := uint32(0); d < nd; d++ {
			dims[d] = r.u64()
			count *= dims[d]
		}
		info.Tensors = append(info.Tensors, Tensor{Name: name, Dims: dims})
		r.u32() // ggml type
		r.u64() // data offset
		total += count
		if strings.Contains(name, "_exps") {
			experts += count
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	info.Params = total
	info.ParamsActive = total
	if experts > 0 && info.ExpertCount > 0 && info.ExpertUsed > 0 && info.ExpertUsed < info.ExpertCount {
		active := experts * uint64(info.ExpertUsed) / uint64(info.ExpertCount)
		info.ParamsActive = total - experts + active
	}
	return info, nil
}

// wanted reports whether the value of key is needed (otherwise it is skipped
// cheaply). Tokenizer vocabularies are the bulk of the header and are skipped.
func wanted(key string) bool {
	if strings.HasPrefix(key, "tokenizer.ggml.") {
		return false
	}
	if strings.HasPrefix(key, "tokenizer.chat_template.") {
		return false // named alternative templates (tool_use, rag)
	}
	return true
}

func (info *Info) apply(kv map[string]any) {
	str := func(k string) string {
		s, _ := kv[k].(string)
		return s
	}
	u32 := func(k string) uint32 {
		if f, ok := num(kv[k]); ok && f >= 0 && f < math.MaxUint32 {
			return uint32(f)
		}
		if arr, ok := kv[k].([]float64); ok && len(arr) > 0 {
			max := 0.0
			for _, v := range arr {
				if v > max {
					max = v
				}
			}
			return uint32(max)
		}
		return 0
	}

	info.Arch = str("general.architecture")
	a := info.Arch
	info.Name = strings.TrimSpace(str("general.name"))
	if base := strings.TrimSpace(str("general.basename")); info.Name == "" && base != "" {
		info.Name = base
		if ft := strings.TrimSpace(str("general.finetune")); ft != "" {
			info.Name += " " + ft
		}
	}
	info.SizeLabel = str("general.size_label")
	info.Type = normalizeType(str("general.type"), a, str("adapter.type"))
	if f, ok := num(kv["general.file_type"]); ok {
		info.FileType = int(f)
		info.Quant = QuantName(int(f))
	}

	info.Layers = u32(a + ".block_count")
	info.CtxTrain = u32(a + ".context_length")
	info.EmbdLen = u32(a + ".embedding_length")
	info.HeadCount = u32(a + ".attention.head_count")
	info.HeadCountKV = u32(a + ".attention.head_count_kv")
	if arr, ok := kv[a+".attention.head_count_kv"].([]float64); ok {
		info.HeadCountKVs = make([]uint32, len(arr))
		for i, v := range arr {
			info.HeadCountKVs[i] = uint32(v)
		}
	}
	if info.HeadCountKV == 0 && len(info.HeadCountKVs) == 0 {
		info.HeadCountKV = info.HeadCount
	}
	info.KeyLen = u32(a + ".attention.key_length")
	info.ValueLen = u32(a + ".attention.value_length")
	if info.HeadCount > 0 && info.EmbdLen > 0 {
		def := info.EmbdLen / info.HeadCount
		if info.KeyLen == 0 {
			info.KeyLen = def
		}
		if info.ValueLen == 0 {
			info.ValueLen = def
		}
	}
	info.SlidingWindow = u32(a + ".attention.sliding_window")
	info.ExpertCount = u32(a + ".expert_count")
	info.ExpertUsed = u32(a + ".expert_used_count")
	info.KVLoraRank = u32(a + ".attention.kv_lora_rank")
	info.RopeDim = u32(a + ".rope.dimension_count")
	info.VocabSize = u32(a + ".vocab_size")
	_, info.Pooling = kv[a+".pooling_type"]

	info.ChatTemplate = str("tokenizer.chat_template")
	info.HasTemplate = info.ChatTemplate != ""

	for k, v := range kv {
		if strings.HasPrefix(k, "general.sampling.") {
			if f, ok := num(v); ok {
				if info.Sampling == nil {
					info.Sampling = map[string]float64{}
				}
				info.Sampling[strings.TrimPrefix(k, "general.sampling.")] = f
			}
		}
	}

	info.SplitCount = u32("split.count")
	info.SplitIndex = u32("split.no")
}

// KVBytesPerToken estimates the KV-cache bytes one token of context needs at
// the given element size (2 for f16, ~1.0625 for q8_0). Sliding-window layers
// are counted as full layers, so the estimate errs on the high side.
func (info *Info) KVBytesPerToken(bytesPerElem float64) float64 {
	if info.KVLoraRank > 0 {
		rope := info.RopeDim
		if rope == 0 {
			rope = 64
		}
		return float64(info.Layers) * float64(info.KVLoraRank+rope) * bytesPerElem
	}
	per := float64(info.KeyLen + info.ValueLen)
	if len(info.HeadCountKVs) > 0 {
		sum := 0.0
		for _, h := range info.HeadCountKVs {
			sum += float64(h)
		}
		return sum * per * bytesPerElem
	}
	return float64(info.Layers) * float64(info.HeadCountKV) * per * bytesPerElem
}

// IsChatModel reports whether the file is a full language model usable for chat.
func (info *Info) IsChatModel() bool {
	return info.Type == "model" && !info.Pooling && info.Layers > 0
}

var quantNames = map[int]string{
	0: "F32", 1: "F16", 2: "Q4_0", 3: "Q4_1", 7: "Q8_0", 8: "Q5_0", 9: "Q5_1",
	10: "Q2_K", 11: "Q3_K_S", 12: "Q3_K_M", 13: "Q3_K_L", 14: "Q4_K_S", 15: "Q4_K_M",
	16: "Q5_K_S", 17: "Q5_K_M", 18: "Q6_K", 19: "IQ2_XXS", 20: "IQ2_XS", 21: "Q2_K_S",
	22: "IQ3_XS", 23: "IQ3_XXS", 24: "IQ1_S", 25: "IQ4_NL", 26: "IQ3_S", 27: "IQ3_M",
	28: "IQ2_S", 29: "IQ2_M", 30: "IQ4_XS", 31: "IQ1_M", 32: "BF16", 36: "TQ1_0",
	37: "TQ2_0", 38: "MXFP4", 39: "NVFP4", 40: "Q1_0", 41: "Q2_0", 42: "F8_E4M3",
}

// QuantName maps a general.file_type value to its conventional name.
func QuantName(fileType int) string {
	if n, ok := quantNames[fileType]; ok {
		return n
	}
	return ""
}

var quantRe = regexp.MustCompile(`(?i)(?:UD-)?(IQ[1-4]_[A-Z]+|Q[1-8]_K(?:_[SMLX]+)?|Q[1-8]_[01]|MXFP4|BF16|F16|F32|Q8_0)`)

func quantFromFileName(name string) string {
	m := quantRe.FindAllStringSubmatch(name, -1)
	if len(m) == 0 {
		return ""
	}
	return strings.ToUpper(m[len(m)-1][1])
}

// normalizeType maps the many spellings of general.type to model, mmproj or adapter.
func normalizeType(t, arch, adapterType string) string {
	t = strings.ToLower(t)
	switch {
	case t == "mmproj" || strings.HasPrefix(t, "clip") || arch == "clip" || strings.HasPrefix(arch, "clip"):
		return "mmproj"
	case strings.HasPrefix(t, "adapter") || adapterType != "":
		return "adapter"
	case t == "" || t == "model":
		return "model"
	}
	return t
}

var splitRe = regexp.MustCompile(`-\d{5}-of-\d{5}$`)

func nameFromFileName(name string) string {
	n := strings.TrimSuffix(name, filepath.Ext(name))
	n = splitRe.ReplaceAllString(n, "")
	if q := quantFromFileName(n); q != "" {
		if i := strings.LastIndex(strings.ToUpper(n), q); i > 0 {
			n = strings.TrimRight(n[:i], "-_. ")
		}
	}
	return strings.NewReplacer("_", " ", "-", " ").Replace(n)
}
