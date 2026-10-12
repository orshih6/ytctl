package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"YTCTL_CLIENT_ID", "YTCTL_CLIENT_SECRET", "YTCTL_CLIENT_SECRET_FILE", "YTCTL_TOKEN_FILE"} {
		t.Setenv(k, "")
	}
}

func TestResolvePrecedence(t *testing.T) {
	dir := t.TempDir()
	flagFile := write(t, dir, "flag.json", `{"installed":{"client_id":"flag-id","client_secret":"flag-secret"}}`)
	cfgFile := write(t, dir, "config.yaml", "client_id: file-id\nclient_secret: file-secret\n")

	t.Run("config file only", func(t *testing.T) {
		clearEnv(t)
		cfg, err := Resolve("", cfgFile)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ClientID != "file-id" || cfg.ClientSecret != "file-secret" {
			t.Errorf("got %q/%q", cfg.ClientID, cfg.ClientSecret)
		}
		if want := filepath.Join(dir, "token.json"); cfg.TokenPath != want {
			t.Errorf("TokenPath = %q, want %q", cfg.TokenPath, want)
		}
	})

	t.Run("env beats config file", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("YTCTL_CLIENT_ID", "env-id")
		t.Setenv("YTCTL_CLIENT_SECRET", "env-secret")
		cfg, err := Resolve("", cfgFile)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ClientID != "env-id" || cfg.ClientSecret != "env-secret" {
			t.Errorf("got %q/%q", cfg.ClientID, cfg.ClientSecret)
		}
	})

	t.Run("flag beats env, as a whole", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("YTCTL_CLIENT_ID", "env-id")
		t.Setenv("YTCTL_CLIENT_SECRET", "env-secret")
		cfg, err := Resolve(flagFile, cfgFile)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ClientID != "flag-id" || cfg.ClientSecret != "flag-secret" {
			t.Errorf("got %q/%q", cfg.ClientID, cfg.ClientSecret)
		}
	})

	t.Run("token file from env", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("YTCTL_TOKEN_FILE", "/tmp/elsewhere.json")
		cfg, err := Resolve("", cfgFile)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.TokenPath != "/tmp/elsewhere.json" {
			t.Errorf("TokenPath = %q", cfg.TokenPath)
		}
	})
}

func TestResolveMissingFileIsNotAnError(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "absent.yaml")
	cfg, err := Resolve("", path)
	if err != nil {
		t.Fatal(err)
	}
	err = cfg.RequireClient()
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("RequireClient() = %v, want an error naming %s", err, path)
	}
}

func TestClientSecretFileWithoutClient(t *testing.T) {
	clearEnv(t)
	bad := write(t, t.TempDir(), "sa.json", `{"type":"service_account"}`)
	if _, err := Resolve(bad, ""); err == nil || !strings.Contains(err.Error(), "Desktop app") {
		t.Errorf("err = %v, want a hint about Desktop app clients", err)
	}
}
