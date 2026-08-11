// Package config handles cf's profile-based configuration and credential
// resolution. Precedence for every value: flag > environment > profile.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Profile holds per-profile settings stored in config.toml.
type Profile struct {
	APIToken  string `toml:"api_token,omitempty"`
	AccountID string `toml:"account_id,omitempty"`
	ZoneID    string `toml:"zone_id,omitempty"`
}

// File is the on-disk configuration file.
type File struct {
	DefaultProfile string             `toml:"default_profile,omitempty"`
	Profiles       map[string]Profile `toml:"profiles"`
}

// Dir returns the configuration directory, honoring CF_CONFIG_DIR.
func Dir() string {
	if d := os.Getenv("CF_CONFIG_DIR"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "cf")
}

// Path returns the config file path.
func Path() string { return filepath.Join(Dir(), "config.toml") }

// Load reads the default config file; a missing file yields an empty config.
func Load() (File, error) { return LoadFrom(Path()) }

// LoadFrom reads a config file from an explicit path.
func LoadFrom(path string) (File, error) {
	f := File{Profiles: map[string]Profile{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return f, nil
		}
		return f, err
	}
	if err := toml.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.Profiles == nil {
		f.Profiles = map[string]Profile{}
	}
	return f, nil
}

// Save writes the config to the default path with 0600 permissions.
func Save(f File) error { return SaveTo(Path(), f) }

// SaveTo writes the config to an explicit path with 0600 permissions.
func SaveTo(path string, f File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(f); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}

// Overrides carries values passed as command-line flags.
type Overrides struct {
	Profile   string
	Token     string
	AccountID string
	ZoneID    string
}

// Resolved is the outcome of credential resolution, with the source of each
// value recorded for `cf auth status`.
type Resolved struct {
	Profile         string
	Token           string
	TokenSource     string
	AccountID       string
	AccountIDSource string
	ZoneID          string
	ZoneIDSource    string
}

func firstEnv(names ...string) (string, string) {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v, "env:" + n
		}
	}
	return "", ""
}

// Resolve applies precedence (flag > env > profile) to produce credentials.
func Resolve(f File, o Overrides) Resolved {
	profile := o.Profile
	if profile == "" {
		profile = os.Getenv("CF_PROFILE")
	}
	if profile == "" {
		profile = f.DefaultProfile
	}
	if profile == "" {
		profile = "default"
	}
	p := f.Profiles[profile]
	r := Resolved{Profile: profile}

	if o.Token != "" {
		r.Token, r.TokenSource = o.Token, "flag"
	} else if v, src := firstEnv("CLOUDFLARE_API_TOKEN", "CF_API_TOKEN"); v != "" {
		r.Token, r.TokenSource = v, src
	} else if p.APIToken != "" {
		r.Token, r.TokenSource = p.APIToken, "profile:"+profile
	} else {
		r.TokenSource = "none"
	}

	if o.AccountID != "" {
		r.AccountID, r.AccountIDSource = o.AccountID, "flag"
	} else if v, src := firstEnv("CLOUDFLARE_ACCOUNT_ID", "CF_ACCOUNT_ID"); v != "" {
		r.AccountID, r.AccountIDSource = v, src
	} else if p.AccountID != "" {
		r.AccountID, r.AccountIDSource = p.AccountID, "profile:"+profile
	} else {
		r.AccountIDSource = "none"
	}

	if o.ZoneID != "" {
		r.ZoneID, r.ZoneIDSource = o.ZoneID, "flag"
	} else if v, src := firstEnv("CLOUDFLARE_ZONE_ID", "CF_ZONE_ID"); v != "" {
		r.ZoneID, r.ZoneIDSource = v, src
	} else if p.ZoneID != "" {
		r.ZoneID, r.ZoneIDSource = p.ZoneID, "profile:"+profile
	} else {
		r.ZoneIDSource = "none"
	}

	return r
}
