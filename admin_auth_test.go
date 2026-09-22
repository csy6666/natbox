package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"natbox/internal/auth"
)

func TestAdminAuthLoginSessionAndCSRF(t *testing.T) {
	hash, err := auth.HashPassword("a sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newAdminAuth(hash)
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"a sufficiently long password"}`))
	login.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	manager.login(response, login, func(string, string) {})
	if response.Code != http.StatusOK {
		t.Fatalf("login status = %d, body=%s", response.Code, response.Body.String())
	}
	cookie := response.Result().Cookies()[0]
	me := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	me.AddCookie(cookie)
	if _, ok := manager.session(me); !ok {
		t.Fatal("session was not created")
	}
	stateChanging := httptest.NewRequest(http.MethodPost, "/api/containers", nil)
	stateChanging.AddCookie(cookie)
	if manager.csrfValid(stateChanging) {
		t.Fatal("missing CSRF token accepted")
	}
}
