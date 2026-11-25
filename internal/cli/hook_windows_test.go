//go:build windows

package cli

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"
)

// TestWindowsHookWriterWithoutDeadlineReturnsOnTime covers the branch the
// deadline test above can't reach on Windows: an anonymous pipe has no write
// deadlines, so the hook writes from a goroutine and stops waiting when its
// budget ends. With a full pipe that nobody reads, the hook must still exit 0
// on time.
func TestWindowsHookWriterWithoutDeadlineReturnsOnTime(t *testing.T) {
	input, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	original := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = original }()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if err := writer.SetWriteDeadline(time.Now().Add(time.Second)); err == nil {
		t.Fatal("this pipe supports write deadlines, so the fallback writer would go untested")
	}
	// Fill the pipe until a write blocks. The filler ends when the reader closes.
	go func() {
		block := make([]byte, 4096)
		for {
			if _, err := writer.Write(block); err != nil {
				return
			}
		}
	}()
	time.Sleep(200 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	done := make(chan int, 1)
	go func() {
		done <- Run(ctx, []string{"hook", "--host", "antigravity", "--event", "Stop"}, writer, &bytes.Buffer{})
	}()
	select {
	case exit := <-done:
		if exit != 0 {
			t.Fatalf("expected exit 0, got %d", exit)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("the hook waited %s for a pipe nobody reads", elapsed)
		}
	case <-time.After(3 * time.Second):
		reader.Close()
		<-done
		t.Fatal("the hook stayed blocked on a full pipe without write deadlines")
	}
}
