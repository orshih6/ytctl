package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// fakeGoogle is a token endpoint that accepts one authorization code and
// insists on a PKCE verifier.
func fakeGoogle(t *testing.T) (*httptest.Server, *oauth2.Config) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Form.Get("grant_type") == "refresh_token":
			json.NewEncoder(w).Encode(map[string]any{"access_token": "refreshed", "token_type": "Bearer", "expires_in": 3600})
		case r.Form.Get("code") != "good-code", r.Form.Get("code_verifier") == "", !strings.HasPrefix(r.Form.Get("redirect_uri"), "http://127.0.0.1:"):
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
		default:
			json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "refresh_token": "refresh", "token_type": "Bearer", "expires_in": 3600})
		}
	}))
	t.Cleanup(srv.Close)

	cfg := NewConfig("id", "secret")
	cfg.Endpoint = oauth2.Endpoint{AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}
	return srv, cfg
}

// browser plays the user's browser: it follows the consent URL's redirect
// back to ytctl with the given code, optionally tampering with the state.
func browser(t *testing.T, code string, tamper bool) func(string) {
	return func(authURL string) {
		u, err := url.Parse(authURL)
		if err != nil {
			t.Error(err)
			return
		}
		q := u.Query()
		if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
			t.Errorf("consent URL has no S256 PKCE challenge: %s", authURL)
		}
		if q.Get("access_type") != "offline" {
			t.Errorf("consent URL does not ask for offline access: %s", authURL)
		}
		state := q.Get("state")
		if tamper {
			state = "forged"
		}
		go func() {
			resp, err := http.Get(q.Get("redirect_uri") + "?" + url.Values{"code": {code}, "state": {state}}.Encode())
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
}

func TestLogin(t *testing.T) {
	_, cfg := fakeGoogle(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tok, err := Login(ctx, cfg, browser(t, "good-code", false))
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "access" || tok.RefreshToken != "refresh" {
		t.Errorf("token = %+v", tok)
	}
	if cfg.RedirectURL != "" {
		t.Errorf("Login modified the caller's config: RedirectURL = %q", cfg.RedirectURL)
	}
}

func TestLoginRejectsForgedState(t *testing.T) {
	_, cfg := fakeGoogle(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := Login(ctx, cfg, browser(t, "good-code", true))
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Errorf("err = %v, want a state mismatch", err)
	}
}

func TestLoginReportsFailedExchange(t *testing.T) {
	_, cfg := fakeGoogle(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := Login(ctx, cfg, browser(t, "bad-code", false)); err == nil {
		t.Error("Login succeeded with a code the token endpoint rejects")
	}
}

func TestLoginStopsWhenCancelled(t *testing.T) {
	_, cfg := fakeGoogle(t)
	ctx, cancel := context.WithCancel(context.Background())

	_, err := Login(ctx, cfg, func(string) { cancel() })
	if err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestTokenStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "token.json")

	if _, err := LoadToken(path); err != ErrNotLoggedIn {
		t.Errorf("LoadToken on a missing file = %v, want ErrNotLoggedIn", err)
	}

	want := &oauth2.Token{AccessToken: "a", RefreshToken: "r", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour).Round(time.Second)}
	if err := SaveToken(path, want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("token file mode = %o, want 600", perm)
	}

	got, err := LoadToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "a" || got.RefreshToken != "r" || !got.Expiry.Equal(want.Expiry) {
		t.Errorf("round trip = %+v", got)
	}

	if err := DeleteToken(path); err != nil {
		t.Fatal(err)
	}
	if err := DeleteToken(path); err != nil {
		t.Errorf("deleting an absent token = %v, want nil", err)
	}
}

func TestTokenSourceSavesRefreshedToken(t *testing.T) {
	_, cfg := fakeGoogle(t)
	path := filepath.Join(t.TempDir(), "token.json")
	expired := &oauth2.Token{AccessToken: "old", RefreshToken: "refresh", Expiry: time.Now().Add(-time.Hour)}
	if err := SaveToken(path, expired); err != nil {
		t.Fatal(err)
	}

	src, err := TokenSource(context.Background(), cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := src.Token()
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "refreshed" {
		t.Errorf("AccessToken = %q", tok.AccessToken)
	}

	saved, err := LoadToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.AccessToken != "refreshed" || saved.RefreshToken != "refresh" {
		t.Errorf("saved token = %+v, want the refreshed access token and the original refresh token", saved)
	}
}

func TestRevoke(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		got = r.Form.Get("token")
	}))
	defer srv.Close()
	defer func(old string) { revokeURL = old }(revokeURL)
	revokeURL = srv.URL

	if err := Revoke(context.Background(), &oauth2.Token{AccessToken: "a", RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	if got != "r" {
		t.Errorf("revoked %q, want the refresh token", got)
	}
}
