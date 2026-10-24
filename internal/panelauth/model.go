// Package panelauth implements invite-only personal accounts with mandatory TOTP.
package panelauth

import (
	"context"
	"errors"
	"time"
)

var (
	ErrCredentials = errors.New("неверные данные входа или код")
	ErrLocked      = errors.New("слишком много попыток; повторите через 15 минут")
	ErrInvite      = errors.New("ссылка недействительна или истекла; запросите /panel у бота")
	ErrNotFound    = errors.New("account not found")
	errPassword    = errors.New("password verification failed")
)

// All secrets stored here are encrypted or hashed. Mutation runs under a DB
// row lock, including failure budgets, replay counters and recovery consumption.
type Account struct {
	Active            bool      `json:"active"`
	PasswordHash      string    `json:"password_hash,omitempty"`
	Secret            string    `json:"secret,omitempty"`
	LastCounter       int64     `json:"last_counter"`
	RecoveryHashes    []string  `json:"recovery_hashes,omitempty"`
	InviteHash        string    `json:"invite_hash,omitempty"`
	InviteExpires     time.Time `json:"invite_expires,omitempty"`
	PendingSecret     string    `json:"pending_secret,omitempty"`
	PendingExpires    time.Time `json:"pending_expires,omitempty"`
	Failures          int       `json:"failures"`
	LockedUntil       time.Time `json:"locked_until,omitempty"`
	ReauthFailures    int       `json:"reauth_failures"`
	ReauthLockedUntil time.Time `json:"reauth_locked_until,omitempty"`
	Version           string    `json:"version,omitempty"`
}

type Session struct {
	Hash    string
	UserID  int64
	Version string
	Expires time.Time
}

type Store interface {
	MutatePanelAccount(context.Context, int64, bool, func(*Account) error) error
	CreatePanelSession(context.Context, Session) error
	FindPanelSession(context.Context, string, time.Time) (Session, error)
	DeletePanelSession(context.Context, string) error
}
