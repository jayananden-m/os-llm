package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"
)

// Duration lets JSON hold durations as strings like "30s".
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// Discovery finds a model's replicas from a Kubernetes Service's EndpointSlices.
type Discovery struct {
	Namespace string `json:"namespace"`
	Service   string `json:"service"`
	Port      string `json:"port"` // port name on the Service; empty takes the first port
}

// Model is one served model and the replicas behind it. Set exactly one of
// Replicas (static addresses, for local dev) or Discovery (in-cluster).
type Model struct {
	Name                  string     `json:"name"`
	Replicas              []string   `json:"replicas,omitempty"`
	Discovery             *Discovery `json:"discovery,omitempty"`
	MaxInFlightPerReplica int        `json:"maxInFlightPerReplica"`
	MaxQueue              int        `json:"maxQueue"`
}

// ModelDeploymentsSource makes the gateway serve every ModelDeployment in a namespace.
type ModelDeploymentsSource struct {
	Namespace string `json:"namespace"`
}

type Config struct {
	Listen       string   `json:"listen"`
	QueueTimeout Duration `json:"queueTimeout"`
	// Models are configured by hand (local dev, or anything outside the operator).
	Models []Model `json:"models,omitempty"`
	// ModelDeployments, if set, adds and removes models as ModelDeployments change.
	ModelDeployments *ModelDeploymentsSource `json:"modelDeployments,omitempty"`
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) ApplyDefaults() {
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.QueueTimeout.Duration == 0 {
		c.QueueTimeout.Duration = 30 * time.Second
	}
	for i := range c.Models {
		m := &c.Models[i]
		if m.MaxInFlightPerReplica == 0 {
			m.MaxInFlightPerReplica = 4
		}
		if m.MaxQueue == 0 {
			m.MaxQueue = 16
		}
	}
}

func (c *Config) Validate() error {
	if len(c.Models) == 0 && c.ModelDeployments == nil {
		return errors.New("config: set models, modelDeployments, or both")
	}
	if c.ModelDeployments != nil && c.ModelDeployments.Namespace == "" {
		return errors.New("config: modelDeployments needs a namespace")
	}
	seen := map[string]bool{}
	for _, m := range c.Models {
		if m.Name == "" {
			return errors.New("config: model name is required")
		}
		if seen[m.Name] {
			return fmt.Errorf("config: duplicate model %q", m.Name)
		}
		seen[m.Name] = true
		switch {
		case len(m.Replicas) > 0 && m.Discovery != nil:
			return fmt.Errorf("config: model %q sets both replicas and discovery", m.Name)
		case len(m.Replicas) == 0 && m.Discovery == nil:
			return fmt.Errorf("config: model %q needs replicas or discovery", m.Name)
		case m.Discovery != nil && (m.Discovery.Namespace == "" || m.Discovery.Service == ""):
			return fmt.Errorf("config: model %q discovery needs namespace and service", m.Name)
		}
		if m.MaxInFlightPerReplica < 1 {
			return fmt.Errorf("config: model %q needs maxInFlightPerReplica >= 1", m.Name)
		}
		if m.MaxQueue < 0 {
			return fmt.Errorf("config: model %q has negative maxQueue", m.Name)
		}
		for _, addr := range m.Replicas {
			u, err := url.Parse(addr)
			if err != nil || u.Scheme == "" || u.Host == "" {
				return fmt.Errorf("config: model %q has invalid replica %q", m.Name, addr)
			}
		}
	}
	return nil
}
