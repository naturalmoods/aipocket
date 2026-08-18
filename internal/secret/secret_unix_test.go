//go:build unix

package secret

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The cap is only worth anything if the resolver actually goes through it. This
// watches a real helper occupy a slot for as long as it runs — sh only, because
// cmd.exe has no sleep and the property is not platform-specific.
func TestResolvingACommandOccupiesASlot(t *testing.T) {
	src, err := Parse("command:sleep 0.4; echo sk-test-abcdefghijklmnop")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan string, 1)
	go func() {
		v, err := src.Resolve(context.Background())
		if err != nil {
			t.Error(err)
		}
		done <- v
	}()

	deadline := time.Now().Add(5 * time.Second)
	for len(commandSlots) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the helper ran without taking a slot: the cap is not wired up")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if got := <-done; got != "sk-test-abcdefghijklmnop" {
		t.Errorf("resolved %q", got)
	}
	if len(commandSlots) != 0 {
		t.Errorf("the slot was not released: %d still held", len(commandSlots))
	}
}

// A FIFO is the case that made the regular-file check load-bearing rather than
// cosmetic: os.Open on one blocks until something writes, so `file:/tmp/fifo`
// would hang the whole run — before any request, with no timeout in the path to
// cut it short.
func TestFIFOCredentialFileDoesNotBlockTheRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("cannot create a fifo here: %v", err)
	}
	s, err := Parse("file:" + path)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { _, err := s.Resolve(context.Background()); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a fifo was accepted as a credential file")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Resolve blocked on a fifo with no writer")
	}
}
