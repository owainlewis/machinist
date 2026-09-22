package config

import (
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Storage struct {
	Artifacts ArtifactStorage `toml:"artifacts"`
}
type ArtifactStorage struct {
	Backend     string `toml:"backend"`
	Path        string `toml:"path"`
	Retention   string `toml:"retention"`
	MaxFileSize string `toml:"max_file_size"`
	MaxRunSize  string `toml:"max_run_size"`
}
type ResolvedStorage struct {
	Path                      string
	Retention                 time.Duration
	MaxFileBytes, MaxRunBytes int64
}

func (c Config) ResolveStorage(defaultPath string) (ResolvedStorage, error) {
	a := c.Storage.Artifacts
	r := ResolvedStorage{Path: defaultPath, Retention: 30 * 24 * time.Hour, MaxFileBytes: 100 << 20, MaxRunBytes: 1 << 30}
	if a.Backend != "" && a.Backend != "filesystem" {
		return r, fmt.Errorf("unsupported artifact backend %q", a.Backend)
	}
	var err error
	if a.Path != "" {
		r.Path, err = resolveConfigPath(a.Path, filepath.Dir(c.path))
		if err != nil {
			return r, err
		}
	}
	if a.Retention != "" {
		value := a.Retention
		if strings.HasSuffix(value, "d") {
			days, e := strconv.ParseFloat(strings.TrimSuffix(value, "d"), 64)
			if e != nil || math.IsNaN(days) || days <= 0 || days > 36500 {
				return r, fmt.Errorf("invalid retention %q", value)
			}
			r.Retention = time.Duration(days * 24 * float64(time.Hour))
		} else {
			r.Retention, err = time.ParseDuration(value)
			if err != nil || r.Retention <= 0 {
				return r, fmt.Errorf("invalid retention %q", value)
			}
		}
	}
	if a.MaxFileSize != "" {
		r.MaxFileBytes, err = parseSize(a.MaxFileSize)
		if err != nil {
			return r, err
		}
	}
	if a.MaxRunSize != "" {
		r.MaxRunBytes, err = parseSize(a.MaxRunSize)
		if err != nil {
			return r, err
		}
	}
	if r.MaxFileBytes > r.MaxRunBytes {
		return r, fmt.Errorf("max_file_size exceeds max_run_size")
	}
	return r, nil
}
func parseSize(s string) (int64, error) {
	for _, unit := range []struct {
		name   string
		factor int64
	}{{"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(s, unit.name) {
			n, err := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(s, unit.name)), 10, 64)
			if err != nil || n <= 0 || n > (1<<40)/unit.factor {
				return 0, fmt.Errorf("invalid size %q", s)
			}
			return n * unit.factor, nil
		}
	}
	return 0, fmt.Errorf("size %q must use B, KB, MB, or GB", s)
}
