package panelauth

import (
	"context"
	"crypto/cipher"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/LionJr/expenses/internal/config"
	"github.com/pquerna/otp/totp"
)

const SessionLifetime = 8 * time.Hour

type Service struct {
	Store    Store
	Origin   string
	cipher   cipher.AEAD
	masterID int64
	now      func() time.Time
}

func New(store Store, origin, encodedKey string, masterID int64) (*Service, error) {
	if masterID <= 0 {
		return nil, fmt.Errorf("panel master user ID must be positive")
	}
	key, err := hex.DecodeString(encodedKey)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("PANEL_AUTH_KEY must be 64 hexadecimal characters")
	}
	c, err := newCipher(key)
	if err != nil {
		return nil, err
	}
	s := &Service{Store: store, Origin: origin, cipher: c, masterID: masterID, now: time.Now}
	return s, nil
}

// Only the configured master may enroll or use the panel. Existing accounts
// receive the login page, never a way to bypass their password or MFA.
func (s *Service) Invite(ctx context.Context, id int64) (string, error) {
	if id != s.masterID {
		return "", ErrCredentials
	}
	link := s.Origin + "/auth"
	err := s.Store.MutatePanelAccount(ctx, id, true, func(a *Account) error {
		if a.Active {
			return nil
		}
		if a.LockedUntil.After(s.now()) {
			return ErrLocked
		}
		key, err := totp.Generate(totp.GenerateOpts{Issuer: "Family expenses", AccountName: strconv.FormatInt(id, 10), SecretSize: 20})
		if err != nil {
			return err
		}
		token := randomToken(32)
		a.InviteHash = digest(token)
		a.InviteExpires = s.now().Add(15 * time.Minute)
		a.Secret = s.encrypt(id, key.Secret())
		link += "#user=" + strconv.FormatInt(id, 10) + "&invite=" + token
		return nil
	})
	return link, err
}

func (s *Service) InspectInvite(ctx context.Context, id int64, token string) (Enrollment, error) {
	var result Enrollment
	if id != s.masterID {
		return result, ErrInvite
	}
	err := s.Store.MutatePanelAccount(ctx, id, false, func(a *Account) error {
		if !s.validInvite(a, token) {
			return ErrInvite
		}
		secret, err := s.decrypt(id, a.Secret)
		if err != nil {
			return err
		}
		result, err = enrollment(secret, id)
		return err
	})
	if errors.Is(err, ErrNotFound) {
		err = ErrInvite
	}
	return result, err
}

func (s *Service) validInvite(a *Account, token string) bool {
	return !a.Active && len(token) == 64 && a.InviteExpires.After(s.now()) && equal(a.InviteHash, digest(token))
}

// operation commits authentication failures rather than rolling back their budget.
func (s *Service) operation(ctx context.Context, id int64, fn func(*Account) error) error {
	return s.operationScope(ctx, id, false, fn)
}

func (s *Service) operationScope(ctx context.Context, id int64, private bool, fn func(*Account) error) error {
	if id != s.masterID {
		return ErrCredentials
	}
	var rejection error
	err := s.Store.MutatePanelAccount(ctx, id, false, func(a *Account) error {
		failures, locked := &a.Failures, &a.LockedUntil
		if private {
			failures, locked = &a.ReauthFailures, &a.ReauthLockedUntil
		}
		if locked.After(s.now()) {
			rejection = ErrLocked
			return nil
		}
		if !locked.IsZero() {
			*failures = 0
			*locked = time.Time{}
		}
		rejection = fn(a)
		// Wrong passwords are limited by source at HTTP admission. They must not
		// let an anonymous caller lock the owner's MFA/recovery budget by ID alone.
		if errors.Is(rejection, errPassword) {
			rejection = ErrCredentials
			return nil
		}
		if errors.Is(rejection, ErrCredentials) {
			*failures = *failures + 1
			if *failures >= 5 {
				*locked = s.now().Add(15 * time.Minute)
			}
			return nil
		}
		if rejection != nil {
			return rejection
		}
		*failures = 0
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return ErrCredentials
	}
	if err != nil {
		return err
	}
	return rejection
}

func (s *Service) Enroll(ctx context.Context, id int64, token, password, code string) ([]string, error) {
	if err := config.ValidatePanelPassword(strconv.FormatInt(id, 10), password); err != nil {
		return nil, err
	}
	var recovery []string
	err := s.operation(ctx, id, func(a *Account) error {
		if !s.validInvite(a, token) {
			return ErrInvite
		}
		secret, err := s.decrypt(id, a.Secret)
		if err != nil {
			return err
		}
		counter, ok := totpCounter(secret, code, s.now(), 0)
		if !ok {
			return ErrCredentials
		}
		hash, err := hashPassword(password)
		if err != nil {
			return err
		}
		a.PasswordHash = hash
		a.LastCounter = counter
		a.Active = true
		a.Version = randomToken(16)
		a.InviteHash = ""
		a.InviteExpires = time.Time{}
		recovery = newRecovery(a)
		return nil
	})
	return recovery, err
}

func newRecovery(a *Account) []string {
	codes := make([]string, 10)
	a.RecoveryHashes = make([]string, 10)
	for i := range codes {
		codes[i] = randomToken(16)
		a.RecoveryHashes[i] = digest(codes[i])
	}
	return codes
}

func (s *Service) verify(a *Account, id int64, password, code string) error {
	if !a.Active || !checkPassword(password, a.PasswordHash) {
		return errPassword
	}
	if len(code) == 32 {
		for i, hash := range a.RecoveryHashes {
			if equal(hash, digest(code)) {
				a.RecoveryHashes = append(a.RecoveryHashes[:i], a.RecoveryHashes[i+1:]...)
				return nil
			}
		}
		return ErrCredentials
	}
	secret, err := s.decrypt(id, a.Secret)
	if err != nil {
		return err
	}
	counter, ok := totpCounter(secret, code, s.now(), a.LastCounter)
	if !ok {
		return ErrCredentials
	}
	a.LastCounter = counter
	return nil
}

func (s *Service) Login(ctx context.Context, id int64, password, code string) (string, error) {
	var version string
	err := s.operation(ctx, id, func(a *Account) error {
		if err := s.verify(a, id, password, code); err != nil {
			return err
		}
		version = a.Version
		return nil
	})
	if err != nil {
		return "", err
	}
	token := randomToken(32)
	err = s.Store.CreatePanelSession(ctx, Session{Hash: digest(token), UserID: id, Version: version, Expires: s.now().Add(SessionLifetime)})
	return token, err
}

func (s *Service) Session(ctx context.Context, token string) (Session, error) {
	if len(token) != 64 {
		return Session{}, ErrCredentials
	}
	session, err := s.Store.FindPanelSession(ctx, digest(token), s.now())
	if err != nil {
		return Session{}, err
	}
	if session.UserID != s.masterID {
		return Session{}, ErrCredentials
	}
	return session, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	return s.Store.DeletePanelSession(ctx, digest(token))
}

func (s *Service) ChangePassword(ctx context.Context, id int64, password, code, replacement string) error {
	if err := config.ValidatePanelPassword(strconv.FormatInt(id, 10), replacement); err != nil {
		return err
	}
	return s.operationScope(ctx, id, true, func(a *Account) error {
		if err := s.verify(a, id, password, code); err != nil {
			return err
		}
		hash, err := hashPassword(replacement)
		if err != nil {
			return err
		}
		a.PasswordHash = hash
		a.Version = randomToken(16)
		a.Failures = 0
		a.LockedUntil = time.Time{}
		a.PendingSecret = ""
		a.PendingExpires = time.Time{}
		return nil
	})
}

func (s *Service) StartMFAReset(ctx context.Context, id int64, password, code string) (Enrollment, error) {
	var result Enrollment
	err := s.operationScope(ctx, id, true, func(a *Account) error {
		if err := s.verify(a, id, password, code); err != nil {
			return err
		}
		key, err := totp.Generate(totp.GenerateOpts{Issuer: "Family expenses", AccountName: strconv.FormatInt(id, 10), SecretSize: 20})
		if err != nil {
			return err
		}
		a.PendingSecret = s.encrypt(id, key.Secret())
		a.PendingExpires = s.now().Add(10 * time.Minute)
		result, err = enrollment(key.Secret(), id)
		return err
	})
	return result, err
}

func (s *Service) ConfirmMFAReset(ctx context.Context, id int64, code string) ([]string, error) {
	var recovery []string
	err := s.operationScope(ctx, id, true, func(a *Account) error {
		if !a.Active || !a.PendingExpires.After(s.now()) {
			return ErrCredentials
		}
		secret, err := s.decrypt(id, a.PendingSecret)
		if err != nil {
			return err
		}
		counter, ok := totpCounter(secret, code, s.now(), 0)
		if !ok {
			return ErrCredentials
		}
		a.Secret = a.PendingSecret
		a.PendingSecret = ""
		a.PendingExpires = time.Time{}
		a.LastCounter = counter
		a.Version = randomToken(16)
		a.Failures = 0
		a.LockedUntil = time.Time{}
		recovery = newRecovery(a)
		return nil
	})
	return recovery, err
}
