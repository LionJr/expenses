package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/LionJr/expenses/internal/agent"
	"github.com/LionJr/expenses/internal/botstate"
	"github.com/LionJr/expenses/internal/expense"
)

type Storage interface {
	UpdateUser(context.Context, int64, string, string) error
	LoadSession(context.Context, int64) (json.RawMessage, error)
	StartUpdate(context.Context, int64, int64) (string, string, error)
	FinishUpdate(context.Context, int64, string, json.RawMessage, int64) error
	MarkSent(context.Context, int64) error
	MarkChoiceSent(context.Context, int64, int64) error
	ChoicePromptCurrent(context.Context, int64, int64) (bool, error)
	SupersedeDelivery(context.Context, int64) error
	PendingDeliveries(context.Context, int) ([]botstate.Delivery, error)
	FailDelivery(context.Context, int64, int, bool, time.Time) error
}

type Responder interface {
	Reply(context.Context, int64, int64, string, agent.Session) (string, agent.Session, error)
}

type Bot struct {
	Token   string
	BaseURL string
	HTTP    *http.Client
	Allowed map[int64]bool
	Store   Storage
	Agent   Responder
}

type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
	IsBot     bool   `json:"is_bot"`
}

type Update struct {
	ID      int64 `json:"update_id"`
	Message *struct {
		ID   int64 `json:"message_id"`
		From *User `json:"from"`
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
}

type sentMessage struct {
	ID int64 `json:"message_id"`
}

type apiResponse struct {
	OK         bool            `json:"ok"`
	Result     json.RawMessage `json:"result"`
	ErrorCode  int             `json:"error_code"`
	Parameters struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (b *Bot) call(ctx context.Context, method string, payload any, result any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.BaseURL+"/bot"+b.Token+"/"+method, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("invalid Telegram URL")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("telegram connection failed")
	}
	defer resp.Body.Close()

	var envelope apiResponse
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope); err != nil {
		if resp.StatusCode != http.StatusOK {
			return &APIError{Code: resp.StatusCode}
		}
		return fmt.Errorf("invalid Telegram response")
	}
	if resp.StatusCode != http.StatusOK || !envelope.OK {
		code := envelope.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		return &APIError{Code: code, RetryAfter: time.Duration(envelope.Parameters.RetryAfter) * time.Second}
	}
	if result != nil {
		return json.Unmarshal(envelope.Result, result)
	}
	return nil
}

// One poller owns the offset and delivery retries. Each fetched batch processes
// users concurrently while preserving message order within each user. The offset
// advances only past a contiguous prefix of durably completed updates.
func (b *Bot) Run(ctx context.Context) error {
	var offset int64
	for ctx.Err() == nil {
		if err := b.deliverPending(ctx); err != nil {
			if ctx.Err() != nil {
				break
			}
			if fatal(err) {
				return err
			}
			slog.Warn("Telegram outbox deferred", "error", err)
			if !pause(ctx, 3*time.Second) {
				break
			}
			continue
		}
		var updates []Update
		// Limit idle long polling to the default delivery retry interval.
		err := b.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 3, "limit": 20, "allowed_updates": []string{"message"}}, &updates)
		if err != nil {
			if fatal(err) {
				return err
			}
			slog.Warn("Telegram polling failed", "error", err)
			if !pause(ctx, retryDelay(err)) {
				break
			}
			continue
		}
		completed, err := b.handleBatch(ctx, updates)
		if completed > 0 {
			offset = updates[completed-1].ID + 1
		}
		if err != nil {
			if fatal(err) {
				return err
			}
			slog.Warn("Telegram update deferred", "update_id", updates[completed].ID, "error", err)
			pause(ctx, 3*time.Second)
		}
	}
	return ctx.Err()
}

func pause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (b *Bot) Handle(ctx context.Context, u Update) error {
	m := u.Message
	if m == nil || m.From == nil || m.From.IsBot || m.Chat.Type != "private" || m.Chat.ID != m.From.ID || !b.Allowed[m.From.ID] {
		return nil
	}
	user := m.From.ID
	state, reply, err := b.Store.StartUpdate(ctx, u.ID, user)
	if err != nil {
		return err
	}
	if state == "sent" || state == "failed" || state == "done" || state == "superseded" {
		return nil
	}
	choicePrompt := false
	if state == "processing" {
		clean := func(s string) string { return strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\x00", "�") }
		if err = b.Store.UpdateUser(ctx, user, clean(m.From.FirstName+" "+m.From.LastName), clean(m.From.Username)); err != nil {
			return err
		}
		data, e := b.Store.LoadSession(ctx, user)
		if e != nil {
			return e
		}
		var session agent.Session
		if len(data) > 0 {
			if e = json.Unmarshal(data, &session); e != nil {
				// A permanently malformed context must not poison the global polling
				// queue. Mutation recovery still checks the operation journal first.
				slog.Warn("Telegram session reset", "user_id", user, "error_kind", "invalid_session")
				session = agent.Session{}
			}
		}
		switch m.Text {
		case "":
			reply = "Отправьте расход обычным текстом, например: кофе 350."
		case "/start":
			session = agent.Session{}
			reply = "Привет! Записываю ваши расходы в рублях. Напишите «кофе 350», «такси 1200 вчера», «покажи траты за неделю» или «сколько потратил на еду в этом месяце»."
		default:
			// A queued reply may have been sent before an outbox retry delivered the
			// new list. Require it to follow the prompt in the chat's message order.
			if agent.IsSelectionReply(m.Text) && session.Choice != nil && session.Choice.State == botstate.AwaitingSelection && session.Choice.MessageID > 0 && m.ID <= session.Choice.MessageID {
				reply = "Список был отправлен после вашего сообщения. Посмотрите полученный список и отправьте выбор ещё раз."
				break
			}
			if session.Choice != nil && session.Choice.State == botstate.AwaitingSelection && session.Choice.MessageID <= 0 {
				session.Choice = nil
			}
			reply, session, err = b.Agent.Reply(ctx, user, u.ID, m.Text, session)
			if err != nil {
				return err
			}
		}
		choicePrompt = session.Choice != nil && session.Choice.UpdateID == u.ID && session.Choice.State == botstate.AwaitingDelivery
		data, e = json.Marshal(session)
		if e != nil {
			return e
		}
		if err = b.Store.FinishUpdate(ctx, u.ID, reply, data, user); err != nil {
			if !errors.Is(err, expense.ErrInvalid) {
				return err
			}
			// Keep the confirmed operation result, but discard an unpersistable
			// context. Unknown storage/commit errors remain retryable as before.
			reply = clean(reply)
			if err = b.Store.FinishUpdate(ctx, u.ID, reply, json.RawMessage(`{}`), user); err != nil {
				return err
			}
			choicePrompt = false
		}
	}
	return b.deliver(ctx, botstate.Delivery{UpdateID: u.ID, UserID: user, Reply: reply, ChoicePrompt: choicePrompt})
}

func retryDelay(err error) time.Duration {
	var api *APIError
	if errors.As(err, &api) && api.RetryAfter > 0 {
		return api.RetryAfter
	}
	return 3 * time.Second
}

func (b *Bot) deliverPending(ctx context.Context) error {
	deliveries, err := b.Store.PendingDeliveries(ctx, 20)
	if err != nil {
		return err
	}
	for _, d := range deliveries {
		if err = b.deliver(ctx, d); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bot) deliver(ctx context.Context, d botstate.Delivery) error {
	if !b.Allowed[d.UserID] {
		return b.Store.FailDelivery(ctx, d.UpdateID, http.StatusForbidden, true, time.Now())
	}
	if d.ChoicePrompt {
		current, err := b.Store.ChoicePromptCurrent(ctx, d.UpdateID, d.UserID)
		if err != nil {
			return err
		}
		if !current {
			return b.Store.SupersedeDelivery(ctx, d.UpdateID)
		}
	}
	var sent sentMessage
	err := b.call(ctx, "sendMessage", map[string]any{"chat_id": d.UserID, "text": formatReply(d.Reply), "parse_mode": "HTML"}, &sent)
	if err == nil && d.ChoicePrompt && sent.ID <= 0 {
		err = fmt.Errorf("telegram choice prompt missing message ID")
	}
	if err == nil {
		if d.ChoicePrompt {
			return b.Store.MarkChoiceSent(ctx, d.UpdateID, sent.ID)
		}
		return b.Store.MarkSent(ctx, d.UpdateID)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if fatal(err) {
		return err
	}
	var api *APIError
	code := 0
	permanent := false
	if errors.As(err, &api) {
		code = api.Code
		permanent = code >= http.StatusBadRequest && code < http.StatusInternalServerError && code != http.StatusTooManyRequests
	}
	delay := retryDelay(err)
	if e := b.Store.FailDelivery(ctx, d.UpdateID, code, permanent, time.Now().Add(delay)); e != nil {
		return e
	}
	slog.Warn("Telegram delivery failed", "update_id", d.UpdateID, "error_code", code, "attempt", d.Attempts+1, "terminal", permanent || d.Attempts+1 >= 3, "retry_after_ms", delay.Milliseconds())
	return nil
}
