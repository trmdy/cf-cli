package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePrecedence(t *testing.T) {
	f := File{
		DefaultProfile: "work",
		Profiles: map[string]Profile{
			"work": {APIToken: "profile-token", AccountID: "profile-acct"},
		},
	}

	t.Run("profile fallback", func(t *testing.T) {
		r := Resolve(f, Overrides{})
		if r.Profile != "work" || r.Token != "profile-token" || r.TokenSource != "profile:work" {
			t.Errorf("unexpected: %+v", r)
		}
	})

	t.Run("env beats profile", func(t *testing.T) {
		t.Setenv("CLOUDFLARE_API_TOKEN", "env-token")
		r := Resolve(f, Overrides{})
		if r.Token != "env-token" || r.TokenSource != "env:CLOUDFLARE_API_TOKEN" {
			t.Errorf("unexpected: %+v", r)
		}
	})

	t.Run("flag beats env", func(t *testing.T) {
		t.Setenv("CLOUDFLARE_API_TOKEN", "env-token")
		r := Resolve(f, Overrides{Token: "flag-token"})
		if r.Token != "flag-token" || r.TokenSource != "flag" {
			t.Errorf("unexpected: %+v", r)
		}
	})

	t.Run("explicit profile", func(t *testing.T) {
		r := Resolve(f, Overrides{Profile: "missing"})
		if r.Profile != "missing" || r.TokenSource != "none" {
			t.Errorf("unexpected: %+v", r)
		}
	})
}

func TestLoadSaveRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	f := File{
		DefaultProfile: "default",
		Profiles: map[string]Profile{
			"default": {APIToken: "tok", AccountID: "acct", ZoneID: "zone"},
		},
	}
	if err := SaveTo(path, f); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("config file mode = %v, want 0600", info.Mode().Perm())
	}
	got, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Profiles["default"].APIToken != "tok" || got.DefaultProfile != "default" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

func TestLoadMissingFile(t *testing.T) {
	got, err := LoadFrom(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Profiles == nil {
		t.Error("missing file should yield empty config with non-nil Profiles")
	}
}
