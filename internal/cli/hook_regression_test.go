package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/angelnicolasc/flinch/internal/gitutil"
)

func TestReportParsesFlagsAfterID(t *testing.T) {
	repo := t.TempDir()
	if _, _, err := gitutil.Run(context.Background(), repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	var out, errors bytes.Buffer
	code := Run(context.Background(), []string{"report", "retry", "--at", "src/main.go:12", "--reason", "allowed case", "--json"}, &out, &errors)
	var got map[string]any
	if code != 0 || json.Unmarshal(out.Bytes(), &got) != nil || got["at"] != "src/main.go:12" || got["reason"] != "allowed case" || got["check_id"] != "retry" {
		t.Fatalf("report lost flags: %s (%s)", out.String(), errors.String())
	}
}

func TestHookInputObeysCallerDeadline(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = reader
	defer func() { os.Stdin = original; reader.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan int, 1)
	go func() {
		done <- Run(ctx, []string{"hook", "--host", "claude", "--event", "Stop"}, &bytes.Buffer{}, &bytes.Buffer{})
	}()
	select {
	case rc := <-done:
		writer.Close()
		if rc != 0 {
			t.Fatalf("hook did not fail open: %d", rc)
		}
	case <-time.After(500 * time.Millisecond):
		writer.Close()
		<-done
		t.Fatal("hook remained blocked on input after its deadline")
	}
}

func TestHookResponseObeysCallerDeadline(t *testing.T) {
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
	if err := writer.SetWriteDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Skip("this pipe does not support write deadlines")
	}
	// Fill the pipe completely. A Stop allow response on this host is three bytes.
	block := make([]byte, 4096)
	for {
		if _, err := writer.Write(block); err != nil {
			break
		}
	}
	if err := writer.SetWriteDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan int, 1)
	go func() {
		done <- Run(ctx, []string{"hook", "--host", "antigravity", "--event", "Stop"}, writer, &bytes.Buffer{})
	}()
	select {
	case exit := <-done:
		if exit != 0 {
			t.Fatalf("expected fail-open exit, got %d", exit)
		}
	case <-time.After(700 * time.Millisecond):
		reader.Close()
		<-done
		t.Fatal("hook remained blocked delivering its response")
	}
}
