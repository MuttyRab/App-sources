package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Media files (generated pictures, attached audio and video) live next to the
// chats in a "media" folder and are referenced from messages by id, which
// keeps chat files small.

var mediaIDRe = regexp.MustCompile(`^[a-f0-9]{24}\.(png|jpg|jpeg|webp|gif|wav|mp3|m4a|ogg|flac|webm|mp4|mov)$`)

// MediaTypes maps file extensions to MIME types.
var MediaTypes = map[string]string{
	"png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg", "webp": "image/webp", "gif": "image/gif",
	"wav": "audio/wav", "mp3": "audio/mpeg", "m4a": "audio/mp4", "ogg": "audio/ogg", "flac": "audio/flac",
	"webm": "video/webm", "mp4": "video/mp4", "mov": "video/quicktime",
}

// ValidMediaID reports whether id names a media file.
func ValidMediaID(id string) bool { return mediaIDRe.MatchString(id) }

func (s *Store) mediaDir() string { return filepath.Join(s.root, "media") }

// SaveMedia stores data with the given extension and returns its id.
func (s *Store) SaveMedia(data []byte, ext string) (string, error) {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	if _, ok := MediaTypes[ext]; !ok {
		return "", errors.New("unsupported media type")
	}
	if err := os.MkdirAll(s.mediaDir(), 0o755); err != nil {
		return "", err
	}
	var b [12]byte
	rand.Read(b[:])
	id := hex.EncodeToString(b[:]) + "." + ext
	if err := WriteFileAtomic(filepath.Join(s.mediaDir(), id), data); err != nil {
		return "", err
	}
	return id, nil
}

// MediaPath returns the file for a media id ("" if the id is invalid).
func (s *Store) MediaPath(id string) string {
	if !ValidMediaID(id) {
		return ""
	}
	return filepath.Join(s.mediaDir(), id)
}

// DeleteMedia removes a media file.
func (s *Store) DeleteMedia(id string) {
	if p := s.MediaPath(id); p != "" {
		os.Remove(p)
	}
}
