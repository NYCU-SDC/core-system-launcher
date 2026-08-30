// Package config manages the launcher's working directory and user settings.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is everything one deployment needs, persisted as JSON in the working
// directory.
type Config struct {
	// AppPort is the single port the whole system is exposed on. The frontend
	// gateway listens on it and proxies /api to the backend. The OAuth redirect
	// URI is bound to this port, so changing it means updating Google Console too.
	AppPort int `json:"appPort"`

	// PostgresPort is where postgres is published on the host, for debugging only.
	PostgresPort int `json:"postgresPort"`

	// AdminEmail becomes the admin of the initial organization.
	AdminEmail string `json:"adminEmail"`

	// TrialMode skips Google OAuth entirely and relies on the backend's internal
	// login endpoint instead.
	TrialMode bool `json:"trialMode"`

	GoogleClientID     string `json:"googleClientId,omitempty"`
	GoogleClientSecret string `json:"googleClientSecret,omitempty"`

	// Secret signs JWTs. Left empty, the backend regenerates one on every boot
	// and logs everyone out.
	Secret string `json:"secret"`
}

// OrgSlug is the slug of the initial organization.
//
// This is deliberately not configurable. The backend hard-codes "SDC" as the
// default org slug in internal/user/find_or_create.go (case-sensitively), and
// the frontend's DefaultOrgRedirect falls back to the same string. Changing it
// silently breaks default role assignment and some redirects.
const OrgSlug = "SDC"

// BaseURL is where the system is reachable.
func (c *Config) BaseURL() string {
	return fmt.Sprintf("http://localhost:%d", c.AppPort)
}

// GoogleRedirectURI is the line that has to be registered in Google Console.
func (c *Config) GoogleRedirectURI() string {
	return c.BaseURL() + "/api/auth/login/oauth/google/callback"
}

// Dir is the launcher's working directory. Fetched sources, generated compose
// files and settings all live here, so the user's own project directories are
// never touched.
func Dir() (string, error) {
	if v := os.Getenv("CORE_SYSTEM_LAUNCHER_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("找不到 home 目錄：%w", err)
	}
	return filepath.Join(home, ".core-system-launcher"), nil
}

// SrcDir holds the checked-out upstream sources.
func SrcDir() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "src"), nil
}

// DeployDir holds the generated compose.yaml, setup.yaml and friends.
func DeployDir() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "deploy"), nil
}

func path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

// Load reads the existing settings. Returns (nil, nil) when none exist.
func Load() (*Config, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("讀取設定失敗：%w", err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("設定檔格式錯誤（%s）：%w", p, err)
	}
	return &c, nil
}

// Save writes the settings back. It holds the OAuth secret, hence mode 0600.
func (c *Config) Save() error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o600)
}
