package levels

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestState_PeekDefaultsToAskWhenFileMissing(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "level.state"), true)
	level, err := s.Peek()
	if err != nil {
		t.Fatalf("Peek() error = %v", err)
	}
	if level != Ask {
		t.Errorf("Peek() = %q, want Ask", level)
	}
}

func TestState_SetThenPeek(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "level.state"), true)
	if err := s.Set(GitHub); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	level, err := s.Peek()
	if err != nil {
		t.Fatalf("Peek() error = %v", err)
	}
	if level != GitHub {
		t.Errorf("Peek() = %q, want GitHub", level)
	}
}

func TestState_SetRejectsAsk(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "level.state"), true)
	if err := s.Set(Ask); err == nil {
		t.Error("Set(Ask) error = nil, want error")
	}
}

func TestState_PeekTreatsGarbageAsAsk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "level.state")
	s := New(path, true)
	if err := s.write(Level("not-a-real-level")); err != nil {
		t.Fatalf("write() error = %v", err)
	}
	level, err := s.Peek()
	if err != nil {
		t.Fatalf("Peek() error = %v", err)
	}
	if level != Ask {
		t.Errorf("Peek() = %q, want Ask for unrecognized contents", level)
	}
}

func TestState_ConsumeSingleUseResetsToAsk(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "level.state"), true)
	if err := s.Set(Research); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	got, err := s.Consume()
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if got != Research {
		t.Fatalf("first Consume() = %q, want Research", got)
	}

	got, err = s.Consume()
	if err != nil {
		t.Fatalf("second Consume() error = %v", err)
	}
	if got != Ask {
		t.Errorf("second Consume() = %q, want Ask (level should have been consumed)", got)
	}
}

func TestState_ConsumeNonSingleUseDoesNotReset(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "level.state"), false)
	if err := s.Set(Full); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	for i := 0; i < 2; i++ {
		got, err := s.Consume()
		if err != nil {
			t.Fatalf("Consume() #%d error = %v", i, err)
		}
		if got != Full {
			t.Errorf("Consume() #%d = %q, want Full (persistent mode should not reset)", i, got)
		}
	}
}

// TestState_ConcurrentConsumeOnlyOneWinner is the concurrency scenario
// called out explicitly in the implementation spec's acceptance criteria:
// with single_use_level enabled, two /start calls racing on the same
// selected level must not both observe it — exactly one does, the other
// sees Ask. Two independent *State values (rather than one shared value)
// simulate two separate request handlers pointed at the same state file,
// the way two concurrent HTTP handlers in the real server would be.
func TestState_ConcurrentConsumeOnlyOneWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "level.state")
	setter := New(path, true)
	if err := setter.Set(GitHub); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	a := New(path, true)
	b := New(path, true)

	var wg sync.WaitGroup
	results := make([]Level, 2)
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		results[0], errs[0] = a.Consume()
	}()
	go func() {
		defer wg.Done()
		results[1], errs[1] = b.Consume()
	}()
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Consume() #%d error = %v", i, err)
		}
	}

	githubCount, askCount := 0, 0
	for _, level := range results {
		switch level {
		case GitHub:
			githubCount++
		case Ask:
			askCount++
		default:
			t.Fatalf("unexpected result %q", level)
		}
	}
	if githubCount != 1 || askCount != 1 {
		t.Errorf("results = %v, want exactly one GitHub and one Ask", results)
	}
}

func TestParseLevel(t *testing.T) {
	for _, level := range []Level{Offline, GitHub, Research, Full} {
		got, err := ParseLevel(string(level))
		if err != nil {
			t.Errorf("ParseLevel(%q) error = %v", level, err)
		}
		if got != level {
			t.Errorf("ParseLevel(%q) = %q", level, got)
		}
	}
	for _, s := range []string{"ask", "", "bogus"} {
		if _, err := ParseLevel(s); err == nil {
			t.Errorf("ParseLevel(%q) error = nil, want error", s)
		}
	}
}
