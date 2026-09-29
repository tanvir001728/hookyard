// Package retry defines retry presets and resolves retry policies from
// presets and partial overrides.
package retry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/tanvir001728/hookyard/internal/model"
)

// Preset names.
const (
	PresetNone     = "none"
	PresetQuick    = "quick"
	PresetStandard = "standard"
	PresetPatient  = "patient"
)

// DefaultPreset is used when neither the request nor the upstream sets a policy.
const DefaultPreset = PresetStandard

// Limits enforced on every resolved policy.
const (
	MinAttempts   = 1
	MaxAttempts   = 100
	MinMultiplier = 1.0
	MaxMultiplier = 10.0
)

var presets = map[string]model.RetryPolicy{
	PresetNone:     {Preset: PresetNone, MaxAttempts: 1, InitialInterval: time.Second, MaxInterval: time.Second, Multiplier: 2, MaxAge: 24 * time.Hour},
	PresetQuick:    {Preset: PresetQuick, MaxAttempts: 5, InitialInterval: time.Second, MaxInterval: 30 * time.Second, Multiplier: 2, MaxAge: 10 * time.Minute},
	PresetStandard: {Preset: PresetStandard, MaxAttempts: 10, InitialInterval: 5 * time.Second, MaxInterval: 10 * time.Minute, Multiplier: 2, MaxAge: 24 * time.Hour},
	PresetPatient:  {Preset: PresetPatient, MaxAttempts: 25, InitialInterval: 30 * time.Second, MaxInterval: time.Hour, Multiplier: 2, MaxAge: 72 * time.Hour},
}

// PresetNames lists the preset names in increasing order of persistence.
var PresetNames = []string{PresetNone, PresetQuick, PresetStandard, PresetPatient}

// Preset returns the named preset.
func Preset(name string) (model.RetryPolicy, bool) {
	p, ok := presets[name]
	return p, ok
}

// Spec is a partial retry policy: an optional preset plus optional overrides.
// In JSON and YAML it is either a preset name ("patient") or an object
// ({"preset": "patient", "max_attempts": 5}).
type Spec struct {
	Preset          string          `json:"preset,omitempty" yaml:"preset,omitempty"`
	MaxAttempts     *int            `json:"max_attempts,omitempty" yaml:"max_attempts,omitempty"`
	InitialInterval *model.Duration `json:"initial_interval,omitempty" yaml:"initial_interval,omitempty"`
	MaxInterval     *model.Duration `json:"max_interval,omitempty" yaml:"max_interval,omitempty"`
	Multiplier      *float64        `json:"multiplier,omitempty" yaml:"multiplier,omitempty"`
	MaxAge          *model.Duration `json:"max_age,omitempty" yaml:"max_age,omitempty"`
}

// IsZero reports whether the spec sets nothing.
func (s Spec) IsZero() bool { return s == Spec{} }

// specFields avoids recursion into the custom unmarshalers.
type specFields Spec

// UnmarshalJSON accepts a preset name or an object.
func (s *Spec) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var name string
		if err := json.Unmarshal(b, &name); err != nil {
			return err
		}
		*s = Spec{Preset: name}
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var f specFields
	if err := dec.Decode(&f); err != nil {
		return fmt.Errorf("retry must be a preset name or an object: %w", err)
	}
	*s = Spec(f)
	return nil
}

// UnmarshalYAML accepts a preset name or a mapping.
func (s *Spec) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*s = Spec{Preset: n.Value}
		return nil
	}
	// node.Decode does not inherit the parent decoder's KnownFields setting,
	// so reject unknown keys here; a typo must never be silently ignored.
	if n.Kind == yaml.MappingNode {
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if !slices.Contains(specKeys, key.Value) {
				return fmt.Errorf("line %d: unknown retry field %q (want one of %s)", key.Line, key.Value, strings.Join(specKeys, ", "))
			}
		}
	}
	var f specFields
	if err := n.Decode(&f); err != nil {
		return err
	}
	*s = Spec(f)
	return nil
}

var specKeys = []string{"preset", "max_attempts", "initial_interval", "max_interval", "multiplier", "max_age"}

// Resolve applies spec on top of base. If spec names a preset, the preset
// replaces base before the overrides are applied. The result is validated.
func Resolve(base model.RetryPolicy, spec Spec) (model.RetryPolicy, error) {
	p := base
	if spec.Preset != "" {
		preset, ok := presets[spec.Preset]
		if !ok {
			return model.RetryPolicy{}, &FieldError{Field: "preset", Message: fmt.Sprintf("unknown preset %q (want one of %s)", spec.Preset, strings.Join(PresetNames, ", "))}
		}
		p = preset
	}

	overridden := false
	if spec.MaxAttempts != nil {
		p.MaxAttempts, overridden = *spec.MaxAttempts, true
	}
	if spec.InitialInterval != nil {
		p.InitialInterval, overridden = spec.InitialInterval.Std(), true
	}
	if spec.MaxInterval != nil {
		p.MaxInterval, overridden = spec.MaxInterval.Std(), true
	}
	if spec.Multiplier != nil {
		p.Multiplier, overridden = *spec.Multiplier, true
	}
	if spec.MaxAge != nil {
		p.MaxAge, overridden = spec.MaxAge.Std(), true
	}
	// A customized policy keeps the preset name it was derived from only when
	// the preset was named explicitly; otherwise it is a custom policy.
	if overridden && spec.Preset == "" {
		p.Preset = ""
	}

	if err := Validate(p); err != nil {
		return model.RetryPolicy{}, err
	}
	return p, nil
}

// FieldError reports an invalid retry policy field.
type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

// Validate checks that a resolved policy is usable. It reports every problem.
func Validate(p model.RetryPolicy) error {
	var errs []error
	add := func(field, msg string) { errs = append(errs, &FieldError{Field: field, Message: msg}) }

	if p.MaxAttempts < MinAttempts || p.MaxAttempts > MaxAttempts {
		add("max_attempts", fmt.Sprintf("must be between %d and %d", MinAttempts, MaxAttempts))
	}
	if p.InitialInterval <= 0 {
		add("initial_interval", "must be positive")
	}
	if p.MaxInterval <= 0 {
		add("max_interval", "must be positive")
	} else if p.InitialInterval > p.MaxInterval {
		add("max_interval", "must not be less than initial_interval")
	}
	if p.Multiplier < MinMultiplier || p.Multiplier > MaxMultiplier {
		add("multiplier", fmt.Sprintf("must be between %g and %g", MinMultiplier, MaxMultiplier))
	}
	if p.MaxAge <= 0 {
		add("max_age", "must be positive")
	}
	return errors.Join(errs...)
}

// FieldErrors flattens an error returned by Resolve or Validate into its
// field errors. Other errors are returned as a single entry with no field.
func FieldErrors(err error) []FieldError {
	if err == nil {
		return nil
	}
	var out []FieldError
	var walk func(error)
	walk = func(e error) {
		if joined, ok := e.(interface{ Unwrap() []error }); ok {
			for _, inner := range joined.Unwrap() {
				walk(inner)
			}
			return
		}
		var fe *FieldError
		if errors.As(e, &fe) {
			out = append(out, *fe)
			return
		}
		out = append(out, FieldError{Message: e.Error()})
	}
	walk(err)
	slices.SortStableFunc(out, func(a, b FieldError) int { return strings.Compare(a.Field, b.Field) })
	return out
}
