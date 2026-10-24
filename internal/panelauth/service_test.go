package panelauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

const testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const testPassword = "Our personal unusual household phrase"

type memoryStore struct {
	mu       sync.Mutex
	accounts map[int64]Account
	sessions map[string]Session
}

func (m *memoryStore) MutatePanelAccount(_ context.Context, id int64, create bool, fn func(*Account) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.accounts[id]
	if !ok && !create {
		return ErrNotFound
	}
	// Match DB rollback semantics; callbacks may consume recovery slices.
	data, _ := json.Marshal(a)
	a = Account{}
	_ = json.Unmarshal(data, &a)
	if err := fn(&a); err != nil {
		return err
	}
	m.accounts[id] = a
	return nil
}
func (m *memoryStore) CreatePanelSession(_ context.Context, s Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.Hash] = s
	return nil
}
func (m *memoryStore) FindPanelSession(_ context.Context, hash string, now time.Time) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[hash]
	a := m.accounts[s.UserID]
	if !ok || !s.Expires.After(now) || !a.Active || a.Version != s.Version {
		return Session{}, ErrCredentials
	}
	return s, nil
}
func (m *memoryStore) DeletePanelSession(_ context.Context, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, hash)
	return nil
}

type fixture struct {
	s   *Service
	db  *memoryStore
	now time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := &memoryStore{accounts: map[int64]Account{}, sessions: map[string]Session{}}
	s, err := New(db, "https://family.example", testKey, 42)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{s: s, db: db, now: time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)}
	s.now = func() time.Time { return f.now }
	return f
}
func (f *fixture) invite(t *testing.T, id int64) (string, Enrollment) {
	t.Helper()
	link, err := f.s.Invite(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	fragment, _ := url.ParseQuery(u.Fragment)
	token := fragment.Get("invite")
	e, err := f.s.InspectInvite(context.Background(), id, token)
	if err != nil || e.Secret == "" || !strings.HasPrefix(e.QR, "data:image/png;base64,") {
		t.Fatalf("enrollment failed: %v", err)
	}
	return token, e
}
func code(t *testing.T, secret string, now time.Time) string {
	t.Helper()
	result, err := totp.GenerateCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func (f *fixture) enroll(t *testing.T, id int64) (Enrollment, []string) {
	t.Helper()
	token, e := f.invite(t, id)
	codes, err := f.s.Enroll(context.Background(), id, token, testPassword, code(t, e.Secret, f.now))
	if err != nil || len(codes) != 10 {
		t.Fatalf("cannot enroll: %v", err)
	}
	return e, codes
}

// Simulate credentials created by a deployment that previously allowed this ID.
func (f *fixture) formerMaster(t *testing.T, id int64) (Enrollment, []string) {
	t.Helper()
	original := f.s
	previous, err := New(f.db, original.Origin, testKey, id)
	if err != nil {
		t.Fatal(err)
	}
	previous.now = original.now
	f.s = previous
	e, codes := f.enroll(t, id)
	f.s = original
	return e, codes
}

func TestPersonalAccountsRequireBothFactorsAndRejectReplay(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	e, codes := f.enroll(t, 42)
	a := f.db.accounts[42]
	serialized, _ := json.Marshal(a)
	for _, secret := range []string{testPassword, e.Secret, codes[0]} {
		if strings.Contains(string(serialized), secret) {
			t.Fatal("stored plaintext credential")
		}
	}
	if _, err := f.s.Login(ctx, 42, testPassword, ""); !errors.Is(err, ErrCredentials) {
		t.Fatal("password-only login accepted")
	}
	if _, err := f.s.Login(ctx, 42, testPassword, code(t, e.Secret, f.now)); !errors.Is(err, ErrCredentials) {
		t.Fatal("enrollment code replay accepted")
	}
	f.now = f.now.Add(30 * time.Second)
	current := code(t, e.Secret, f.now)
	if _, err := f.s.Login(ctx, 42, "wrong", current); !errors.Is(err, ErrCredentials) {
		t.Fatal("MFA-only login accepted")
	}
	token, err := f.s.Login(ctx, 42, testPassword, current)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.db.sessions[token]; ok {
		t.Fatal("raw session token stored")
	}
	if _, err = f.s.Session(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Login(ctx, 42, testPassword, current); !errors.Is(err, ErrCredentials) {
		t.Fatal("TOTP replay accepted")
	}
	if link, err := f.s.Invite(ctx, 42); err != nil || link != "https://family.example/auth" {
		t.Fatal("existing account got a bypass invite")
	}
	if err = f.s.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Session(ctx, token); !errors.Is(err, ErrCredentials) {
		t.Fatal("logged out session survived")
	}
}

func TestInvitationExpiryReplacementAndOneUse(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	old, _ := f.invite(t, 42)
	token, e := f.invite(t, 42)
	if _, err := f.s.InspectInvite(ctx, 42, old); !errors.Is(err, ErrInvite) {
		t.Fatal("superseded invite accepted")
	}
	if _, err := f.s.InspectInvite(ctx, 99, token); !errors.Is(err, ErrInvite) {
		t.Fatal("invite transferred to another user")
	}
	if _, err := f.s.Enroll(ctx, 42, token, "weak", code(t, e.Secret, f.now)); err == nil {
		t.Fatal("weak password accepted")
	}
	f.now = f.now.Add(16 * time.Minute)
	if _, err := f.s.InspectInvite(ctx, 42, token); !errors.Is(err, ErrInvite) {
		t.Fatal("expired invite accepted")
	}
	token, e = f.invite(t, 42)
	if _, err := f.s.Enroll(ctx, 42, token, testPassword, code(t, e.Secret, f.now)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Enroll(ctx, 42, token, "A completely different household password", code(t, e.Secret, f.now)); !errors.Is(err, ErrInvite) {
		t.Fatal("enrollment replay accepted")
	}
}

func TestPersistentAccountLockCannotBeResetByNewProcessOrAffectOtherMember(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, codes := f.enroll(t, 42)
	_, other := f.formerMaster(t, 99)
	for i := 0; i < 5; i++ {
		_, _ = f.s.Login(ctx, 42, testPassword, "")
	}
	restarted, err := New(f.db, f.s.Origin, testKey, 42)
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = f.s.now
	if _, err = restarted.Login(ctx, 42, testPassword, codes[0]); !errors.Is(err, ErrLocked) {
		t.Fatal("restart bypassed lock")
	}
	if _, err = restarted.Login(ctx, 99, testPassword, other[0]); !errors.Is(err, ErrCredentials) {
		t.Fatal("regular family member was allowed into panel")
	}
	f.now = f.now.Add(15 * time.Minute)
	if _, err = restarted.Login(ctx, 42, testPassword, codes[0]); err != nil {
		t.Fatal("lock did not expire")
	}
}

func TestRecoveryCodeConsumedOnceAcrossConcurrentRequests(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, codes := f.enroll(t, 42)
	results := make(chan error, 4)
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Go(func() { _, err := f.s.Login(ctx, 42, testPassword, codes[0]); results <- err })
	}
	workers.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("recovery code accepted %d times", successes)
	}
}

func TestPasswordAndMFAChangesRevokeSessionsAndOldRecoveryCodes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, codes := f.enroll(t, 42)
	first, err := f.s.Login(ctx, 42, testPassword, codes[0])
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.s.Login(ctx, 42, testPassword, codes[1])
	if err != nil {
		t.Fatal(err)
	}
	newPassword := "A replacement unique household passphrase"
	if err = f.s.ChangePassword(ctx, 42, testPassword, codes[2], newPassword); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{first, second} {
		if _, err = f.s.Session(ctx, token); !errors.Is(err, ErrCredentials) {
			t.Fatal("old session survived password change")
		}
	}
	token, err := f.s.Login(ctx, 42, newPassword, codes[3])
	if err != nil {
		t.Fatal(err)
	}
	next, err := f.s.StartMFAReset(ctx, 42, newPassword, codes[4])
	if err != nil {
		t.Fatal(err)
	}
	updated, err := f.s.ConfirmMFAReset(ctx, 42, code(t, next.Secret, f.now))
	if err != nil || len(updated) != 10 {
		t.Fatal(err)
	}
	if _, err = f.s.Session(ctx, token); !errors.Is(err, ErrCredentials) {
		t.Fatal("session survived MFA rotation")
	}
	if _, err = f.s.Login(ctx, 42, newPassword, codes[5]); !errors.Is(err, ErrCredentials) {
		t.Fatal("old recovery code survived MFA rotation")
	}
	if _, err = f.s.Login(ctx, 42, newPassword, updated[0]); err != nil {
		t.Fatal(err)
	}
}

func TestSessionsExpireAndRemovedAllowlistMemberIsRejected(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, codes := f.enroll(t, 42)
	token, err := f.s.Login(ctx, 42, testPassword, codes[0])
	if err != nil {
		t.Fatal(err)
	}
	removed, _ := New(f.db, f.s.Origin, testKey, 99)
	removed.now = f.s.now
	if _, err = removed.Session(ctx, token); !errors.Is(err, ErrCredentials) {
		t.Fatal("removed member retained session")
	}
	f.now = f.now.Add(SessionLifetime)
	if _, err = f.s.Session(ctx, token); !errors.Is(err, ErrCredentials) {
		t.Fatal("expired session accepted")
	}
}

func TestMFAEncryptionBoundToAccountAndKey(t *testing.T) {
	f := newFixture(t)
	encrypted := f.s.encrypt(42, "SECRETBASE32")
	if _, err := f.s.decrypt(99, encrypted); err == nil {
		t.Fatal("secret transferable between users")
	}
	other, _ := New(f.db, f.s.Origin, strings.Repeat("ab", 32), 42)
	if _, err := other.decrypt(42, encrypted); err == nil {
		t.Fatal("wrong key accepted")
	}
	if _, err := f.s.decrypt(42, encrypted[:len(encrypted)-2]); err == nil {
		t.Fatal("corrupted ciphertext accepted")
	}
}

func TestOnlyMasterCanEnrollLoginOrUseExistingSessions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	previous, err := New(f.db, f.s.Origin, testKey, 99)
	if err != nil {
		t.Fatal(err)
	}
	previous.now = f.s.now
	link, err := previous.Invite(ctx, 99)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	q, _ := url.ParseQuery(u.Fragment)
	token := q.Get("invite")
	if _, err = f.s.Invite(ctx, 99); !errors.Is(err, ErrCredentials) {
		t.Fatal("bot-only user got a panel invite")
	}
	if _, err = f.s.InspectInvite(ctx, 99, token); !errors.Is(err, ErrInvite) {
		t.Fatal("old non-master invite exposed enrollment")
	}
	e, err := previous.InspectInvite(ctx, 99, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Enroll(ctx, 99, token, testPassword, code(t, e.Secret, f.now)); !errors.Is(err, ErrCredentials) {
		t.Fatal("non-master enrolled using old valid link")
	}
	codes, err := previous.Enroll(ctx, 99, token, testPassword, code(t, e.Secret, f.now))
	if err != nil {
		t.Fatal(err)
	}
	session, err := previous.Login(ctx, 99, testPassword, codes[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Login(ctx, 99, testPassword, codes[1]); !errors.Is(err, ErrCredentials) {
		t.Fatal("non-master credentials accepted")
	}
	if _, err = f.s.Session(ctx, session); !errors.Is(err, ErrCredentials) {
		t.Fatal("non-master old cookie accepted")
	}
	if err = f.s.ChangePassword(ctx, 99, testPassword, codes[2], "A different unique family password"); !errors.Is(err, ErrCredentials) {
		t.Fatal("non-master password operation accepted")
	}
	if _, err = f.s.StartMFAReset(ctx, 99, testPassword, codes[2]); !errors.Is(err, ErrCredentials) {
		t.Fatal("non-master MFA operation accepted")
	}
	if _, err = f.s.ConfirmMFAReset(ctx, 99, "123456"); !errors.Is(err, ErrCredentials) {
		t.Fatal("non-master MFA confirmation accepted")
	}
	if len(f.db.accounts[99].RecoveryHashes) != 9 {
		t.Fatal("denied requests consumed another account's credentials")
	}
	_, masterCodes := f.enroll(t, 42)
	if _, err = f.s.Login(ctx, 42, testPassword, masterCodes[0]); err != nil {
		t.Fatal("master denied")
	}
}

func TestWrongPasswordsCannotLockOwnerByIDAndRecoveryHasSeparateBudget(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, codes := f.enroll(t, 42)
	for i := 0; i < 6; i++ {
		if _, err := f.s.Login(ctx, 42, "unknown-password", ""); !errors.Is(err, ErrCredentials) {
			t.Fatal(err)
		}
	}
	if f.db.accounts[42].Failures != 0 || !f.db.accounts[42].LockedUntil.IsZero() {
		t.Fatal("wrong passwords locked owner account")
	}
	// A correct password with incorrect MFA must still hit the persistent limit.
	for i := 0; i < 5; i++ {
		_, _ = f.s.Login(ctx, 42, testPassword, "")
	}
	if _, err := f.s.Login(ctx, 42, testPassword, codes[0]); !errors.Is(err, ErrLocked) {
		t.Fatal("MFA budget missing")
	}
	newPassword := "Another unique personal replacement phrase"
	if err := f.s.ChangePassword(ctx, 42, testPassword, codes[0], newPassword); err != nil {
		t.Fatalf("public MFA lock blocked private recovery: %v", err)
	}
	if _, err := f.s.Login(ctx, 42, newPassword, codes[1]); err != nil {
		t.Fatalf("secure password change did not clear public lock: %v", err)
	}
}
