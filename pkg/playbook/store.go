package playbook

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/3dl-dev/ready/pkg/jsonl"
)

// PlaybooksFile is the append-only JSONL file of playbook template records under
// a project's .ready/ directory. It is the store-free, campfire-free home for
// playbook templates on the nostr-native path: each line is a serialized
// PlaybookTemplate, and when a playbook ID appears more than once the LAST
// (most recent) record wins. There is no campfire store and no .cf identity in
// this path — templates are project-local files, the items they instantiate are
// the only thing published to nostr.
const PlaybooksFile = "playbooks.jsonl"

// Store is a store-free playbook template store backed by an append-only JSONL
// file (typically <projectDir>/.ready/playbooks.jsonl). It has no dependency on
// the campfire SDK or any .cf identity.
type Store struct {
	path string
}

// NewStore returns a Store rooted at path.
func NewStore(path string) *Store { return &Store{path: path} }

// Path returns the underlying file path.
func (s *Store) Path() string { return s.path }

// Add validates and appends a playbook template as one JSON line, creating the
// parent directory on demand. Appends are serialized by an advisory file lock so
// concurrent writers never interleave a partial line.
func (s *Store) Add(t *PlaybookTemplate) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("playbook store mkdir: %w", err)
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("playbook store open: %w", err)
	}
	defer f.Close()
	if err := jsonl.LockFile(f); err != nil {
		return fmt.Errorf("playbook store lock: %w", err)
	}
	defer jsonl.UnlockFile(f) //nolint:errcheck // advisory unlock in defer
	data, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("playbook store marshal: %w", err)
	}
	data = append(data, '\n')
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("playbook store write: %w", err)
	}
	return f.Sync()
}

// List returns all registered playbook templates, latest-wins per ID, sorted by
// ID. A missing file is an empty store, not an error. Malformed lines are skipped
// so a single corrupt append never blinds the whole store.
func (s *Store) List() ([]*PlaybookTemplate, error) {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("playbook store open: %w", err)
	}
	defer f.Close()

	byID := map[string]*PlaybookTemplate{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var t PlaybookTemplate
		if err := json.Unmarshal(line, &t); err != nil {
			continue // skip malformed lines
		}
		tt := t
		byID[t.ID] = &tt // last record for an ID wins
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("playbook store scan: %w", err)
	}
	out := make([]*PlaybookTemplate, 0, len(byID))
	for _, t := range byID {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Find returns the registered playbook template with the given ID (latest-wins),
// or a not-found error.
func (s *Store) Find(id string) (*PlaybookTemplate, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	for _, t := range all {
		if t.ID == id {
			return t, nil
		}
	}
	return nil, fmt.Errorf("playbook %q not found", id)
}
