//go:build unix

package secret

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

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
