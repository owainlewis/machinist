package config

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/owainlewis/machinist/internal/harness"
)

const (
	defaultDockerMemory    = "4g"
	defaultDockerCPUs      = 2.0
	defaultDockerPIDsLimit = 512
)

var (
	dockerImagePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]*$`)
	dockerNamePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	dockerMemoryPattern = regexp.MustCompile(`^[1-9][0-9]*([bkmgBKMG][bB]?)?$`)
)

// Docker is operator-owned configuration for running each job in an ephemeral,
// hardened Docker container.
type Docker struct {
	Image     string   `yaml:"image"`
	Runtime   string   `yaml:"runtime"`
	Network   string   `yaml:"network"`
	ProxyURL  string   `yaml:"proxy_url"`
	Memory    string   `yaml:"memory"`
	CPUs      float64  `yaml:"cpus"`
	PIDsLimit int      `yaml:"pids_limit"`
	Env       []string `yaml:"env"`
	Harnesses []string `yaml:"harnesses"`
}

// WithDefaults returns a copy of d with hardened resource defaults applied.
func (d Docker) WithDefaults() Docker {
	d.applyDefaults()
	return d
}

// applyDefaults fills in hardened resource defaults and trims fields.
func (d *Docker) applyDefaults() {
	d.Image = strings.TrimSpace(d.Image)
	d.Runtime = strings.TrimSpace(d.Runtime)
	d.Network = strings.TrimSpace(d.Network)
	d.ProxyURL = strings.TrimSpace(d.ProxyURL)
	d.Memory = strings.TrimSpace(d.Memory)
	if d.Memory == "" {
		d.Memory = defaultDockerMemory
	}
	if d.CPUs == 0 {
		d.CPUs = defaultDockerCPUs
	}
	if d.PIDsLimit == 0 {
		d.PIDsLimit = defaultDockerPIDsLimit
	}
	if len(d.Harnesses) > 0 {
		seen := make(map[string]struct{}, len(d.Harnesses))
		unique := make([]string, 0, len(d.Harnesses))
		for _, name := range d.Harnesses {
			name = strings.TrimSpace(name)
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			unique = append(unique, name)
		}
		slices.Sort(unique)
		d.Harnesses = unique
	}
}

// Validate checks that Docker runner configuration is well-formed and safe to
// pass to docker run.
func (d Docker) Validate() error {
	if d.Image == "" {
		return errors.New("docker: image is required")
	}
	if !dockerImagePattern.MatchString(d.Image) {
		return fmt.Errorf("docker: invalid image %q", d.Image)
	}
	if d.Runtime != "" && !dockerNamePattern.MatchString(d.Runtime) {
		return fmt.Errorf("docker: invalid runtime %q", d.Runtime)
	}
	if d.Network != "" && !dockerNamePattern.MatchString(d.Network) {
		return fmt.Errorf("docker: invalid network %q", d.Network)
	}
	if d.ProxyURL != "" {
		parsed, err := url.Parse(d.ProxyURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("docker: proxy_url %q must be an http or https URL", d.ProxyURL)
		}
	}
	if d.Memory == "" || !dockerMemoryPattern.MatchString(d.Memory) {
		return fmt.Errorf("docker: invalid memory limit %q (expected e.g. 4g or 512m)", d.Memory)
	}
	if math.IsNaN(d.CPUs) || math.IsInf(d.CPUs, 0) || d.CPUs <= 0 {
		return errors.New("docker: cpus must be positive")
	}
	if d.PIDsLimit <= 0 {
		return errors.New("docker: pids_limit must be positive")
	}
	for _, name := range d.Env {
		if !environmentName.MatchString(name) {
			return fmt.Errorf("docker.env: invalid environment variable name %q", name)
		}
	}
	for _, name := range d.Harnesses {
		if _, ok := harness.Find(name); !ok {
			return fmt.Errorf("docker.harnesses: unknown harness %q (supported: %s)", name, strings.Join(harness.Names(), ", "))
		}
	}
	return nil
}
