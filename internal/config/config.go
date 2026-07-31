// Package config holds user preferences for ecis. Preferences live in
// $XDG_CONFIG_HOME/ecis/config.yml (falling back to ~/.config/ecis/config.yml)
// and every field has a usable default, so a missing file is not an error.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Bounds on the refresh loop. Anything faster than MinRefreshRate hammers the
// ECS describe APIs hard enough to get throttled on a busy account.
const (
	MinRefreshRate     = 1
	MaxRefreshRate     = 600
	DefaultRefreshRate = 5
)

// Cost configures the Cost Explorer views.
//
// Cost Explorer can only break spend down by cluster or service when the
// corresponding cost allocation tags have been activated in Billing, so the tag
// keys are configurable and the estimator covers accounts that have not done so.
type Cost struct {
	// ClusterTagKey is the cost allocation tag carrying the cluster name.
	ClusterTagKey string `yaml:"clusterTagKey"`
	// ServiceTagKey is the cost allocation tag carrying the service name.
	ServiceTagKey string `yaml:"serviceTagKey"`
	// LookbackDays is how much history the cost views load.
	LookbackDays int `yaml:"lookbackDays"`
	// Estimate enables the local Fargate estimator alongside actual spend.
	Estimate bool `yaml:"estimate"`
	// IncludeCredits folds credits and refunds into the reported totals.
	IncludeCredits bool `yaml:"includeCredits"`
	// RefreshMinutes is the auto-reload interval for cost views. Cost Explorer
	// bills per request, so this is deliberately far slower than the ECS tables.
	RefreshMinutes int `yaml:"refreshMinutes"`
	// Rates overrides the built-in Fargate price table, keyed by region. Set
	// this when the built-in estimates drift from your actual rates.
	Rates map[string]Rate `yaml:"rates"`
}

// Rate is a per-region Fargate price override.
type Rate struct {
	VCPUHour float64 `yaml:"vcpuHour"`
	GBHour   float64 `yaml:"gbHour"`
}

// Metrics configures CloudWatch sampling for the monitoring views.
type Metrics struct {
	// PeriodSeconds is the CloudWatch aggregation period. 60 is the finest
	// granularity available without detailed monitoring surcharges.
	PeriodSeconds int `yaml:"periodSeconds"`
	// WindowMinutes is how far back the charts plot.
	WindowMinutes int `yaml:"windowMinutes"`
	// ContainerInsights queries the ECS/ContainerInsights namespace for
	// per-task utilization. Harmless (just empty) when it is not enabled.
	ContainerInsights bool `yaml:"containerInsights"`
}

// Config is the root of the preferences file.
type Config struct {
	// RefreshRate is the table auto-reload interval in seconds.
	RefreshRate int `yaml:"refreshRate"`
	// MaxLogLines caps the in-memory log buffer per container.
	MaxLogLines int `yaml:"maxLogLines"`
	// LogSinceSeconds is how much history a log view loads on open.
	LogSinceSeconds int `yaml:"logSinceSeconds"`
	// ReadOnly disables every mutating action (scale, restart, stop, exec).
	ReadOnly bool `yaml:"readOnly"`
	// NoIcons renders the UI without unicode decorations.
	NoIcons bool `yaml:"noIcons"`
	// Profile, Region and Cluster seed the initial context.
	Profile string `yaml:"profile"`
	Region  string `yaml:"region"`
	Cluster string `yaml:"cluster"`

	Cost    Cost    `yaml:"cost"`
	Metrics Metrics `yaml:"metrics"`

	path string
}

// New returns a Config populated with defaults.
func New() *Config {
	return &Config{
		RefreshRate:     DefaultRefreshRate,
		MaxLogLines:     5000,
		LogSinceSeconds: 300,
		Cost: Cost{
			ClusterTagKey:  "ecs:cluster-name",
			ServiceTagKey:  "ecs:service-name",
			LookbackDays:   30,
			Estimate:       true,
			IncludeCredits: false,
			RefreshMinutes: 15,
		},
		Metrics: Metrics{
			PeriodSeconds:     60,
			WindowMinutes:     60,
			ContainerInsights: true,
		},
	}
}

// Dir returns the directory holding the ecis configuration.
func Dir() string {
	if d := os.Getenv("ECIS_CONFIG_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "ecis")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".ecis"
	}
	return filepath.Join(home, ".config", "ecis")
}

// Path returns the location of the preferences file.
func Path() string { return filepath.Join(Dir(), "config.yml") }

// Load reads the preferences file, returning defaults when it does not exist.
// A malformed file is reported rather than silently ignored, so a typo does not
// quietly revert someone's settings.
func Load() (*Config, error) {
	cfg := New()
	cfg.path = Path()

	raw, err := os.ReadFile(cfg.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read config %s: %w", cfg.path, err)
	}
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return New(), fmt.Errorf("parse config %s: %w", cfg.path, err)
	}
	cfg.Validate()
	return cfg, nil
}

// Save writes the preferences back to disk.
func (c *Config) Save() error {
	if c.path == "" {
		c.path = Path()
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	raw, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, raw, 0o644)
}

// Validate clamps out-of-range values back to something workable.
func (c *Config) Validate() {
	if c.RefreshRate < MinRefreshRate || c.RefreshRate > MaxRefreshRate {
		c.RefreshRate = DefaultRefreshRate
	}
	if c.MaxLogLines <= 0 {
		c.MaxLogLines = 5000
	}
	if c.LogSinceSeconds <= 0 {
		c.LogSinceSeconds = 300
	}
	if c.Metrics.PeriodSeconds <= 0 {
		c.Metrics.PeriodSeconds = 60
	}
	// CloudWatch only accepts periods that are 1, 5, 10, 30 or a multiple of 60.
	if c.Metrics.PeriodSeconds > 60 && c.Metrics.PeriodSeconds%60 != 0 {
		c.Metrics.PeriodSeconds = ((c.Metrics.PeriodSeconds / 60) + 1) * 60
	}
	if c.Metrics.WindowMinutes <= 0 {
		c.Metrics.WindowMinutes = 60
	}
	if c.Cost.LookbackDays <= 0 {
		c.Cost.LookbackDays = 30
	}
	if c.Cost.ClusterTagKey == "" {
		c.Cost.ClusterTagKey = "ecs:cluster-name"
	}
	if c.Cost.ServiceTagKey == "" {
		c.Cost.ServiceTagKey = "ecs:service-name"
	}
	if c.Cost.RefreshMinutes <= 0 {
		c.Cost.RefreshMinutes = 15
	}
}

// CostInterval returns the cost view refresh interval.
func (c *Config) CostInterval() time.Duration {
	return time.Duration(c.Cost.RefreshMinutes) * time.Minute
}

// Interval returns the refresh rate as a duration.
func (c *Config) Interval() time.Duration {
	return time.Duration(c.RefreshRate) * time.Second
}

// MetricWindow returns the charting window as a duration.
func (c *Config) MetricWindow() time.Duration {
	return time.Duration(c.Metrics.WindowMinutes) * time.Minute
}

// MetricPeriod returns the CloudWatch period as a duration.
func (c *Config) MetricPeriod() time.Duration {
	return time.Duration(c.Metrics.PeriodSeconds) * time.Second
}
