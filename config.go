package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type config struct {
	StartURL             string `yaml:"sso_start_url"`
	Region               string `yaml:"sso_region"`
	ClientID             string `yaml:"client_id,omitempty"`
	ClientSecret         string `yaml:"client_secret,omitempty"`
	ClientExpiresAt      string `yaml:"client_expires_at,omitempty"`
	AccessToken          string `yaml:"access_token,omitempty"`
	RefreshToken         string `yaml:"refresh_token,omitempty"`
	AccessTokenExpiresAt string `yaml:"access_token_expires_at,omitempty"`
}

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".awsx", "config.yaml"), nil
}

func validStartURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && strings.EqualFold(u.Scheme, "https") && u.Hostname() != "" && u.User == nil
}

func loadConfig(path string) (config, error) {
	var c config
	data, err := os.ReadFile(path)
	if err != nil {
		return c, fmt.Errorf("read configuration: %w; run awsx init", err)
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		// YAML errors can include field values; do not expose cached secrets.
		return c, errors.New("invalid configuration YAML; run awsx init")
	}
	if !validStartURL(c.StartURL) || !isRegion(c.Region) {
		return c, errors.New("missing or invalid SSO start URL or commercial region; run awsx init")
	}
	return c, nil
}

func (c *config) clearAuth() {
	c.ClientID, c.ClientSecret, c.ClientExpiresAt = "", "", ""
	c.clearTokens()
}

func (c *config) clearTokens() { c.AccessToken, c.RefreshToken, c.AccessTokenExpiresAt = "", "", "" }

func expiresAfter(value string, deadline time.Time) bool {
	t, err := time.Parse(time.RFC3339, value)
	return err == nil && t.After(deadline)
}

func (c config) validClient() bool {
	return c.ClientID != "" && c.ClientSecret != "" && expiresAfter(c.ClientExpiresAt, time.Now())
}

func saveConfig(path string, c config) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return errors.New("encode configuration failed")
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("secure configuration directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return fmt.Errorf("create temporary configuration: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write configuration: %w", err)
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("replace configuration: %w", err)
	}
	return nil
}
