package bot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/rakhshon-mirzoev/coinkeeper/internal/user"
)

const (
	member   int64 = 200 // уже вступил по ссылке
	stranger int64 = 300

	code = "family-2026_ok"
)

type fakeUsers struct {
	members map[int64]bool
	err     error
}

func (f *fakeUsers) Upsert(_ context.Context, u user.User) error {
	f.members[u.ID] = true
	return nil
}

func (f *fakeUsers) Exists(_ context.Context, id int64) (bool, error) {
	return f.members[id], f.err
}

func testBot(inviteCode string) *Bot {
	users := &fakeUsers{members: map[int64]bool{member: true}}
	return newBot(inviteCode, users, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestCheckAccess(t *testing.T) {
	tests := []struct {
		name       string
		inviteCode string
		userID     int64
		text       string
		want       accessVerdict
	}{
		// Приглашения выключены — бот открыт.
		{"open bot", "", stranger, "кофе 350", accessGranted},
		{"open bot ignores start payload", "", stranger, "/start whatever", accessGranted},

		// Приглашения включены.
		{"existing member", code, member, "кофе 350", accessGranted},
		{"member with any start", code, member, "/start whatever", accessGranted},
		{"stranger without code", code, stranger, "кофе 350", accessDenied},
		{"stranger plain start", code, stranger, "/start", accessDenied},
		{"correct code", code, stranger, "/start " + code, accessJoined},
		{"correct code with bot mention", code, stranger, "/start@coin_bot " + code, accessJoined},
		{"wrong code", code, stranger, "/start wrong-code-1", accessWrongCode},
		{"code prefix", code, stranger, "/start " + code[:8], accessWrongCode},
		{"code without start", code, stranger, code, accessDenied},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := testBot(tt.inviteCode).checkAccess(context.Background(), tt.userID, tt.text)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("verdict = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCheckAccess_StoreError(t *testing.T) {
	b := newBot(code, &fakeUsers{err: errors.New("db down")}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if _, err := b.checkAccess(context.Background(), stranger, "кофе 350"); err == nil {
		t.Fatal("store error must not grant access silently")
	}
}

func TestStartPayload(t *testing.T) {
	tests := []struct {
		text    string
		payload string
		ok      bool
	}{
		{"/start", "", true},
		{"/start abc", "abc", true},
		{"  /start   abc  ", "abc", true},
		{"/start@my_bot abc", "abc", true},
		{"/started abc", "", false},
		{"кофе 350", "", false},
		{"", "", false},
	}

	for _, tt := range tests {
		payload, ok := startPayload(tt.text)
		if payload != tt.payload || ok != tt.ok {
			t.Errorf("startPayload(%q) = %q, %v; want %q, %v", tt.text, payload, ok, tt.payload, tt.ok)
		}
	}
}
