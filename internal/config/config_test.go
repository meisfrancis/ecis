package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	t.Setenv("ECIS_CONFIG_DIR", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() with no config file returned %v, want no error", err)
	}
	if cfg.RefreshRate != DefaultRefreshRate {
		t.Errorf("RefreshRate = %d, want the default %d", cfg.RefreshRate, DefaultRefreshRate)
	}
	if cfg.Cost.ClusterTagKey == "" || cfg.Metrics.PeriodSeconds == 0 {
		t.Errorf("nested defaults were not applied: %+v", cfg)
	}
}

func TestLoadReadsFileAndValidates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ECIS_CONFIG_DIR", dir)

	body := []byte("refreshRate: 9999\nreadOnly: true\ncluster: prod\nmetrics:\n  periodSeconds: 90\n")
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if !cfg.ReadOnly {
		t.Error("readOnly was not read from the file")
	}
	if cfg.Cluster != "prod" {
		t.Errorf("Cluster = %q, want prod", cfg.Cluster)
	}
	// Out-of-range values are clamped back to something workable.
	if cfg.RefreshRate != DefaultRefreshRate {
		t.Errorf("RefreshRate = %d, want it clamped to %d", cfg.RefreshRate, DefaultRefreshRate)
	}
	// CloudWatch only accepts periods that are a multiple of 60 above a minute.
	if cfg.Metrics.PeriodSeconds%60 != 0 {
		t.Errorf("PeriodSeconds = %d, want a multiple of 60", cfg.Metrics.PeriodSeconds)
	}
}

func TestLoadReportsMalformedFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ECIS_CONFIG_DIR", dir)

	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("refreshRate: [not a number\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err == nil {
		t.Fatal("Load() accepted a malformed config file")
	}
	// A broken file must still yield a usable configuration.
	if cfg == nil || cfg.RefreshRate != DefaultRefreshRate {
		t.Errorf("Load() returned %+v, want usable defaults alongside the error", cfg)
	}
}

func TestSaveRoundTrips(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ECIS_CONFIG_DIR", dir)

	cfg := New()
	cfg.Cluster = "staging"
	cfg.Cost.LookbackDays = 14
	cfg.Cost.Rates = map[string]Rate{"us-east-1": {VCPUHour: 0.05, GBHour: 0.005}}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if loaded.Cluster != "staging" || loaded.Cost.LookbackDays != 14 {
		t.Errorf("round trip lost values: %+v", loaded)
	}
	if r, ok := loaded.Cost.Rates["us-east-1"]; !ok || r.VCPUHour != 0.05 {
		t.Errorf("rate overrides did not survive the round trip: %+v", loaded.Cost.Rates)
	}
}

func TestIntervals(t *testing.T) {
	cfg := New()
	cfg.RefreshRate = 7
	cfg.Cost.RefreshMinutes = 20
	cfg.Metrics.WindowMinutes = 30
	cfg.Metrics.PeriodSeconds = 300

	if got := cfg.Interval(); got != 7*time.Second {
		t.Errorf("Interval() = %v, want 7s", got)
	}
	if got := cfg.CostInterval(); got != 20*time.Minute {
		t.Errorf("CostInterval() = %v, want 20m", got)
	}
	if got := cfg.MetricWindow(); got != 30*time.Minute {
		t.Errorf("MetricWindow() = %v, want 30m", got)
	}
	if got := cfg.MetricPeriod(); got != 5*time.Minute {
		t.Errorf("MetricPeriod() = %v, want 5m", got)
	}
}

func TestDirHonoursOverride(t *testing.T) {
	t.Setenv("ECIS_CONFIG_DIR", "/tmp/ecis-test")
	if got := Dir(); got != "/tmp/ecis-test" {
		t.Errorf("Dir() = %q, want the override", got)
	}
	if got := Path(); got != "/tmp/ecis-test/config.yml" {
		t.Errorf("Path() = %q", got)
	}
}
