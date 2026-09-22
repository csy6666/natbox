package auth

import "testing"

func TestHashAndVerifyPassword(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(encoded, "correct horse battery staple") {
		t.Fatal("password did not verify")
	}
	if VerifyPassword(encoded, "wrong password") {
		t.Fatal("wrong password verified")
	}
}

func TestHashRejectsShortPassword(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("expected short password error")
	}
}
