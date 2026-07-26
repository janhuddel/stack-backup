// Package config lädt und validiert die YAML-Konfiguration des Backup-Tools.
package config

import (
	"fmt"
	"os"
	"sort"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Retention beschreibt die Aufbewahrungsregeln eines Targets und wird 1:1 auf
// die --keep-*-Flags von "restic forget" gemappt.
type Retention struct {
	KeepLast    int `yaml:"keep_last"`
	KeepHourly  int `yaml:"keep_hourly"`
	KeepDaily   int `yaml:"keep_daily"`
	KeepWeekly  int `yaml:"keep_weekly"`
	KeepMonthly int `yaml:"keep_monthly"`
	KeepYearly  int `yaml:"keep_yearly"`
}

// IsZero meldet, ob keine einzige Aufbewahrungsregel gesetzt ist.
func (r Retention) IsZero() bool {
	return r == Retention{}
}

// Flags liefert die restic-forget-Flags für diese Policy.
func (r Retention) Flags() []string {
	var flags []string
	add := func(flag string, v int) {
		if v > 0 {
			flags = append(flags, flag, strconv.Itoa(v))
		}
	}
	add("--keep-last", r.KeepLast)
	add("--keep-hourly", r.KeepHourly)
	add("--keep-daily", r.KeepDaily)
	add("--keep-weekly", r.KeepWeekly)
	add("--keep-monthly", r.KeepMonthly)
	add("--keep-yearly", r.KeepYearly)
	return flags
}

// Target ist ein restic-Repository mit eigener Retention und eigenem Env.
type Target struct {
	Repository   string            `yaml:"repository"`
	Password     string            `yaml:"password"`
	PasswordFile string            `yaml:"password_file"`
	Env          map[string]string `yaml:"env"`
	Retention    Retention         `yaml:"retention"`
}

// ResticEnv baut das Environment für restic-Aufrufe gegen dieses Target.
// Werte aus Env werden per ${VAR} gegen das Prozess-Env expandiert.
func (t Target) ResticEnv() []string {
	env := []string{"RESTIC_REPOSITORY=" + t.Repository}
	switch {
	case t.PasswordFile != "":
		env = append(env, "RESTIC_PASSWORD_FILE="+t.PasswordFile)
	case t.Password != "":
		env = append(env, "RESTIC_PASSWORD="+t.Password)
	}
	keys := make([]string, 0, len(t.Env))
	for k := range t.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+os.ExpandEnv(t.Env[k]))
	}
	return env
}

// Config ist die Gesamtkonfiguration aus der YAML-Datei.
type Config struct {
	Schedule    string `yaml:"schedule"`
	LabelPrefix string `yaml:"label_prefix"`
	StopTimeout int    `yaml:"stop_timeout_seconds"`
	// RequireHealthy überspringt Container, deren Healthcheck beim Start des
	// Backup-Versuchs nicht "healthy" meldet (Default: true). Container ohne
	// definierten Healthcheck sind davon nicht betroffen.
	RequireHealthy *bool             `yaml:"require_healthy"`
	Targets        map[string]Target `yaml:"targets"`
}

// TargetNames liefert alle Target-Namen in stabiler Reihenfolge.
func (c *Config) TargetNames() []string {
	names := make([]string, 0, len(c.Targets))
	for name := range c.Targets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Load liest und validiert die Konfiguration aus path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config lesen: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config parsen: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.LabelPrefix == "" {
		c.LabelPrefix = "stack-backup"
	}
	if c.StopTimeout <= 0 {
		c.StopTimeout = 30
	}
	if c.RequireHealthy == nil {
		t := true
		c.RequireHealthy = &t
	}
}

func (c *Config) validate() error {
	if c.Schedule == "" {
		return fmt.Errorf("config: schedule fehlt")
	}
	if len(c.Targets) == 0 {
		return fmt.Errorf("config: mindestens ein Target erforderlich")
	}
	for name, t := range c.Targets {
		if t.Repository == "" {
			return fmt.Errorf("config: target %q ohne repository", name)
		}
		if t.Password == "" && t.PasswordFile == "" && t.Env["RESTIC_PASSWORD"] == "" && t.Env["RESTIC_PASSWORD_FILE"] == "" {
			return fmt.Errorf("config: target %q ohne password/password_file", name)
		}
	}
	return nil
}
