package ipmap

import (
	"errors"
	"sync"
	"testing"
)

// Close used to discard the closer's error, so a failed munmap looked like a
// clean release. The call that actually ran the closer reports its error; the
// idempotent repeats have nothing left to release and return nil, and the
// closer runs exactly once however many callers race to it.
func TestCloseReportsReleaseError(t *testing.T) {
	boom := errors.New("munmap failed")
	calls := 0
	m := &Map{close: func() error { calls++; return boom }}
	if err := m.Close(); !errors.Is(err, boom) {
		t.Fatalf("first Close = %v, want %v", err, boom)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
	if calls != 1 {
		t.Fatalf("closer ran %d times, want 1", calls)
	}

	// Raced Closes: exactly one caller sees the error.
	boomed := 0
	n := &Map{close: func() error { return boom }}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := n.Close(); err != nil {
				mu.Lock()
				boomed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if boomed != 1 {
		t.Fatalf("%d concurrent Close calls saw the error, want exactly 1", boomed)
	}
}
