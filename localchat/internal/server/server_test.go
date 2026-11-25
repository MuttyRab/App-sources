package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"localchat/internal/engine"
	"localchat/internal/models"
	"localchat/internal/store"
)

type fakeNative struct{}

func (fakeNative) PickModelFile() (string, error)                  { return "", nil }
func (fakeNative) PickFolder() (string, error)                     { return "", nil }
func (fakeNative) SaveFile(string, string, []byte) (string, error) { return "", nil }
func (fakeNative) OpenURL(string) error                            { return nil }
func (fakeNative) Reveal(string) error                             { return nil }
func (fakeNative) SetTheme(bool, string)                           {}

func newTestServer(t *testing.T) (*Server, http.Handler) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{
		Store:   st,
		Library: models.Open(filepath.Join(dir, "models.json")),
		Engine:  engine.New("/nonexistent/llama-server", dir, 1),
		Native:  fakeNative{},
		Web:     fstest.MapFS{"index.html": {Data: []byte(`<html data-theme="light"></html>`)}},
		Token:   "secret",
	})
	s.SetAddr("127.0.0.1:5555")
	return s, s.Handler()
}

func do(h http.Handler, method, path, host, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.Host = host
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAuthAndHostChecks(t *testing.T) {
	_, h := newTestServer(t)
	if w := do(h, "GET", "/api/state", "127.0.0.1:5555", ""); w.Code != 401 {
		t.Fatalf("no cookie: %d", w.Code)
	}
	if w := do(h, "GET", "/api/state", "127.0.0.1:5555", "wrong"); w.Code != 401 {
		t.Fatalf("wrong cookie: %d", w.Code)
	}
	if w := do(h, "GET", "/api/state", "evil.example:5555", "secret"); w.Code != 403 {
		t.Fatalf("bad host: %d", w.Code)
	}
	if w := do(h, "GET", "/api/state", "127.0.0.1:5555", "secret"); w.Code != 200 {
		t.Fatalf("valid: %d", w.Code)
	}
	if w := do(h, "POST", "/v1/chat/completions", "127.0.0.1:5555", ""); w.Code != 401 {
		t.Fatalf("proxy without cookie: %d", w.Code)
	}
}

func TestIndexSetsCookieOnlyForValidToken(t *testing.T) {
	_, h := newTestServer(t)
	w := do(h, "GET", "/?t=secret", "127.0.0.1:5555", "")
	if w.Code != 302 || !strings.Contains(w.Header().Get("Set-Cookie"), "SameSite=Strict") || !strings.Contains(w.Header().Get("Set-Cookie"), "HttpOnly") {
		t.Fatalf("valid token: %d %q", w.Code, w.Header().Get("Set-Cookie"))
	}
	if w := do(h, "GET", "/?t=nope", "127.0.0.1:5555", ""); w.Code != 401 || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("invalid token: %d", w.Code)
	}
	w = do(h, "GET", "/", "127.0.0.1:5555", "secret")
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("index: %d csp=%q", w.Code, w.Header().Get("Content-Security-Policy"))
	}
}

func TestProxyWithoutModelIs503(t *testing.T) {
	_, h := newTestServer(t)
	w := do(h, "POST", "/v1/chat/completions", "127.0.0.1:5555", "secret")
	if w.Code != 503 {
		t.Fatalf("code %d", w.Code)
	}
	var body struct {
		Error struct{ Message string } `json:"error"`
	}
	json.Unmarshal(w.Body.Bytes(), &body)
	if !strings.Contains(body.Error.Message, "No model") {
		t.Fatalf("message %q", body.Error.Message)
	}
}

func TestOpenURLRejectsNonWebLinks(t *testing.T) {
	_, h := newTestServer(t)
	r := httptest.NewRequest("POST", "/api/native/open-url", strings.NewReader(`{"url":"file:///C:/Windows/System32/calc.exe"}`))
	r.Host = "127.0.0.1:5555"
	r.AddCookie(&http.Cookie{Name: cookieName, Value: "secret"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("code %d", w.Code)
	}
}
