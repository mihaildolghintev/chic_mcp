package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestAppendAndRecent(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		if err := d.AppendMessage(ctx, 42, role, fmt.Sprintf("msg-%d", i)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	got, err := d.RecentMessages(ctx, 42, 3)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d messages, want 3", len(got))
	}
	// The last 3, oldest first.
	for i, want := range []string{"msg-3", "msg-4", "msg-5"} {
		if got[i].Content != want {
			t.Errorf("message %d = %q, want %q", i, got[i].Content, want)
		}
	}
	if got[1].Role != "assistant" {
		t.Errorf("role = %q, want assistant", got[1].Role)
	}
}

func TestRecentMessages_EmptyChat(t *testing.T) {
	d := openTest(t)
	got, err := d.RecentMessages(context.Background(), 1, 10)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d messages for empty chat, want 0", len(got))
	}
}

func TestChatsAreIsolated(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()

	if err := d.AppendMessage(ctx, 1, "user", "for chat 1"); err != nil {
		t.Fatal(err)
	}
	if err := d.AppendMessage(ctx, 2, "user", "for chat 2"); err != nil {
		t.Fatal(err)
	}

	got, err := d.RecentMessages(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "for chat 1" {
		t.Errorf("chat 1 sees %+v, want only its own message", got)
	}
}

// TestConcurrentWrites exercises the single-writer pool under the race
// detector: parallel appends must all land, none may error with SQLITE_BUSY.
func TestConcurrentWrites(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()

	const goroutines, perGoroutine = 10, 20
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*perGoroutine)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				if err := d.AppendMessage(ctx, int64(g), "user", fmt.Sprintf("g%d-i%d", g, i)); err != nil {
					errs <- err
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent append failed: %v", err)
	}

	for g := 0; g < goroutines; g++ {
		got, err := d.RecentMessages(ctx, int64(g), perGoroutine+5)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != perGoroutine {
			t.Errorf("chat %d has %d messages, want %d", g, len(got), perGoroutine)
		}
	}
}
