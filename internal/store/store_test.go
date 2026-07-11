package store

import (
	"context"
	"database/sql"
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

// TestStartSessionHidesOlderMessages: after a session boundary the replayed
// history starts from a clean slate, per chat, without deleting anything.
func TestStartSessionHidesOlderMessages(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()

	if err := d.AppendMessage(ctx, 42, "user", "старый вопрос"); err != nil {
		t.Fatal(err)
	}
	if err := d.AppendMessage(ctx, 7, "user", "чужой чат"); err != nil {
		t.Fatal(err)
	}
	if err := d.StartSession(ctx, 42); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	got, err := d.RecentMessages(ctx, 42, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("after reset chat 42 sees %+v, want nothing", got)
	}

	if err := d.AppendMessage(ctx, 42, "user", "новый вопрос"); err != nil {
		t.Fatal(err)
	}
	got, err = d.RecentMessages(ctx, 42, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "новый вопрос" {
		t.Errorf("after reset chat 42 sees %+v, want only the new message", got)
	}

	// The boundary is per chat: chat 7 still remembers.
	got, err = d.RecentMessages(ctx, 7, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "чужой чат" {
		t.Errorf("chat 7 sees %+v, want its message intact", got)
	}
}

func TestSessionEpochRollsOverOnReset(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()

	// No reset yet: epoch is the zero value, shared by every fresh chat.
	first, err := d.SessionEpoch(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if first != 0 {
		t.Errorf("epoch before any reset = %d, want 0", first)
	}

	if err := d.AppendMessage(ctx, 42, "user", "вопрос"); err != nil {
		t.Fatal(err)
	}
	// Appending messages must not move the epoch — only /new does.
	if same, err := d.SessionEpoch(ctx, 42); err != nil {
		t.Fatal(err)
	} else if same != first {
		t.Errorf("epoch changed on append: %d -> %d", first, same)
	}

	if err := d.StartSession(ctx, 42); err != nil {
		t.Fatal(err)
	}
	after, err := d.SessionEpoch(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if after == first {
		t.Errorf("epoch did not change after reset, still %d", after)
	}

	// A second reset rolls it over again.
	if err := d.StartSession(ctx, 42); err != nil {
		t.Fatal(err)
	}
	if again, err := d.SessionEpoch(ctx, 42); err != nil {
		t.Fatal(err)
	} else if again == after {
		t.Errorf("epoch did not change after second reset, still %d", again)
	}

	// The epoch is per user: chat 7 never reset, so it stays at zero.
	if other, err := d.SessionEpoch(ctx, 7); err != nil {
		t.Fatal(err)
	} else if other != 0 {
		t.Errorf("chat 7 epoch = %d, want 0 (its own boundary)", other)
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

func TestMessagesSinceSkipsBoundariesAndWatermark(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()

	if err := d.AppendMessage(ctx, 42, "user", "старое"); err != nil {
		t.Fatal(err)
	}
	if err := d.StartSession(ctx, 42); err != nil { // inserts a 'reset' row
		t.Fatal(err)
	}
	if err := d.AppendMessage(ctx, 42, "user", "новое-1"); err != nil {
		t.Fatal(err)
	}
	if err := d.AppendMessage(ctx, 42, "assistant", "новое-2"); err != nil {
		t.Fatal(err)
	}

	// Since the very start: reset rows are never returned, only dialog turns.
	all, err := d.MessagesSince(ctx, 42, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("MessagesSince from 0 = %+v, want 3 dialog turns (no reset)", all)
	}
	for _, m := range all {
		if m.Role == "reset" || m.ID == 0 {
			t.Errorf("unexpected row %+v (reset leaked or missing id)", m)
		}
	}

	// The watermark excludes everything at or before it.
	watermark := all[0].ID
	rest, err := d.MessagesSince(ctx, 42, watermark, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 2 || rest[0].Content != "новое-1" {
		t.Errorf("MessagesSince after watermark = %+v, want the two newer turns", rest)
	}

	// limit caps the result.
	if capped, err := d.MessagesSince(ctx, 42, 0, 1); err != nil || len(capped) != 1 {
		t.Fatalf("MessagesSince limit=1 = %+v, %v; want 1", capped, err)
	}
}

func TestSessionSummaryRoundTrip(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()

	// Missing row is not an error: empty summary, zero watermark.
	if s, up, err := d.GetSessionSummary(ctx, 42, 0); err != nil || s != "" || up != 0 {
		t.Fatalf("missing summary = %q, %d, %v; want empty", s, up, err)
	}

	if err := d.PutSessionSummary(ctx, 42, 5, "сводка A", 12); err != nil {
		t.Fatal(err)
	}
	s, up, err := d.GetSessionSummary(ctx, 42, 5)
	if err != nil {
		t.Fatal(err)
	}
	if s != "сводка A" || up != 12 {
		t.Errorf("round-trip = %q, %d; want \"сводка A\", 12", s, up)
	}

	// Upsert on the same (user, epoch) key advances the watermark, not duplicates.
	if err := d.PutSessionSummary(ctx, 42, 5, "сводка B", 20); err != nil {
		t.Fatal(err)
	}
	if s, up, _ := d.GetSessionSummary(ctx, 42, 5); s != "сводка B" || up != 20 {
		t.Errorf("after upsert = %q, %d; want \"сводка B\", 20", s, up)
	}

	// A different epoch is a separate row — old summary untouched.
	if s, _, _ := d.GetSessionSummary(ctx, 42, 6); s != "" {
		t.Errorf("epoch 6 = %q, want empty (own row)", s)
	}
}

func TestPreferences(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()

	if got, err := d.Preferences(ctx, 42); err != nil || len(got) != 0 {
		t.Fatalf("empty preferences = %+v, %v", got, err)
	}

	if err := d.SetPreference(ctx, 42, "language", "en"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetPreference(ctx, 42, "reply_style", "кратко"); err != nil {
		t.Fatal(err)
	}
	// A repeated key overwrites, not duplicates.
	if err := d.SetPreference(ctx, 42, "language", "de"); err != nil {
		t.Fatal(err)
	}

	got, err := d.Preferences(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	// Ordered by key: language, reply_style.
	want := []Preference{{Key: "language", Value: "de"}, {Key: "reply_style", Value: "кратко"}}
	if len(got) != len(want) {
		t.Fatalf("preferences = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("preference %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// Preferences are per user.
	if other, err := d.Preferences(ctx, 7); err != nil || len(other) != 0 {
		t.Errorf("user 7 sees %+v, %v — want none", other, err)
	}

	// Deleting is idempotent and scoped to the key.
	if err := d.DeletePreference(ctx, 42, "language"); err != nil {
		t.Fatal(err)
	}
	if err := d.DeletePreference(ctx, 42, "language"); err != nil {
		t.Fatalf("deleting a missing key must not error: %v", err)
	}
	got, err = d.Preferences(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != "reply_style" {
		t.Errorf("after delete = %+v, want only reply_style", got)
	}
}

// TestPreferencesCap: the whole profile is injected into every prompt, so
// Preferences must never return more than the cap even if more are stored.
func TestPreferencesCap(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()

	for i := 0; i < maxRenderedPreferences+5; i++ {
		if err := d.SetPreference(ctx, 42, fmt.Sprintf("key_%03d", i), "v"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.Preferences(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != maxRenderedPreferences {
		t.Errorf("Preferences returned %d, want cap of %d", len(got), maxRenderedPreferences)
	}
}

// TestPreferencesSurviveSessionReset: /new resets dialog history but must not
// touch durable preferences — the bot forgets the conversation, not the person.
func TestPreferencesSurviveSessionReset(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()

	if err := d.SetPreference(ctx, 42, "language", "en"); err != nil {
		t.Fatal(err)
	}
	if err := d.AppendMessage(ctx, 42, "user", "старый вопрос"); err != nil {
		t.Fatal(err)
	}
	if err := d.StartSession(ctx, 42); err != nil {
		t.Fatal(err)
	}

	if hist, err := d.RecentMessages(ctx, 42, 10); err != nil || len(hist) != 0 {
		t.Fatalf("history after reset = %+v, %v — want empty", hist, err)
	}
	prefs, err := d.Preferences(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefs) != 1 || prefs[0] != (Preference{Key: "language", Value: "en"}) {
		t.Errorf("preferences after reset = %+v, want language=en intact", prefs)
	}
}

// TestMigrateLegacyChatID exercises adopting goose on a pre-goose database:
// an old chat_id-keyed table with no goose_db_version is migrated in place to
// the per-user schema, carrying every message over (chat_id == user_id in the
// private chats the bot serves) without loss.
func TestMigrateLegacyChatID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")

	// Hand-build the pre-migration schema and seed it.
	legacy, err := sql.Open("sqlite", "file:"+path+dsnPragmas)
	if err != nil {
		t.Fatal(err)
	}
	const oldSchema = `
CREATE TABLE messages (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	chat_id    INTEGER NOT NULL,
	role       TEXT    NOT NULL,
	content    TEXT    NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE INDEX idx_messages_chat ON messages(chat_id, id);`
	if _, err := legacy.Exec(oldSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(
		`INSERT INTO messages (chat_id, role, content, created_at) VALUES (42, 'user', 'до миграции', 100), (42, 'assistant', 'ответ', 101)`,
	); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	// Open through the store: migrate must rebuild the table in place.
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrate): %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	got, err := d.RecentMessages(context.Background(), 42, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Content != "до миграции" || got[1].Content != "ответ" {
		t.Errorf("migrated history = %+v, want the two legacy messages under user 42", got)
	}

	// The new schema and user_memory table are usable after migration.
	if err := d.SetPreference(context.Background(), 42, "language", "en"); err != nil {
		t.Errorf("user_memory unusable after migration: %v", err)
	}
}

// TestMigrationsRoundTrip drives every migration Up → Down → Up on a fresh
// database. It proves the Down migrations actually roll back (production never
// exercises them, so they rot silently otherwise) and that re-applying from a
// rolled-back state lands the current schema cleanly.
func TestMigrationsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := sql.Open("sqlite", "file:"+path+dsnPragmas)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	provider, err := newProvider(db)
	if err != nil {
		t.Fatal(err)
	}

	tableExists := func(name string) bool {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name,
		).Scan(&n); err != nil {
			t.Fatalf("check table %q: %v", name, err)
		}
		return n > 0
	}

	// Up: full schema present.
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !tableExists("messages") || !tableExists("user_memory") {
		t.Fatal("after Up, expected messages and user_memory tables")
	}

	// Down to zero: every Down migration must run without error and remove
	// what its Up created.
	if _, err := provider.DownTo(ctx, 0); err != nil {
		t.Fatalf("DownTo(0): %v", err)
	}
	if tableExists("messages") || tableExists("user_memory") {
		t.Fatal("after DownTo(0), tables should be gone — a Down migration no-op'd")
	}

	// Up again: re-applying from scratch yields the current per-user schema.
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("second Up: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO messages (user_id, role, content, created_at) VALUES (1, 'user', 'hi', 1)`,
	); err != nil {
		t.Errorf("messages not in per-user shape after round trip: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO user_memory (user_id, key, value, updated_at) VALUES (1, 'language', 'en', 1)`,
	); err != nil {
		t.Errorf("user_memory missing after round trip: %v", err)
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
