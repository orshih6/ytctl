// Package config resolves ytctl's configuration from flags, environment
// variables, and an optional YAML config file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds the fully resolved settings ytctl runs with.
type Config struct {
	ClientID     string
	ClientSecret string
	TokenPath    string

	configPath string
}

// fileConfig mirrors the YAML config file's shape.
type fileConfig struct {
	ClientID         string `yaml:"client_id"`
	ClientSecret     string `yaml:"client_secret"`
	ClientSecretFile string `yaml:"client_secret_file"`
	TokenFile        string `yaml:"token_file"`
}

// DefaultDir is where ytctl keeps its config file and OAuth token.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "ytctl")
}

// Resolve determines the effective configuration, applying precedence
// flag > environment > config file > default.
//
// The OAuth client comes from a client-secret file (the JSON downloaded
// from the Google Cloud console) or from an id/secret pair. Whichever
// source ranks highest wins as a whole: a client-secret file given by flag
// is never mixed with an id from the environment. Resolve does not require
// a client to be configured; call RequireClient before using it.
func Resolve(flagClientSecretFile, flagConfigPath string) (Config, error) {
	fc, err := loadFile(flagConfigPath)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		TokenPath:  firstNonEmpty(os.Getenv("YTCTL_TOKEN_FILE"), fc.TokenFile),
		configPath: flagConfigPath,
	}
	if cfg.TokenPath == "" {
		dir := DefaultDir()
		if flagConfigPath != "" {
			dir = filepath.Dir(flagConfigPath)
		}
		cfg.TokenPath = filepath.Join(dir, "token.json")
	}

	envID, envSecret := os.Getenv("YTCTL_CLIENT_ID"), os.Getenv("YTCTL_CLIENT_SECRET")
	switch {
	case flagClientSecretFile != "":
		cfg.ClientID, cfg.ClientSecret, err = readClientSecretFile(flagClientSecretFile)
	case envID != "":
		cfg.ClientID, cfg.ClientSecret = envID, envSecret
	case os.Getenv("YTCTL_CLIENT_SECRET_FILE") != "":
		cfg.ClientID, cfg.ClientSecret, err = readClientSecretFile(os.Getenv("YTCTL_CLIENT_SECRET_FILE"))
	case fc.ClientID != "":
		cfg.ClientID, cfg.ClientSecret = fc.ClientID, fc.ClientSecret
	case fc.ClientSecretFile != "":
		cfg.ClientID, cfg.ClientSecret, err = readClientSecretFile(fc.ClientSecretFile)
	}
	if err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// RequireClient returns an error describing how to configure an OAuth
// client if none was resolved.
func (c Config) RequireClient() error {
	if c.ClientID != "" {
		return nil
	}
	return fmt.Errorf("no OAuth client configured: pass --client-secret-file, or set YTCTL_CLIENT_ID and YTCTL_CLIENT_SECRET, or put client_id/client_secret in %s (see the README for how to create one)", c.configPath)
}

// readClientSecretFile extracts the client id and secret from the JSON
// Google's console offers for download. Desktop clients are filed under
// "installed"; "web" is accepted so that a mistaken download fails later,
// at the redirect, with Google's own explanation rather than here.
func readClientSecretFile(path string) (id, secret string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("reading client secret file: %w", err)
	}

	type client struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	var doc struct {
		Installed *client `json:"installed"`
		Web       *client `json:"web"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", "", fmt.Errorf("parsing client secret file %s: %w", path, err)
	}

	c := doc.Installed
	if c == nil {
		c = doc.Web
	}
	if c == nil || c.ClientID == "" {
		return "", "", fmt.Errorf("client secret file %s has no \"installed\" client: download the JSON for a \"Desktop app\" OAuth client", path)
	}
	return c.ClientID, c.ClientSecret, nil
}

// loadFile reads and parses the YAML config file at path. A missing file is
// not an error; it just yields a zero-value fileConfig.
func loadFile(path string) (fileConfig, error) {
	var fc fileConfig
	if path == "" {
		return fc, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fc, nil
		}
		return fc, fmt.Errorf("reading config file: %w", err)
	}
	if err := yaml.Unmarshal(data, &fc); err != nil {
		return fc, fmt.Errorf("parsing config file %s: %w", path, err)
	}
	return fc, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
