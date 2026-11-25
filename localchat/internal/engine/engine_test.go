package engine

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary impersonate llama-server: when FAKE_ENGINE is
// set it prints the router-child protocol lines and serves /health.
func TestMain(m *testing.M) {
	if mode := os.Getenv("FAKE_ENGINE"); mode != "" {
		fakeEngine(mode)
		return
	}
	os.Exit(m.Run())
}

func fakeEngine(mode string) {
	port := ""
	for i, a := range os.Args {
		if a == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	if os.Getenv(envRouterPort) == "" {
		fmt.Fprintln(os.Stderr, "missing router port env")
		os.Exit(3)
	}
	if mode == "fail" {
		fmt.Fprintln(os.Stderr, "llama_model_load: error loading model: unknown model architecture: 'futurenet'")
		os.Exit(1)
	}
	if mode == "oom" && !strings.Contains(strings.Join(os.Args, " "), "f16") {
		fmt.Fprintln(os.Stderr, "ggml_backend_cpu_buffer_type_alloc_buffer: failed to allocate buffer")
		os.Exit(1)
	}
	for _, v := range []float64{0, 0.5, 1} {
		fmt.Printf("%s{\"state\":\"loading\",\"payload\":{\"stages\":[\"text_model\"],\"current\":\"text_model\",\"value\":%v}}\n", stateLine, v)
		time.Sleep(30 * time.Millisecond)
	}
	ready := false
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if !ready {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(`{"status":"ok"}`))
	})
	go http.ListenAndServe("127.0.0.1:"+port, nil)
	time.Sleep(50 * time.Millisecond)
	ready = true
	fmt.Printf("%s{\"state\":\"ready\",\"payload\":{\"memory\":{\"n_ctx\":4096,\"devices\":[{\"kind\":\"host\",\"name\":\"Host\",\"model\":1000,\"context\":4096000,\"compute\":10}]}}}\n", stateLine)
	// Exit when stdin closes or the exit command arrives, like llama-server.
	buf := make([]byte, 256)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || strings.Contains(string(buf[:n]), exitCommand) {
			os.Exit(0)
		}
	}
}

func waitFor(t *testing.T, m *Manager, status string) State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := m.State(); s.Status == status {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s, state=%+v", status, m.State())
	return State{}
}

func newFake(t *testing.T, mode string) *Manager {
	t.Setenv("FAKE_ENGINE", mode)
	exe, _ := os.Executable()
	return New(exe, t.TempDir(), 12345)
}

func TestManager_LoadReadyUnload(t *testing.T) {
	m := newFake(t, "ok")
	ch, cancel := m.Subscribe()
	defer cancel()
	m.Load(LoadRequest{ModelPath: "x.gguf", Args: []string{"-c", "4096"}})
	st := waitFor(t, m, StatusReady)
	if st.Memory == nil || st.Memory.NCtx != 4096 || st.Memory.ContextBytes != 4096000 {
		t.Fatalf("memory report = %+v", st.Memory)
	}
	if m.BaseURL() == "" {
		t.Fatal("no base url")
	}
	sawProgress := false
	for len(ch) > 0 {
		if s := <-ch; s.Status == StatusLoading && s.Progress > 0 && s.Progress < 1 {
			sawProgress = true
		}
	}
	if !sawProgress {
		t.Fatal("no intermediate progress was published")
	}
	resp, err := http.Get(m.BaseURL() + "/health")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("health: %v %v", err, resp)
	}
	resp.Body.Close()
	m.Unload()
	if m.State().Status != StatusIdle || m.BaseURL() != "" {
		t.Fatalf("after unload: %+v", m.State())
	}
}

func TestManager_FriendlyError(t *testing.T) {
	m := newFake(t, "fail")
	m.Load(LoadRequest{ModelPath: "x.gguf"})
	st := waitFor(t, m, StatusError)
	if !strings.Contains(st.Error, "futurenet") || !strings.Contains(st.ErrorDetail, "unknown model architecture") {
		t.Fatalf("error = %q detail=%q", st.Error, st.ErrorDetail)
	}
}

func TestManager_FallbackArgs(t *testing.T) {
	m := newFake(t, "oom")
	m.Load(LoadRequest{ModelPath: "x.gguf", Args: []string{"-ctk", "q8_0"}, Fallbacks: [][]string{{"-ctk", "f16"}}})
	st := waitFor(t, m, StatusReady)
	if st.Attempt != 1 {
		t.Fatalf("attempt = %d", st.Attempt)
	}
	m.Unload()
}

func TestExplain(t *testing.T) {
	msg, _ := Explain([]string{"llama_model_load: error loading model: tensor 'x' data is not within the file bounds"})
	if !strings.Contains(msg, "incomplete or damaged") {
		t.Fatal(msg)
	}
	msg, _ = Explain([]string{"something odd"})
	if msg != genericError {
		t.Fatal(msg)
	}
}

func TestExtract(t *testing.T) {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, _ := zw.Create("llama-server.exe")
	w.Write([]byte("bin"))
	zw.Close()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "oldengine"), 0o755)
	dir, err := Extract(b.Bytes(), root)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "llama-server.exe")); string(data) != "bin" {
		t.Fatal("not extracted")
	}
	if _, err := os.Stat(filepath.Join(root, "oldengine")); !os.IsNotExist(err) {
		t.Fatal("old engine not cleaned up")
	}
	dir2, err := Extract(b.Bytes(), root)
	if err != nil || dir2 != dir {
		t.Fatal("second extract should reuse", err)
	}

	var evil bytes.Buffer
	zw = zip.NewWriter(&evil)
	w, _ = zw.Create("../evil.txt")
	w.Write([]byte("x"))
	zw.Close()
	if _, err := Extract(evil.Bytes(), t.TempDir()); err == nil {
		t.Fatal("path traversal not rejected")
	}
}
