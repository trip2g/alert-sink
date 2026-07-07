package alertsink

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestSignHAT(t *testing.T) {
	secret := "test-secret"
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)

	signed, err := SignHAT(secret, "alert-sink@local", now)
	if err != nil {
		t.Fatalf("SignHAT: %v", err)
	}

	var claims hatClaims
	parsed, err := jwt.ParseWithClaims(signed, &claims, func(tok *jwt.Token) (any, error) {
		if tok.Method != jwt.SigningMethodHS256 {
			t.Fatalf("unexpected signing method %v", tok.Method)
		}
		return []byte(secret), nil
	}, jwt.WithTimeFunc(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("parse signed HAT: %v", err)
	}
	if !parsed.Valid {
		t.Fatal("token not valid")
	}
	if claims.Email != "alert-sink@local" {
		t.Errorf("email claim = %q", claims.Email)
	}
	if !claims.AdminEnter {
		t.Error("admin-enter claim not set")
	}
	exp := claims.ExpiresAt.Time
	if want := now.Add(5 * time.Minute); !exp.Equal(want) {
		t.Errorf("expiry = %v, want %v (5 minutes)", exp, want)
	}
}

func TestSignHATRejectsWrongSecret(t *testing.T) {
	signed, err := SignHAT("right-secret", "a@b", time.Now())
	if err != nil {
		t.Fatalf("SignHAT: %v", err)
	}
	_, err = jwt.ParseWithClaims(signed, &hatClaims{}, func(_ *jwt.Token) (any, error) {
		return []byte("wrong-secret"), nil
	})
	if err == nil {
		t.Fatal("token verified with the wrong secret")
	}
}
