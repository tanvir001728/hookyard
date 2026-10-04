package config

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/tanvir001728/hookyard/internal/breaker"
	"github.com/tanvir001728/hookyard/internal/model"
)

// Breaker is an upstream's resolved circuit breaker configuration.
type Breaker struct {
	Enabled bool
	breaker.Config
}

// breakerSpec is `breaker:` in hookyard.yaml: `off`, or a mapping of overrides.
type breakerSpec struct {
	set                 bool
	off                 bool
	FailureRate         *float64        `yaml:"failure_rate"`
	MinCalls            *int            `yaml:"min_calls"`
	Window              *model.Duration `yaml:"window"`
	ConsecutiveFailures *int            `yaml:"consecutive_failures"`
	Cooldown            *model.Duration `yaml:"cooldown"`
	Probes              *int            `yaml:"probes"`
}

var breakerKeys = []string{"failure_rate", "min_calls", "window", "consecutive_failures", "cooldown", "probes"}

func (b *breakerSpec) UnmarshalYAML(n *yaml.Node) error {
	b.set = true
	if n.Kind == yaml.ScalarNode {
		switch strings.ToLower(n.Value) {
		case "off", "false":
			b.off = true
			return nil
		case "on", "true":
			return nil
		}
		return fmt.Errorf("line %d: breaker must be off, on, or a mapping of settings", n.Line)
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i < len(n.Content); i += 2 {
			if key := n.Content[i]; !slices.Contains(breakerKeys, key.Value) {
				return fmt.Errorf("line %d: unknown breaker field %q (want one of %s)", key.Line, key.Value, strings.Join(breakerKeys, ", "))
			}
		}
	}
	type fields breakerSpec
	var f fields
	if err := n.Decode(&f); err != nil {
		return err
	}
	*b = breakerSpec(f)
	b.set = true
	return nil
}

func resolveBreaker(prefix string, base Breaker, s breakerSpec, add func(string, string, ...any)) Breaker {
	if !s.set {
		return base
	}
	if s.off {
		return Breaker{Enabled: false, Config: base.Config}
	}
	out := Breaker{Enabled: true, Config: base.Config}
	if s.FailureRate != nil {
		if *s.FailureRate <= 0 || *s.FailureRate > 1 {
			add(prefix+".failure_rate", "must be greater than 0 and at most 1 (for example 0.5 for 50%%)")
		}
		out.FailureRate = *s.FailureRate
	}
	if s.MinCalls != nil {
		if *s.MinCalls < 1 || *s.MinCalls > 10_000 {
			add(prefix+".min_calls", "must be between 1 and 10000")
		}
		out.MinCalls = *s.MinCalls
	}
	if s.Window != nil {
		if d := s.Window.Std(); d < time.Second || d > time.Hour {
			add(prefix+".window", "must be between 1s and 1h")
		}
		out.Window = s.Window.Std()
	}
	if s.ConsecutiveFailures != nil {
		if *s.ConsecutiveFailures < 1 || *s.ConsecutiveFailures > 10_000 {
			add(prefix+".consecutive_failures", "must be between 1 and 10000")
		}
		out.ConsecutiveFailures = *s.ConsecutiveFailures
	}
	if s.Cooldown != nil {
		if d := s.Cooldown.Std(); d < 100*time.Millisecond || d > time.Hour {
			add(prefix+".cooldown", "must be between 100ms and 1h")
		}
		out.Cooldown = s.Cooldown.Std()
	}
	if s.Probes != nil {
		if *s.Probes < 1 || *s.Probes > 100 {
			add(prefix+".probes", "must be between 1 and 100")
		}
		out.Probes = *s.Probes
	}
	return out
}
