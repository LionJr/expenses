package panelauth

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func authRequestFor(t *testing.T, handler http.Handler, path string, body any, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
	return authRequestFrom(t, handler, path, body, cookie, origin, "192.0.2.1:1234")
}

func authRequestFrom(t *testing.T, handler http.Handler, path string, body any, cookie *http.Cookie, origin, source string) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", path, strings.NewReader(string(data)))
	r.RemoteAddr = source
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", origin)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestAnonymousFloodCannotBlockOtherSourceLoginOrAuthenticatedLogout(t *testing.T) {
	f := newFixture(t)
	_, codes := f.enroll(t, 42)
	token, err := f.s.Login(t.Context(), 42, testPassword, codes[0])
	if err != nil {
		t.Fatal(err)
	}
	h, _ := NewHTTP(f.s, true)
	handler := h.Handler()
	for i := 0; i < publicRequestsPerMinute; i++ {
		w := authRequestFrom(t, handler, "/auth/login", map[string]string{"user_id": "0"}, nil, f.s.Origin, "198.51.100.2:111")
		if w.Code != 401 {
			t.Fatalf("anonymous request %d: %d", i, w.Code)
		}
	}
	w := authRequestFrom(t, handler, "/auth/login", map[string]string{"user_id": "0"}, nil, f.s.Origin, "198.51.100.2:111")
	if w.Code != 429 {
		t.Fatal("offending source not throttled")
	}
	w = authRequestFrom(t, handler, "/auth/login", map[string]string{"user_id": "42", "password": testPassword, "code": codes[1]}, nil, f.s.Origin, "203.0.113.3:222")
	if w.Code != 200 {
		t.Fatalf("valid owner blocked by other source: %d", w.Code)
	}
	cookie := &http.Cookie{Name: h.cookieName(), Value: token}
	// Even with both password gates occupied, logout from a source whose public
	// budget is exhausted must remain available and revoke the session.
	h.gate <- struct{}{}
	h.privateGate <- struct{}{}
	w = authRequestFrom(t, handler, "/auth/logout", map[string]string{}, cookie, f.s.Origin, "198.51.100.2:111")
	<-h.gate
	<-h.privateGate
	if w.Code != 200 {
		t.Fatalf("logout starved: %d", w.Code)
	}
	if _, err = f.s.Session(t.Context(), token); err == nil {
		t.Fatal("logout did not revoke session")
	}
}

type notifyingBody struct {
	io.Reader
	entered chan struct{}
	once    sync.Once
}

func (b *notifyingBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	return b.Reader.Read(p)
}

func TestSlowBodyDoesNotOccupyPasswordGate(t *testing.T) {
	f := newFixture(t)
	_, codes := f.enroll(t, 42)
	h, _ := NewHTTP(f.s, true)
	handler := h.Handler()
	reader, writer := io.Pipe()
	defer writer.Close()
	body := &notifyingBody{Reader: reader, entered: make(chan struct{})}
	r := httptest.NewRequest("POST", "/auth/login", body)
	r.RemoteAddr = "198.51.100.10:10"
	r.Header.Set("Origin", f.s.Origin)
	r.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() { defer close(done); handler.ServeHTTP(httptest.NewRecorder(), r) }()
	select {
	case <-body.entered:
	case <-time.After(time.Second):
		t.Fatal("slow read did not start")
	}
	w := authRequestFrom(t, handler, "/auth/login", map[string]string{"user_id": "42", "password": testPassword, "code": codes[0]}, nil, f.s.Origin, "203.0.113.20:20")
	if w.Code != 200 {
		t.Fatalf("slow body blocked authentication: %d", w.Code)
	}
	writer.Close()
	<-done
}

func TestAnonymousHashWorkCannotStarveAuthenticatedPasswordChange(t *testing.T) {
	f := newFixture(t)
	_, codes := f.enroll(t, 42)
	token, err := f.s.Login(t.Context(), 42, testPassword, codes[0])
	if err != nil {
		t.Fatal(err)
	}
	h, _ := NewHTTP(f.s, true)
	h.gate <- struct{}{}
	w := authRequestFor(t, h.Handler(), "/auth/password", map[string]string{"password": testPassword, "code": codes[1], "replacement": "A replacement safe personal passphrase"}, &http.Cookie{Name: h.cookieName(), Value: token}, f.s.Origin)
	<-h.gate
	if w.Code != 200 {
		t.Fatalf("private reauthentication shared public gate: %d", w.Code)
	}
}

func TestRealHTTPIncompleteAuthBodyTimesOut(t *testing.T) {
	f := newFixture(t)
	h, _ := NewHTTP(f.s, false)
	server := httptest.NewServer(h.Handler())
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "http://")
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	_, err = fmt.Fprintf(conn, "POST /auth/login HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n", address, f.s.Origin)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "POST"})
	if err != nil {
		t.Fatalf("server failed to stop incomplete upload: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 400 || !response.Close {
		t.Fatalf("incomplete request not rejected/closed: %d close=%v", response.StatusCode, response.Close)
	}
}

func TestPrivateThrottleIsPerSessionAndNeverBlocksLogout(t *testing.T) {
	f := newFixture(t)
	_, codes := f.enroll(t, 42)
	first, err := f.s.Login(t.Context(), 42, testPassword, codes[0])
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.s.Login(t.Context(), 42, testPassword, codes[1])
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.s.Session(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := NewHTTP(f.s, true)
	for i := 0; i < publicRequestsPerMinute; i++ {
		h.privateAdmission.allow(current.Hash, time.Now())
	}
	cookie := &http.Cookie{Name: h.cookieName(), Value: first}
	body := map[string]string{"password": testPassword, "code": codes[2], "replacement": "A replacement safe personal passphrase"}
	w := authRequestFor(t, h.Handler(), "/auth/password", body, cookie, f.s.Origin)
	if w.Code != 429 {
		t.Fatal("private password guesses not limited")
	}
	w = authRequestFor(t, h.Handler(), "/auth/logout", map[string]string{}, cookie, f.s.Origin)
	if w.Code != 200 {
		t.Fatal("private rate limit blocked logout")
	}
	w = authRequestFor(t, h.Handler(), "/auth/password", body, &http.Cookie{Name: h.cookieName(), Value: second}, f.s.Origin)
	if w.Code != 200 {
		t.Fatalf("one session blocked another: %d", w.Code)
	}
}

func TestHTTPPasswordOnlyAndBasicCannotBypassMFA(t *testing.T) {
	f := newFixture(t)
	_, codes := f.enroll(t, 42)
	h, err := NewHTTP(f.s, true)
	if err != nil {
		t.Fatal(err)
	}
	handler := h.Handler()
	login := authRequestFor(t, handler, "/auth/login", map[string]string{"user_id": "42", "password": testPassword}, nil, f.s.Origin)
	if login.Code != 401 || len(login.Result().Cookies()) != 0 {
		t.Fatal("password-only login obtained session")
	}
	protected := h.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	r := httptest.NewRequest("GET", "/api/users", nil)
	r.SetBasicAuth("42", testPassword)
	w := httptest.NewRecorder()
	protected.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("Basic Auth bypass accepted")
	}
	login = authRequestFor(t, handler, "/auth/login", map[string]string{"user_id": "42", "password": testPassword, "code": codes[0]}, nil, f.s.Origin)
	if login.Code != 200 {
		t.Fatalf("MFA login failed: %d %s", login.Code, login.Body)
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	cookie := cookies[0]
	if cookie.Name != "__Host-expenses_session" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("unsafe cookie: %+v", cookie)
	}
	r = httptest.NewRequest("GET", "/api/users", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	protected.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal("full MFA session rejected")
	}
	for _, origin := range []string{"", "https://attacker.example"} {
		r = httptest.NewRequest("DELETE", "/api/expenses/1", nil)
		r.AddCookie(cookie)
		r.Header.Set("Origin", origin)
		w = httptest.NewRecorder()
		protected.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("CSRF request accepted")
		}
	}
	logout := authRequestFor(t, handler, "/auth/logout", map[string]string{}, cookie, f.s.Origin)
	if logout.Code != 200 || logout.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout did not clear cookie")
	}
	r = httptest.NewRequest("GET", "/api/users", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	protected.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("copied logged-out cookie remained usable")
	}
}

func TestHTTPRejectsCrossOriginAndOversizedAuthBodies(t *testing.T) {
	f := newFixture(t)
	h, _ := NewHTTP(f.s, true)
	handler := h.Handler()
	for _, origin := range []string{"", "https://attacker.example"} {
		w := authRequestFor(t, handler, "/auth/login", map[string]string{}, nil, origin)
		if w.Code != 403 {
			t.Fatal("cross-origin auth accepted")
		}
	}
	w := authRequestFor(t, handler, "/auth/login", map[string]string{"password": strings.Repeat("x", 20000)}, nil, f.s.Origin)
	if w.Code != 400 {
		t.Fatalf("large body accepted: %d", w.Code)
	}
	if len(f.db.accounts) != 0 {
		t.Fatal("public login created an account")
	}
	r := httptest.NewRequest("GET", "/auth", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Вход с двумя факторами") || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("login page/headers invalid")
	}
}

func TestHTTPSensitiveChangeUsesSessionIdentity(t *testing.T) {
	f := newFixture(t)
	_, first := f.enroll(t, 42)
	_, other := f.formerMaster(t, 99)
	h, _ := NewHTTP(f.s, true)
	token, err := f.s.Login(t.Context(), 42, testPassword, first[0])
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: h.cookieName(), Value: token}
	w := authRequestFor(t, h.Handler(), "/auth/password", map[string]string{"user_id": "99", "password": testPassword, "code": first[1], "replacement": "An entirely new personal passphrase"}, cookie, f.s.Origin)
	if w.Code != 200 {
		t.Fatalf("password change failed: %d", w.Code)
	}
	previous, _ := New(f.db, f.s.Origin, testKey, 99)
	previous.now = f.s.now
	if _, err = previous.Login(t.Context(), 99, testPassword, other[0]); err != nil {
		t.Fatal("request body changed another member's password")
	}
}

func TestHTTPRejectsFormerNonMasterCookieForPanelAndAPI(t *testing.T) {
	f := newFixture(t)
	_, codes := f.formerMaster(t, 99)
	previous, _ := New(f.db, f.s.Origin, testKey, 99)
	previous.now = f.s.now
	token, err := previous.Login(t.Context(), 99, testPassword, codes[0])
	if err != nil {
		t.Fatal(err)
	}
	h, _ := NewHTTP(f.s, true)
	cookie := &http.Cookie{Name: h.cookieName(), Value: token}
	protected := h.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("non-master reached protected handler") }))
	for _, path := range []string{"/api/users", "/api/expenses", "/"} {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		protected.ServeHTTP(w, r)
		want := 401
		if path == "/" {
			want = 303
		}
		if w.Code != want {
			t.Fatalf("%s: status %d", path, w.Code)
		}
	}
	w := authRequestFor(t, h.Handler(), "/auth/login", map[string]string{"user_id": "99", "password": testPassword, "code": codes[1]}, nil, f.s.Origin)
	if w.Code != 401 || len(w.Result().Cookies()) != 0 {
		t.Fatal("non-master obtained new cookie")
	}
	w = authRequestFor(t, h.Handler(), "/auth/password", map[string]string{"user_id": "42", "password": testPassword, "code": codes[2], "replacement": "A replacement personal account password"}, cookie, f.s.Origin)
	if w.Code != 401 {
		t.Fatal("non-master cookie accepted for account management")
	}
}
