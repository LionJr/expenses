package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/LionJr/expenses/internal/expense"
	"github.com/LionJr/expenses/internal/expensewire"
	"github.com/LionJr/expenses/web"
)

type Repository interface {
	Users(context.Context) ([]expense.User, error)
	List(context.Context, expense.Filter) (expense.Page, error)
	Create(context.Context, expense.Input) (expense.Expense, error)
	Update(context.Context, int64, int64, expense.Patch) (expense.Expense, error)
	Delete(context.Context, int64, int64) error
}

type API struct {
	Store          Repository
	Timezone       string
	RequestTimeout time.Duration
}

func (a *API) Handler() http.Handler {
	t, templateErr := template.ParseFS(web.Files, "templates/index.html")

	mux := http.NewServeMux()
	mux.HandleFunc(http.MethodGet+" /{$}", func(w http.ResponseWriter, r *http.Request) {
		if templateErr != nil {
			a.fail(w, templateErr)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := t.Execute(w, a.Timezone); err != nil {
			slog.Error("render panel", "error", err)
		}
	})
	// For embed.FS, Sub only checks path syntax; the fixed "static" path is valid.
	assets, _ := fs.Sub(web.Files, "static")
	mux.Handle(http.MethodGet+" /static/", http.StripPrefix("/static/", http.FileServer(http.FS(assets))))

	mux.HandleFunc(http.MethodGet+" /api/users", a.users)
	mux.HandleFunc(http.MethodGet+" /api/expenses", a.list)
	mux.HandleFunc(http.MethodPost+" /api/expenses", a.create)
	mux.HandleFunc(http.MethodPatch+" /api/expenses/{id}", a.patch)
	mux.HandleFunc(http.MethodDelete+" /api/expenses/{id}", a.delete)

	handler := withSameOrigin(mux)
	handler = withAPITimeout(handler, a.RequestTimeout)
	return withSecurityHeaders(handler)
}

func (a *API) fail(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "внутренняя ошибка сервера"
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		status = http.StatusGatewayTimeout
		message = "Превышено время ожидания хранилища"
	case errors.Is(err, expense.ErrInvalid):
		status = http.StatusBadRequest
		message = "Некорректные данные запроса"
	case errors.Is(err, expense.ErrNotFound):
		status = http.StatusNotFound
		message = "Расход не найден"
	case errors.Is(err, expense.ErrConflict):
		status = http.StatusConflict
		message = "Конфликт изменения расхода. Обновите данные и повторите запрос."
	default:
		slog.Error("API request failed", "error", err)
	}
	write(w, status, map[string]any{"error": map[string]string{"message": message}})
}
func (a *API) users(w http.ResponseWriter, r *http.Request) {
	v, e := a.Store.Users(r.Context())
	if e != nil {
		a.fail(w, e)
		return
	}
	write(w, http.StatusOK, map[string]any{"items": expensewire.FromUsers(v)})
}
func (a *API) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := expense.Filter{Category: q.Get("category"), From: q.Get("date_from"), To: q.Get("date_to")}
	for k, dst := range map[string]*int{"limit": &f.Limit, "offset": &f.Offset} {
		if v := q.Get(k); v != "" {
			n, e := strconv.Atoi(v)
			if e != nil {
				a.fail(w, expense.Invalid("неверный "+k))
				return
			}
			*dst = n
		}
	}
	if v := q.Get("user_id"); v != "" {
		n, e := strconv.ParseInt(v, 10, 64)
		if e != nil || n <= 0 {
			a.fail(w, expense.Invalid("неверный участник"))
			return
		}
		f.UserID = n
	}
	v, e := a.Store.List(r.Context(), f)
	if e != nil {
		a.fail(w, e)
		return
	}
	write(w, http.StatusOK, expensewire.FromPage(v))
}
func (a *API) create(w http.ResponseWriter, r *http.Request) {
	var body expensewire.Input
	if e := decode(w, r, &body); e != nil {
		a.fail(w, e)
		return
	}
	i := body.Domain()
	v, e := a.Store.Create(r.Context(), i)
	if e != nil {
		a.fail(w, e)
		return
	}
	write(w, http.StatusCreated, expensewire.FromExpense(v))
}
func (a *API) patch(w http.ResponseWriter, r *http.Request) {
	id, e := pathID(r)
	if e != nil {
		a.fail(w, e)
		return
	}
	var body expensewire.Patch
	if e = decode(w, r, &body); e != nil {
		a.fail(w, e)
		return
	}
	p := body.Domain()
	v, e := a.Store.Update(r.Context(), 0, id, p)
	if e != nil {
		a.fail(w, e)
		return
	}
	write(w, http.StatusOK, expensewire.FromExpense(v))
}
func (a *API) delete(w http.ResponseWriter, r *http.Request) {
	id, e := pathID(r)
	if e == nil {
		e = a.Store.Delete(r.Context(), 0, id)
	}
	if e != nil {
		a.fail(w, e)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return expense.Invalid("нужен Content-Type application/json")
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return expense.Invalid("некорректный JSON или тип поля")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return expense.Invalid("ожидался один JSON объект")
	}
	return nil
}
func pathID(r *http.Request) (int64, error) {
	id, e := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if e != nil || id <= 0 {
		return 0, expense.Invalid("неверный ID")
	}
	return id, nil
}
