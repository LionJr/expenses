package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LionJr/expenses/internal/expense"
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
	requestTimeout := a.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = 5 * time.Second
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		t, err := template.ParseFS(web.Files, "templates/index.html")
		if err != nil {
			a.fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err = t.Execute(w, a.Timezone); err != nil {
			slog.Error("render panel", "error", err)
		}
	})
	assets, _ := fs.Sub(web.Files, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(assets))))
	mux.HandleFunc("GET /api/users", a.users)
	mux.HandleFunc("GET /api/expenses", a.list)
	mux.HandleFunc("POST /api/expenses", a.create)
	mux.HandleFunc("PATCH /api/expenses/{id}", a.patch)
	mux.HandleFunc("DELETE /api/expenses/{id}", a.delete)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
			defer cancel()
			r = r.WithContext(ctx)
			w.Header().Set("Cache-Control", "no-store")
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host && origin != "https://"+r.Host {
				write(w, 403, map[string]any{"error": map[string]string{"message": "запрос с другого сайта запрещён"}})
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func (a *API) fail(w http.ResponseWriter, err error) {
	status := 500
	message := "внутренняя ошибка сервера"
	if errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
		message = "Превышено время ожидания хранилища"
	} else if errors.Is(err, expense.ErrInvalid) {
		status = 400
		message = err.Error()
	} else if errors.Is(err, expense.ErrNotFound) {
		status = 404
		message = err.Error()
	} else if errors.Is(err, expense.ErrConflict) {
		status = http.StatusConflict
		message = err.Error()
	} else {
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
	write(w, 200, map[string]any{"items": v})
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
	if e := f.Validate(); e != nil {
		a.fail(w, e)
		return
	}
	v, e := a.Store.List(r.Context(), f)
	if e != nil {
		a.fail(w, e)
		return
	}
	write(w, 200, v)
}
func (a *API) create(w http.ResponseWriter, r *http.Request) {
	var i expense.Input
	if e := decode(w, r, &i); e != nil {
		a.fail(w, e)
		return
	}
	if e := i.Validate(); e != nil {
		a.fail(w, e)
		return
	}
	v, e := a.Store.Create(r.Context(), i)
	if e != nil {
		a.fail(w, e)
		return
	}
	write(w, 201, v)
}
func (a *API) patch(w http.ResponseWriter, r *http.Request) {
	id, e := pathID(r)
	if e != nil {
		a.fail(w, e)
		return
	}
	var p expense.Patch
	if e = decode(w, r, &p); e != nil {
		a.fail(w, e)
		return
	}
	if e = p.Validate(); e != nil {
		a.fail(w, e)
		return
	}
	v, e := a.Store.Update(r.Context(), 0, id, p)
	if e != nil {
		a.fail(w, e)
		return
	}
	write(w, 200, v)
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
	w.WriteHeader(204)
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
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
