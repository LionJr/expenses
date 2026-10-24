package panelauth

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LionJr/expenses/internal/config"
	"github.com/LionJr/expenses/web"
)

type HTTP struct {
	Service          *Service
	Secure           bool
	TrustRenderProxy bool
	page             *template.Template
	gate             chan struct{}
	privateGate      chan struct{}
	admission        sourceAdmission
	privateAdmission sourceAdmission
}

func NewHTTP(s *Service, secure bool) (*HTTP, error) {
	page, err := template.ParseFS(web.Files, "templates/auth.html")
	if err != nil {
		return nil, err
	}
	return &HTTP{Service: s, Secure: secure, page: page, gate: make(chan struct{}, 1), privateGate: make(chan struct{}, 1)}, nil
}

func (h *HTTP) cookieName() string {
	if h.Secure {
		return "__Host-expenses_session"
	}
	return "expenses_session"
}

func (h *HTTP) cookie(w http.ResponseWriter, token string, maxAge int) {
	c := &http.Cookie{Name: h.cookieName(), Value: token, Path: "/", HttpOnly: true, Secure: h.Secure, SameSite: http.SameSiteStrictMode, MaxAge: maxAge}
	if maxAge < 0 {
		c.Expires = time.Unix(1, 0)
	} else {
		c.Expires = time.Now().Add(SessionLifetime)
	}
	http.SetCookie(w, c)
}

func (h *HTTP) session(r *http.Request) (Session, string, error) {
	cookie, err := r.Cookie(h.cookieName())
	if err != nil {
		return Session{}, "", ErrCredentials
	}
	session, err := h.Service.Session(r.Context(), cookie.Value)
	return session, cookie.Value, err
}

func (h *HTTP) headers(w http.ResponseWriter) {
	if h.Secure {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
}

func authJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func authError(w http.ResponseWriter, err error) {
	status, message := http.StatusServiceUnavailable, "Не удалось выполнить запрос. Повторите позже."
	switch {
	case errors.Is(err, ErrCredentials):
		status, message = 401, ErrCredentials.Error()
	case errors.Is(err, ErrInvite):
		status, message = 403, ErrInvite.Error()
	case errors.Is(err, ErrLocked):
		status, message = 429, ErrLocked.Error()
		w.Header().Set("Retry-After", "900")
	}
	authJSON(w, status, map[string]any{"error": map[string]string{"message": message}})
}

func (h *HTTP) sameOrigin(r *http.Request) bool {
	return r.Header.Get("Origin") == h.Service.Origin && r.Header.Get("Sec-Fetch-Site") != "cross-site"
}

// Protect authorizes every request from a revocable server-side session, never
// from Basic Auth or a password-only intermediate login state.
func (h *HTTP) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.headers(w)
		if r.Method != "GET" && r.Method != "HEAD" && !h.sameOrigin(r) {
			authJSON(w, 403, map[string]any{"error": map[string]string{"message": "Запрос с другого сайта запрещён"}})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		_, _, err := h.session(r.WithContext(ctx))
		if err != nil {
			if errors.Is(err, ErrCredentials) && !strings.HasPrefix(r.URL.Path, "/api/") {
				http.Redirect(w, r, "/auth", http.StatusSeeOther)
			} else {
				authError(w, err)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

type authRequest struct {
	UserID      string `json:"user_id"`
	Invite      string `json:"invite"`
	Password    string `json:"password"`
	Code        string `json:"code"`
	Replacement string `json:"replacement"`
}

func (h *HTTP) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, path := range []string{"GET /auth", "GET /auth/security"} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_ = h.page.Execute(w, nil)
		})
	}
	mux.HandleFunc("GET /auth/me", func(w http.ResponseWriter, r *http.Request) {
		session, _, err := h.session(r)
		if err != nil {
			authError(w, err)
			return
		}
		authJSON(w, 200, map[string]string{"user_id": strconv.FormatInt(session.UserID, 10)})
	})
	for _, path := range []string{"/auth/login", "/auth/invite", "/auth/enroll", "/auth/logout", "/auth/password", "/auth/mfa/start", "/auth/mfa/confirm"} {
		mux.HandleFunc("POST "+path, h.post)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.headers(w)
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h *HTTP) post(w http.ResponseWriter, r *http.Request) {
	if !h.sameOrigin(r) {
		authJSON(w, 403, map[string]any{"error": map[string]string{"message": "Запрос с другого сайта запрещён"}})
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		w.WriteHeader(415)
		return
	}
	private := r.URL.Path == "/auth/logout" || r.URL.Path == "/auth/password" || strings.HasPrefix(r.URL.Path, "/auth/mfa/")
	var session Session
	var token string
	if private {
		session, token, err = h.session(r)
		if err != nil {
			authError(w, err)
			return
		}
		if r.URL.Path != "/auth/logout" && !h.privateAdmission.allow(session.Hash, time.Now()) {
			w.Header().Set("Retry-After", "60")
			authJSON(w, 429, map[string]any{"error": map[string]string{"message": "Слишком много запросов. Подождите минуту."}})
			return
		}
	} else if !h.admission.allow(h.source(r), time.Now()) {
		w.Header().Set("Retry-After", "60")
		authJSON(w, 429, map[string]any{"error": map[string]string{"message": "Слишком много запросов. Подождите минуту."}})
		return
	}
	// Bound slow uploads before taking either password verification gate.
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(3 * time.Second))
	var body authRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF {
		// Keep the read deadline on malformed/incomplete uploads; otherwise
		// net/http could drain a slow unread body with its deadline removed.
		w.Header().Set("Connection", "close")
		w.WriteHeader(400)
		return
	}
	_ = controller.SetReadDeadline(time.Time{})
	id, _ := strconv.ParseInt(body.UserID, 10, 64)
	if private {
		id = session.UserID // Never accept the target identity from the request body.
	}
	if !private && id != h.Service.masterID {
		if r.URL.Path == "/auth/invite" || r.URL.Path == "/auth/enroll" {
			authError(w, ErrInvite)
		} else {
			authError(w, ErrCredentials)
		}
		return
	}
	if r.URL.Path != "/auth/logout" && r.URL.Path != "/auth/invite" {
		gate := h.gate
		if private {
			gate = h.privateGate
		}
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
		default:
			w.Header().Set("Retry-After", "2")
			authJSON(w, 429, map[string]any{"error": map[string]string{"message": "Вход занят. Повторите через несколько секунд."}})
			return
		}
	}
	switch r.URL.Path {
	case "/auth/invite":
		result, err := h.Service.InspectInvite(r.Context(), id, body.Invite)
		if err != nil {
			authError(w, err)
			return
		}
		authJSON(w, 200, result)
	case "/auth/enroll":
		if !validPassword(w, id, body.Password) {
			return
		}
		codes, err := h.Service.Enroll(r.Context(), id, body.Invite, body.Password, body.Code)
		if err != nil {
			authError(w, err)
			return
		}
		authJSON(w, 200, map[string]any{"recovery_codes": codes})
	case "/auth/login":
		if len(body.Password) > 1024 || len(body.Code) > 64 {
			authError(w, ErrCredentials)
			return
		}
		token, err := h.Service.Login(r.Context(), id, body.Password, body.Code)
		if err != nil {
			authError(w, err)
			return
		}
		h.cookie(w, token, int(SessionLifetime.Seconds()))
		authJSON(w, 200, map[string]bool{"ok": true})
	case "/auth/logout":
		if err := h.Service.Logout(r.Context(), token); err != nil {
			authError(w, err)
			return
		}
		h.cookie(w, "", -1)
		authJSON(w, 200, map[string]bool{"ok": true})
	case "/auth/password":
		if !validPassword(w, id, body.Replacement) {
			return
		}
		if err := h.Service.ChangePassword(r.Context(), id, body.Password, body.Code, body.Replacement); err != nil {
			authError(w, err)
			return
		}
		h.cookie(w, "", -1)
		authJSON(w, 200, map[string]bool{"ok": true})
	case "/auth/mfa/start":
		result, err := h.Service.StartMFAReset(r.Context(), id, body.Password, body.Code)
		if err != nil {
			authError(w, err)
			return
		}
		authJSON(w, 200, result)
	case "/auth/mfa/confirm":
		codes, err := h.Service.ConfirmMFAReset(r.Context(), id, body.Code)
		if err != nil {
			authError(w, err)
			return
		}
		h.cookie(w, "", -1)
		authJSON(w, 200, map[string]any{"recovery_codes": codes})
	}
}

func validPassword(w http.ResponseWriter, id int64, password string) bool {
	if err := config.ValidatePanelPassword(strconv.FormatInt(id, 10), password); err != nil {
		authJSON(w, 400, map[string]any{"error": map[string]string{"message": err.Error()}})
		return false
	}
	return true
}
