// Package levels reads handbox's network-level state file and maps the
// current level to an OpenSandbox networkPolicy.
package levels

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Level is a network egress level selected via the ai-level CLI.
type Level string

const (
	// Ask is the default/unset state: a hard stop, not a live prompt.
	// /start must refuse to create a sandbox while the level is Ask.
	Ask      Level = "ask"
	Offline  Level = "offline"
	GitHub   Level = "github"
	Research Level = "research"
	Full     Level = "full"
)

// settable holds the levels ai-level may write to the state file. Ask is
// deliberately excluded — it's the file's default/reset value, not
// something a level selection sets it to.
var settable = map[Level]bool{
	Offline:  true,
	GitHub:   true,
	Research: true,
	Full:     true,
}

// ParseLevel validates a level string as typed by a human, e.g. via the
// ai-level CLI. Unlike the lenient parsing used when reading the state
// file, an invalid value here is reported rather than silently treated as
// Ask.
func ParseLevel(s string) (Level, error) {
	level := Level(strings.TrimSpace(s))
	if !settable[level] {
		return "", fmt.Errorf("invalid level %q: must be one of offline, github, research, full", s)
	}
	return level, nil
}

// parseStored parses the raw contents of the state file. Per the
// network-level design, the file's default value is "ask", and any
// missing or unrecognized value is treated as "ask" rather than erroring
// — handbox must never guess a level from garbage state.
func parseStored(raw string) Level {
	level := Level(strings.TrimSpace(raw))
	if settable[level] {
		return level
	}
	return Ask
}

// State reads and writes handbox's network-level state file. The file
// must live on the host filesystem, never bind-mounted into any container
// the agent can reach (see the implementation spec's Section 6 security
// requirement) — State has no opinion on that; it's the caller's
// responsibility to point it at the right path.
type State struct {
	path      string
	singleUse bool
}

// New returns a State backed by the state file at path. When singleUse is
// true, Consume atomically resets the file back to Ask after reading it,
// so each selected level is good for exactly one /start call.
func New(path string, singleUse bool) *State {
	return &State{path: path, singleUse: singleUse}
}

// Peek reads the current level without consuming it. A missing file or
// unrecognized contents read as Ask.
func (s *State) Peek() (Level, error) {
	unlock, err := s.lock()
	if err != nil {
		return "", err
	}
	defer unlock()
	return s.read()
}

// Consume atomically reads the current level and, when singleUse is set,
// resets the file back to Ask within the same locked critical section —
// two concurrent Consume calls can never both observe the same non-Ask
// level. Consume itself never errors just because the level is Ask;
// deciding that Ask is a hard stop is Policy's job, not State's.
func (s *State) Consume() (Level, error) {
	unlock, err := s.lock()
	if err != nil {
		return "", err
	}
	defer unlock()

	level, err := s.read()
	if err != nil {
		return "", err
	}
	if s.singleUse && level != Ask {
		if err := s.write(Ask); err != nil {
			return "", err
		}
	}
	return level, nil
}

// Set writes level to the state file. Used by the ai-level CLI.
func (s *State) Set(level Level) error {
	if !settable[level] {
		return fmt.Errorf("cannot set level %q", level)
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return s.write(level)
}

func (s *State) read() (Level, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Ask, nil
	}
	if err != nil {
		return "", fmt.Errorf("read state file: %w", err)
	}
	return parseStored(string(data)), nil
}

func (s *State) write(level Level) error {
	if err := os.WriteFile(s.path, []byte(level), 0o600); err != nil {
		return fmt.Errorf("write state file: %w", err)
	}
	return nil
}

// lock acquires an exclusive advisory lock for the duration of a
// read-modify-write critical section, so two concurrent /start calls
// can't both observe the same level before either clears it. The lock is
// held on a sibling ".lock" file rather than the state file itself so
// read/write of the state file's contents never has to reason about the
// lock's own fd.
func (s *State) lock() (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	f, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
