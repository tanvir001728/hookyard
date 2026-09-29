package retry

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/tanvir001728/hookyard/internal/model"
)

func ptr[T any](v T) *T { return &v }

func TestPresetsAreValid(t *testing.T) {
	for _, name := range PresetNames {
		p, ok := Preset(name)
		if !ok {
			t.Fatalf("preset %q missing", name)
		}
		if err := Validate(p); err != nil {
			t.Errorf("preset %q invalid: %v", name, err)
		}
		if p.Preset != name {
			t.Errorf("preset %q has Preset=%q", name, p.Preset)
		}
	}
}

func TestResolve(t *testing.T) {
	standard, _ := Preset(PresetStandard)
	patient, _ := Preset(PresetPatient)

	tests := []struct {
		name string
		base model.RetryPolicy
		spec Spec
		want model.RetryPolicy
	}{
		{"empty spec keeps base", standard, Spec{}, standard},
		{"preset replaces base", standard, Spec{Preset: PresetPatient}, patient},
		{
			"override on base becomes custom",
			standard,
			Spec{MaxAttempts: ptr(3)},
			model.RetryPolicy{MaxAttempts: 3, InitialInterval: 5 * time.Second, MaxInterval: 10 * time.Minute, Multiplier: 2, MaxAge: 24 * time.Hour},
		},
		{
			"override on named preset keeps the name",
			standard,
			Spec{Preset: PresetPatient, MaxInterval: ptr(model.Duration(2 * time.Hour))},
			model.RetryPolicy{Preset: PresetPatient, MaxAttempts: 25, InitialInterval: 30 * time.Second, MaxInterval: 2 * time.Hour, Multiplier: 2, MaxAge: 72 * time.Hour},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.base, tt.spec)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestResolveErrors(t *testing.T) {
	standard, _ := Preset(PresetStandard)

	_, err := Resolve(standard, Spec{Preset: "forever"})
	if err == nil || !strings.Contains(err.Error(), `unknown preset "forever"`) {
		t.Fatalf("unknown preset: err = %v", err)
	}

	_, err = Resolve(standard, Spec{
		MaxAttempts:     ptr(0),
		InitialInterval: ptr(model.Duration(time.Hour)),
		MaxInterval:     ptr(model.Duration(time.Minute)),
		Multiplier:      ptr(0.5),
		MaxAge:          ptr(model.Duration(0)),
	})
	fields := FieldErrors(err)
	var names []string
	for _, f := range fields {
		names = append(names, f.Field)
	}
	if got := strings.Join(names, ","); got != "max_age,max_attempts,max_interval,multiplier" {
		t.Errorf("field errors = %s (%v)", got, err)
	}
}

func TestSpecJSON(t *testing.T) {
	var s Spec
	if err := json.Unmarshal([]byte(`"quick"`), &s); err != nil || s.Preset != PresetQuick {
		t.Fatalf("preset string: %+v %v", s, err)
	}
	if err := json.Unmarshal([]byte(`{"preset":"patient","max_attempts":5,"max_age":"2h"}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Preset != PresetPatient || *s.MaxAttempts != 5 || s.MaxAge.Std() != 2*time.Hour {
		t.Errorf("object: %+v", s)
	}
	if err := json.Unmarshal([]byte(`{"retries":5}`), &s); err == nil {
		t.Error("unknown fields must be rejected")
	}
}

func TestSpecYAML(t *testing.T) {
	var v struct {
		A Spec `yaml:"a"`
		B Spec `yaml:"b"`
	}
	src := "a: patient\nb:\n  max_attempts: 3\n  initial_interval: 2s\n"
	if err := yaml.Unmarshal([]byte(src), &v); err != nil {
		t.Fatal(err)
	}
	if v.A.Preset != PresetPatient || *v.B.MaxAttempts != 3 || v.B.InitialInterval.Std() != 2*time.Second {
		t.Errorf("got %+v", v)
	}

	err := yaml.Unmarshal([]byte("a:\n  max_attempt: 3\n"), &v)
	if err == nil || !strings.Contains(err.Error(), `unknown retry field "max_attempt"`) || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("typo must be rejected with its line: %v", err)
	}
}
