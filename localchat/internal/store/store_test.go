package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStore_PutGetListSearchDelete(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	c := &Chat{ID: "abc", Title: "Trip plans", Created: 1, Updated: 2,
		Messages: json.RawMessage(`{"m1":{"role":"user","content":"Plan a weekend in Lisbon"}}`)}
	if err := s.Put(c); err != nil {
		t.Fatal(err)
	}
	s.Put(&Chat{ID: "def", Title: "Recipes", Updated: 5})
	list := s.List()
	if len(list) != 2 || list[0].ID != "def" {
		t.Fatalf("list = %+v", list)
	}
	got, err := s.Get("abc")
	if err != nil || got.Title != "Trip plans" {
		t.Fatalf("get: %v %+v", err, got)
	}
	hits := s.Search("lisbon weekend", 10)
	if len(hits) != 1 || hits[0].ID != "abc" || hits[0].Snippet == "" {
		t.Fatalf("search = %+v", hits)
	}
	// Reopen: the index is rebuilt from disk.
	s2, _ := Open(root)
	if len(s2.List()) != 2 {
		t.Fatal("reopen lost chats")
	}
	if err := s.Delete("abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("abc"); err == nil {
		t.Fatal("deleted chat still readable")
	}
}

func TestStore_RejectsBadIDs(t *testing.T) {
	s, _ := Open(t.TempDir())
	for _, id := range []string{"../x", "a/b", "", "a b", "..\\x"} {
		if err := s.Put(&Chat{ID: id}); err == nil {
			t.Errorf("id %q accepted", id)
		}
		if _, err := s.Get(id); err == nil {
			t.Errorf("get %q accepted", id)
		}
	}
}

func TestStore_SkipsCorruptAndTempFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "chats")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{"id":"bad","title":`), 0o644)
	os.WriteFile(filepath.Join(dir, "good.json"), []byte(`{"id":"good","title":"ok","messages":{}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "good.json.tmp"), []byte(`partial`), 0o644)
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if l := s.List(); len(l) != 1 || l[0].ID != "good" {
		t.Fatalf("list = %+v", l)
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.json.bad")); err != nil {
		t.Fatal("corrupt chat not preserved as .bad")
	}
	if _, err := os.Stat(filepath.Join(dir, "good.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("temp file not cleaned")
	}
}

func TestStore_JSONDocs(t *testing.T) {
	s, _ := Open(t.TempDir())
	if d, err := s.ReadJSON("settings.json"); d != nil || err != nil {
		t.Fatal("missing file should be nil, nil")
	}
	if err := s.WriteJSON("settings.json", []byte(`{"theme":"dark"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteJSON("settings.json", []byte(`{bad`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	d, _ := s.ReadJSON("settings.json")
	if string(d) != `{"theme":"dark"}` {
		t.Fatalf("got %s", d)
	}
	os.WriteFile(filepath.Join(s.Root(), "settings.json"), []byte("{broken"), 0o644)
	if d, err := s.ReadJSON("settings.json"); d != nil || err == nil {
		t.Fatal("damaged settings should be reported and reset")
	}
}
