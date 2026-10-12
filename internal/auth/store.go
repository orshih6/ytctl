package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/oauth2"
)

// ErrNotLoggedIn is returned when there is no stored token.
var ErrNotLoggedIn = errors.New("not signed in: run `ytctl auth login` first")

// LoadToken reads the token stored at path.
func LoadToken(path string) (*oauth2.Token, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotLoggedIn
		}
		return nil, fmt.Errorf("reading token: %w", err)
	}
	var tok oauth2.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, fmt.Errorf("token file %s is corrupt (%w): run `ytctl auth login` again", path, err)
	}
	return &tok, nil
}

// SaveToken writes the token to path, readable by the owner only. It is
// written to a temporary file and renamed so that an interrupted save
// cannot leave a truncated token behind.
func SaveToken(path string, tok *oauth2.Token) error {
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating token directory: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".token-*")
	if err != nil {
		return fmt.Errorf("saving token: %w", err)
	}
	defer os.Remove(tmp.Name())

	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return fmt.Errorf("saving token: %w", err)
	}
	return nil
}

// DeleteToken removes the stored token. A token that is already gone is
// not an error.
func DeleteToken(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("deleting token: %w", err)
	}
	return nil
}

// TokenSource returns a source that refreshes the stored token as needed
// and writes each refreshed token back to path.
func TokenSource(ctx context.Context, cfg *oauth2.Config, path string) (oauth2.TokenSource, error) {
	tok, err := LoadToken(path)
	if err != nil {
		return nil, err
	}
	return &savingSource{base: cfg.TokenSource(ctx, tok), path: path, last: tok.AccessToken}, nil
}

type savingSource struct {
	base oauth2.TokenSource
	path string

	mu   sync.Mutex
	last string
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	tok, err := s.base.Token()
	if err != nil {
		var rerr *oauth2.RetrieveError
		if errors.As(err, &rerr) && rerr.ErrorCode == "invalid_grant" {
			return nil, fmt.Errorf("the stored sign-in is no longer valid (%s): run `ytctl auth login` again", rerr.ErrorCode)
		}
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if tok.AccessToken != s.last {
		// Losing a refreshed access token only costs another refresh next
		// run, so a failed save is not worth failing the command over.
		if SaveToken(s.path, tok) == nil {
			s.last = tok.AccessToken
		}
	}
	return tok, nil
}
