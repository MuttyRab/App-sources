// Package store keeps chats and settings as JSON files on disk. Every write
// goes to a temporary file first and is then renamed, so a crash or power
// loss never leaves a half-written chat behind.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// Chat is one conversation. Messages is owned by the UI (a message tree) and
// stored verbatim.
type Chat struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	Created   int64           `json:"created"`
	Updated   int64           `json:"updated"`
	Pinned    bool            `json:"pinned,omitempty"`
	Archived  bool            `json:"archived,omitempty"`
	Model     string          `json:"model,omitempty"`
	ModelName string          `json:"modelName,omitempty"`
	Leaf      string          `json:"leaf,omitempty"`
	Messages  json.RawMessage `json:"messages"`
}

// ChatMeta is what the sidebar needs.
type ChatMeta struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Created   int64  `json:"created"`
	Updated   int64  `json:"updated"`
	Pinned    bool   `json:"pinned,omitempty"`
	Archived  bool   `json:"archived,omitempty"`
	ModelName string `json:"modelName,omitempty"`
}

// SearchHit is a chat matching a search query with a snippet around the match.
type SearchHit struct {
	ChatMeta
	Snippet string `json:"snippet"`
}

type entry struct {
	meta ChatMeta
	text string // lower-cased plain text for search
}

// Store is safe for concurrent use.
type Store struct {
	root  string
	mu    sync.Mutex
	chats map[string]*entry
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidID reports whether id is safe to use as a file name.
func ValidID(id string) bool { return idRe.MatchString(id) }

// Open loads the chat index from root, creating directories as needed.
// Unreadable chat files are renamed to *.bad and skipped.
func Open(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "chats"), 0o755); err != nil {
		return nil, err
	}
	s := &Store{root: root, chats: map[string]*entry{}}
	files, _ := filepath.Glob(filepath.Join(root, "chats", "*.json"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var c Chat
		if err := json.Unmarshal(data, &c); err != nil || !ValidID(c.ID) || filepath.Base(f) != c.ID+".json" {
			os.Rename(f, f+".bad")
			continue
		}
		s.chats[c.ID] = &entry{meta: metaOf(&c), text: searchText(&c)}
	}
	// Leftover temp files from an interrupted write are garbage.
	tmps, _ := filepath.Glob(filepath.Join(root, "chats", "*.tmp"))
	for _, t := range tmps {
		os.Remove(t)
	}
	return s, nil
}

func metaOf(c *Chat) ChatMeta {
	return ChatMeta{ID: c.ID, Title: c.Title, Created: c.Created, Updated: c.Updated, Pinned: c.Pinned, Archived: c.Archived, ModelName: c.ModelName}
}

// searchText extracts message contents from the UI's message tree.
func searchText(c *Chat) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(c.Title))
	var msgs map[string]struct {
		Content string `json:"content"`
	}
	if json.Unmarshal(c.Messages, &msgs) == nil {
		for _, m := range msgs {
			b.WriteByte('\n')
			b.WriteString(strings.ToLower(m.Content))
		}
	}
	return b.String()
}

func (s *Store) chatPath(id string) string { return filepath.Join(s.root, "chats", id+".json") }

// List returns all chats, newest first.
func (s *Store) List() []ChatMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ChatMeta, 0, len(s.chats))
	for _, e := range s.chats {
		out = append(out, e.meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated > out[j].Updated })
	return out
}

// Get loads a chat.
func (s *Store) Get(id string) (*Chat, error) {
	if !ValidID(id) {
		return nil, errors.New("invalid chat id")
	}
	data, err := os.ReadFile(s.chatPath(id))
	if err != nil {
		return nil, err
	}
	var c Chat
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Put saves a chat.
func (s *Store) Put(c *Chat) error {
	if !ValidID(c.ID) {
		return errors.New("invalid chat id")
	}
	if len(c.Messages) == 0 {
		c.Messages = json.RawMessage("{}")
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := WriteFileAtomic(s.chatPath(c.ID), data); err != nil {
		return err
	}
	s.mu.Lock()
	s.chats[c.ID] = &entry{meta: metaOf(c), text: searchText(c)}
	s.mu.Unlock()
	return nil
}

// Delete removes a chat.
func (s *Store) Delete(id string) error {
	if !ValidID(id) {
		return errors.New("invalid chat id")
	}
	s.mu.Lock()
	delete(s.chats, id)
	s.mu.Unlock()
	err := os.Remove(s.chatPath(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// DeleteAll removes every chat (archived ones too).
func (s *Store) DeleteAll() error {
	s.mu.Lock()
	ids := make([]string, 0, len(s.chats))
	for id := range s.chats {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		if err := s.Delete(id); err != nil {
			return err
		}
	}
	return nil
}

// Search finds chats whose title or messages contain every word of q.
func (s *Store) Search(q string, limit int) []SearchHit {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var hits []SearchHit
	for _, e := range s.chats {
		ok := true
		for _, w := range words {
			if !strings.Contains(e.text, w) {
				ok = false
				break
			}
		}
		if ok {
			hits = append(hits, SearchHit{ChatMeta: e.meta, Snippet: snippet(e.text, words[0])})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Updated > hits[j].Updated })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func snippet(text, word string) string {
	i := strings.Index(text, word)
	if i < 0 {
		return ""
	}
	start := max(0, i-40)
	end := min(len(text), i+len(word)+80)
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}
	s := strings.Join(strings.Fields(text[start:end]), " ")
	if start > 0 {
		s = "…" + s
	}
	if end < len(text) {
		s += "…"
	}
	return s
}

// ReadJSON loads a JSON document stored next to the chats (e.g. settings).
// A missing file yields (nil, nil).
func (s *Store) ReadJSON(name string) (json.RawMessage, error) {
	data, err := os.ReadFile(filepath.Join(s.root, name))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !json.Valid(data) {
		os.Rename(filepath.Join(s.root, name), filepath.Join(s.root, name+".bad"))
		return nil, fmt.Errorf("%s was damaged and has been reset", name)
	}
	return data, nil
}

// WriteJSON stores a JSON document atomically.
func (s *Store) WriteJSON(name string, data []byte) error {
	if !json.Valid(data) {
		return errors.New("invalid JSON")
	}
	return WriteFileAtomic(filepath.Join(s.root, name), data)
}

// Root returns the data directory.
func (s *Store) Root() string { return s.root }

// WriteFileAtomic writes data to path via a temporary file and rename.
func WriteFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
