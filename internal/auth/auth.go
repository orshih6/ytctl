// Package auth signs ytctl in to a Google account and keeps the resulting
// OAuth token on disk.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Scopes are what ytctl asks for: youtube.force-ssl covers reading and
// managing the account's videos, playlists and comments; youtube.upload
// covers uploading.
var Scopes = []string{
	"https://www.googleapis.com/auth/youtube.force-ssl",
	"https://www.googleapis.com/auth/youtube.upload",
}

// revokeURL is Google's token revocation endpoint. A variable so tests can
// point it at a local server.
var revokeURL = "https://oauth2.googleapis.com/revoke"

// NewConfig builds the OAuth configuration for a Desktop-app client. The
// redirect URL is filled in by Login once it knows which port it got.
func NewConfig(clientID, clientSecret string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       Scopes,
	}
}

// Login runs the OAuth authorization-code flow with a loopback redirect
// and PKCE: it listens on a random local port, hands the consent URL to
// show, and waits for Google to redirect the browser back with a code.
//
// show is called once with the URL the user must visit; it is expected to
// open a browser, print the URL, or both.
func Login(ctx context.Context, cfg *oauth2.Config, show func(authURL string)) (*oauth2.Token, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listening for the OAuth redirect: %w", err)
	}
	defer ln.Close()

	conf := *cfg
	conf.RedirectURL = fmt.Sprintf("http://%s/callback", ln.Addr())

	state, err := randomString()
	if err != nil {
		return nil, err
	}
	verifier := oauth2.GenerateVerifier()

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var res result
		switch {
		case q.Get("state") != state:
			res.err = errors.New("OAuth redirect carried the wrong state; start the login again")
		case q.Get("error") != "":
			res.err = fmt.Errorf("authorization was not granted: %s", q.Get("error"))
		case q.Get("code") == "":
			res.err = errors.New("OAuth redirect carried no authorization code")
		default:
			res.code = q.Get("code")
		}

		if res.err != nil {
			http.Error(w, "ytctl: "+res.err.Error(), http.StatusBadRequest)
		} else {
			fmt.Fprintln(w, "ytctl is signed in. You can close this tab.")
		}
		select {
		case done <- res:
		default:
		}
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()

	// access_type=offline with prompt=consent is what makes Google return
	// a refresh token every time, not only on the account's first consent.
	show(conf.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
		oauth2.S256ChallengeOption(verifier),
	))

	var res result
	select {
	case res = <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if res.err != nil {
		return nil, res.err
	}

	tok, err := conf.Exchange(ctx, res.code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("exchanging the authorization code: %w", err)
	}
	return tok, nil
}

// Revoke asks Google to invalidate the token, which signs ytctl out on
// Google's side as well as locally.
func Revoke(ctx context.Context, tok *oauth2.Token) error {
	value := tok.RefreshToken
	if value == "" {
		value = tok.AccessToken
	}
	if value == "" {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, revokeURL, strings.NewReader(url.Values{"token": {value}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("revoking token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("revoking token: Google answered %s", resp.Status)
	}
	return nil
}

func randomString() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating OAuth state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
