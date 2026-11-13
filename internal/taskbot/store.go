package taskbot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type Draft struct {
	EditTarget  *Issue    `json:"edit_target,omitempty"`
	Token       string    `json:"token"`
	Source      string    `json:"source"`
	Fingerprint string    `json:"fingerprint"`
	State       string    `json:"state"`
	URL         string    `json:"url,omitempty"`
	UserID      int64     `json:"user_id"`
	ChatID      int64     `json:"chat_id"`
	Proposal    Proposal  `json:"proposal"`
	Private     bool      `json:"private"`
	Created     time.Time `json:"created"`
}
type snapshot struct {
	Version int              `json:"version"`
	Offset  int64            `json:"offset"`
	Drafts  map[string]Draft `json:"drafts"`
}
type Store struct {
	mu   sync.Mutex
	path string
	data snapshot
}

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	s := &Store{path: path, data: snapshot{Version: 1, Drafts: map[string]Draft{}}}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err = json.Unmarshal(raw, &s.data); err != nil {
			return nil, fmt.Errorf("invalid state file: %w", err)
		}
		if s.data.Version != 1 || s.data.Drafts == nil {
			return nil, fmt.Errorf("unsupported state format")
		}
		if err = os.Chmod(path, 0600); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Every mutation writes a complete 0600 snapshot, fsyncs it, atomically renames it,
// and fsyncs the directory. An error prevents the caller from issuing a GitHub POST.
func (s *Store) mutate(change func(*snapshot) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := snapshot{Version: 1, Offset: s.data.Offset, Drafts: make(map[string]Draft, len(s.data.Drafts))}
	for key, draft := range s.data.Drafts {
		next.Drafts[key] = draft
	}
	if err := change(&next); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(s.path), ".state-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, s.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return err
	}
	s.data = next
	return nil
}
func (s *Store) BySource(source string) (Draft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.data.Drafts[source]
	return d, ok
}
func (s *Store) Get(token string) (Draft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.data.Drafts {
		if d.Token == token {
			return d, true
		}
	}
	return Draft{}, false
}
func (s *Store) Save(draft Draft) error {
	return s.mutate(func(data *snapshot) error {
		if _, exists := data.Drafts[draft.Source]; exists {
			return fmt.Errorf("source already exists")
		}
		data.Drafts[draft.Source] = draft
		return nil
	})
}
func (s *Store) RemoveReplaceable(token string) error {
	return s.mutate(func(data *snapshot) error {
		for source, draft := range data.Drafts {
			if draft.Token == token && (draft.State == "preview" || draft.State == "expired" || draft.State == "cancelled") {
				delete(data.Drafts, source)
			}
		}
		return nil
	})
}
func (s *Store) Transition(token, old, newState, url string) (bool, error) {
	changed := false
	err := s.mutate(func(data *snapshot) error {
		for source, draft := range data.Drafts {
			if draft.Token == token && draft.State == old {
				draft.State = newState
				draft.URL = url
				data.Drafts[source] = draft
				changed = true
			}
		}
		return nil
	})
	return changed, err
}
func (s *Store) Offset() int64 { s.mu.Lock(); defer s.mu.Unlock(); return s.data.Offset }
func (s *Store) SetOffset(offset int64) error {
	return s.mutate(func(data *snapshot) error { data.Offset = offset; return nil })
}

// Keep the returned file open for the entire process lifetime. Linux deployment only.
func LockState(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("another bot owns this state: %w", err)
	}
	return file, nil
}
