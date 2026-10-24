package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LionJr/expenses/internal/panelauth"
	"github.com/LionJr/expenses/internal/postgres"
	"github.com/pquerna/otp/totp"
)

func checkPersonalPanelAccounts(t *testing.T, store *postgres.Store) {
	ctx := context.Background()
	if _, err := postgres.DBForTest(store).ExecContext(ctx, `TRUNCATE panel_sessions,panel_accounts`); err != nil {
		t.Fatal(err)
	}
	const key = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const password = "Individual family member password"
	s, err := panelauth.New(store, "https://family.example", key, 101)
	if err != nil {
		t.Fatal(err)
	}
	enroll := func(id int64) (panelauth.Enrollment, []string) {
		t.Helper()
		link, err := s.Invite(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(link)
		q, _ := url.ParseQuery(u.Fragment)
		token := q.Get("invite")
		e, err := s.InspectInvite(ctx, id, token)
		if err != nil {
			t.Fatal(err)
		}
		code, err := totp.GenerateCode(e.Secret, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		codes, err := s.Enroll(ctx, id, token, password, code)
		if err != nil {
			t.Fatal(err)
		}
		return e, codes
	}
	e, codes := enroll(101)
	master := s
	s, _ = panelauth.New(store, master.Origin, key, 202)
	_, other := enroll(202)
	previous := s
	s = master
	oldToken, err := previous.Login(ctx, 202, password, other[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Session(ctx, oldToken); !errors.Is(err, panelauth.ErrCredentials) {
		t.Fatal("non-master session retained panel access")
	}
	var stored string
	if err = postgres.DBForTest(store).QueryRowContext(ctx, `SELECT data::text FROM panel_accounts WHERE user_id=101`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{password, e.Secret, codes[0]} {
		if strings.Contains(stored, secret) {
			t.Fatal("plaintext credential persisted")
		}
	}
	token, err := s.Login(ctx, 101, password, codes[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Session(ctx, token); err != nil {
		t.Fatal(err)
	}
	var rawTokens int
	if err = postgres.DBForTest(store).QueryRowContext(ctx, `SELECT count(*) FROM panel_sessions WHERE token_hash=$1`, token).Scan(&rawTokens); err != nil || rawTokens != 0 {
		t.Fatal("raw session token stored")
	}
	restarted, _ := panelauth.New(store, s.Origin, key, 101)
	if _, err = restarted.Session(ctx, token); err != nil {
		t.Fatal("session not durable")
	}
	replacement := "A replacement individual family password"
	if err = s.ChangePassword(ctx, 101, password, codes[1], replacement); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Session(ctx, token); !errors.Is(err, panelauth.ErrCredentials) {
		t.Fatal("revoked session survived restart")
	}
	token, err = restarted.Login(ctx, 101, replacement, codes[2])
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Session(ctx, token); !errors.Is(err, panelauth.ErrCredentials) {
		t.Fatal("logout not durable")
	}
	results := make(chan error, 8)
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Go(func() { _, e := restarted.Login(ctx, 101, replacement, codes[3]); results <- e })
	}
	workers.Wait()
	close(results)
	successes := 0
	for result := range results {
		if result == nil {
			successes++
		} else if !errors.Is(result, panelauth.ErrCredentials) && !errors.Is(result, panelauth.ErrLocked) {
			t.Fatal(result)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent recovery succeeded %d times", successes)
	}
	if _, err = s.Login(ctx, 202, password, other[1]); !errors.Is(err, panelauth.ErrCredentials) {
		t.Fatal("bot-only family member allowed panel login")
	}
	// Exactly four backup codes have been consumed, including only one winner
	// of the concurrent attempt, and the account budget survives new services.
	var a panelauth.Account
	if err = postgres.DBForTest(store).QueryRowContext(ctx, `SELECT data::text FROM panel_accounts WHERE user_id=101`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(stored), &a); err != nil || len(a.RecoveryHashes) != 6 {
		t.Fatalf("recovery consumption: %d %v", len(a.RecoveryHashes), err)
	}
	if !a.LockedUntil.After(time.Now()) {
		t.Fatal("account failure budget not persisted")
	}
	if _, err = s.Login(ctx, 101, replacement, codes[4]); !errors.Is(err, panelauth.ErrLocked) {
		t.Fatal("persistent lock bypassed")
	}
	// Operator recovery removes only panel credentials and their sessions.
	if _, err = postgres.DBForTest(store).ExecContext(ctx, `DELETE FROM panel_accounts WHERE user_id=101`); err != nil {
		t.Fatal(err)
	}
	if link, err := s.Invite(ctx, 101); err != nil || !strings.Contains(link, "#user=101&invite=") {
		t.Fatal("operator reset did not allow new enrollment")
	}
}
