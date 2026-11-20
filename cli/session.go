package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/Flonk/mazama/ui"
)

// maxHistory caps how many pages a session keeps to go back to.
const maxHistory = 100

// session is the browsing state the stateful commands share between runs:
// the current page and the pages before it, as they were shown. It lives in
// a JSON file, one per session name, so separate agents can browse
// independently.
type session struct {
	Current *ui.Page   `json:"current,omitempty"`
	History []*ui.Page `json:"history,omitempty"`

	path string
}

var sessionNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// sessionDir is where session files go; tests point it elsewhere.
var sessionDir = func() (string, error) {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "mazama", "sessions"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "mazama", "sessions"), nil
}

func loadSession(name string) (*session, error) {
	if !sessionNameRe.MatchString(name) || name == "." || name == ".." {
		return nil, fmt.Errorf("bad session name %q: use letters, digits, '.', '_' and '-'", name)
	}
	dir, err := sessionDir()
	if err != nil {
		return nil, err
	}
	s := &session{path: filepath.Join(dir, name+".json")}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("reading session %s: %v", s.path, err)
	}
	return s, nil
}

// save writes the session atomically.
func (s *session) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// push makes p the current page, keeping the old one for back.
func (s *session) push(p *ui.Page) {
	if s.Current != nil {
		s.History = append(s.History, s.Current)
		if len(s.History) > maxHistory {
			s.History = s.History[len(s.History)-maxHistory:]
		}
	}
	s.Current = p
}

// replace swaps the current page (reload, or answering an input prompt).
func (s *session) replace(p *ui.Page) { s.Current = p }

func (s *session) back() (*ui.Page, error) {
	if len(s.History) == 0 {
		return nil, errors.New("no history")
	}
	s.Current = s.History[len(s.History)-1]
	s.History = s.History[:len(s.History)-1]
	return s.Current, nil
}

func (s *session) current() (*ui.Page, error) {
	if s.Current == nil {
		return nil, errors.New("no page open in this session; start with: mazama open <address>")
	}
	return s.Current, nil
}
