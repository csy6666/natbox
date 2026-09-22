package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"natbox/internal/auth"
)

const (
	adminCookieName = "natbox_session"
	sessionLifetime = 12 * time.Hour
)

type adminAuth struct {
	hash     string
	secure   bool
	mu       sync.Mutex
	sessions map[string]adminSession
	failures map[string]loginFailure
}

type adminSession struct {
	CSRF      string
	ExpiresAt time.Time
}

type loginFailure struct {
	Count   int
	ResetAt time.Time
}

func newAdminAuth(hash string) (*adminAuth, error) {
	hash = strings.TrimSpace(hash)
	if hash != "" {
		if _, err := auth.Parse(hash); err != nil {
			return nil, err
		}
	}
	return &adminAuth{hash: hash, secure: os.Getenv("NATBOX_COOKIE_SECURE") == "1", sessions: map[string]adminSession{}, failures: map[string]loginFailure{}}, nil
}

func (a *adminAuth) enabled() bool { return a != nil && a.hash != "" }

func (a *adminAuth) status(w http.ResponseWriter) {
	write(w, envelope{Success: true, Data: map[string]bool{"adminEnabled": a.enabled()}})
}

func (a *adminAuth) login(w http.ResponseWriter, r *http.Request, record func(string, string)) {
	if !a.enabled() {
		writeStatus(w, http.StatusNotFound, "administrator login is disabled")
		return
	}
	if !a.allowAttempt(requestIP(r)) {
		writeStatus(w, http.StatusTooManyRequests, "too many login attempts; retry later")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var request struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !auth.VerifyPassword(a.hash, request.Password) {
		a.failed(requestIP(r))
		record("failure", "")
		writeStatus(w, http.StatusUnauthorized, "invalid administrator credentials")
		return
	}
	a.clearFailure(requestIP(r))
	sessionID, csrf, err := randomToken(), randomToken(), error(nil)
	if sessionID == "" || csrf == "" {
		err = errRandomToken
	}
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "could not create session")
		return
	}
	a.mu.Lock()
	a.sessions[sessionID] = adminSession{CSRF: csrf, ExpiresAt: time.Now().Add(sessionLifetime)}
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: adminCookieName, Value: sessionID, Path: "/", HttpOnly: true, Secure: a.secure || r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: int(sessionLifetime.Seconds())})
	record("success", "")
	write(w, envelope{Success: true, Data: map[string]any{"username": "admin", "csrfToken": csrf, "expiresAt": time.Now().Add(sessionLifetime).UTC()}})
}

func (a *adminAuth) logout(w http.ResponseWriter, r *http.Request, record func(string, string)) {
	if cookie, err := r.Cookie(adminCookieName); err == nil {
		a.mu.Lock()
		delete(a.sessions, cookie.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: adminCookieName, Value: "", Path: "/", HttpOnly: true, Secure: a.secure || r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	record("success", "")
	write(w, envelope{Success: true, Data: map[string]string{"status": "logged_out"}})
}

func (a *adminAuth) me(w http.ResponseWriter, r *http.Request) {
	if session, ok := a.session(r); ok {
		write(w, envelope{Success: true, Data: map[string]any{"username": "admin", "csrfToken": session.CSRF, "expiresAt": session.ExpiresAt.UTC()}})
		return
	}
	writeStatus(w, http.StatusUnauthorized, "authentication required")
}

func (a *adminAuth) session(r *http.Request) (adminSession, bool) {
	if !a.enabled() {
		return adminSession{}, false
	}
	cookie, err := r.Cookie(adminCookieName)
	if err != nil || cookie.Value == "" {
		return adminSession{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	session, ok := a.sessions[cookie.Value]
	if !ok || time.Now().After(session.ExpiresAt) {
		delete(a.sessions, cookie.Value)
		return adminSession{}, false
	}
	return session, true
}

func (a *adminAuth) csrfValid(r *http.Request) bool {
	session, ok := a.session(r)
	return ok && r.Header.Get("X-CSRF-Token") != "" && subtleConstantCompare(session.CSRF, r.Header.Get("X-CSRF-Token"))
}

func (a *adminAuth) allowAttempt(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	item, ok := a.failures[ip]
	if !ok || time.Now().After(item.ResetAt) {
		return true
	}
	return item.Count < 5
}

func (a *adminAuth) failed(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	item := a.failures[ip]
	if time.Now().After(item.ResetAt) {
		item = loginFailure{ResetAt: time.Now().Add(5 * time.Minute)}
	}
	item.Count++
	a.failures[ip] = item
}

func (a *adminAuth) clearFailure(ip string) {
	a.mu.Lock()
	delete(a.failures, ip)
	a.mu.Unlock()
}

func randomToken() string {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(value)
}

var errRandomToken = &randomTokenError{}

type randomTokenError struct{}

func (*randomTokenError) Error() string { return "random token generation failed" }

func subtleConstantCompare(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}
