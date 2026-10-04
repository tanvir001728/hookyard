package config

import (
	"fmt"
	"strconv"

	"go.yaml.in/yaml/v3"

	"github.com/tanvir001728/hookyard/internal/classify"
)

// ruleSchema is one entry of `classify:` in hookyard.yaml.
type ruleSchema struct {
	Name      string     `yaml:"name"`
	Status    *rawNode   `yaml:"status"`
	Body      string     `yaml:"body"`
	Equals    *rawNode   `yaml:"equals"`
	NotEquals *rawNode   `yaml:"not_equals"`
	In        []*rawNode `yaml:"in"`
	Exists    *bool      `yaml:"exists"`
	Then      string     `yaml:"then"`
}

// rawNode keeps a YAML value as-is, so a field can hold any scalar or list,
// and an explicit null is distinguishable from an absent field.
type rawNode struct{ n *yaml.Node }

func (r *rawNode) UnmarshalYAML(n *yaml.Node) error {
	r.n = n
	return nil
}

const maxRules = 50

func resolveRules(prefix string, raw []ruleSchema, add func(string, string, ...any)) classify.Rules {
	if len(raw) > maxRules {
		add(prefix, "at most %d rules are allowed", maxRules)
		return nil
	}
	rules := make(classify.Rules, 0, len(raw))
	for i, r := range raw {
		field := fmt.Sprintf("%s[%d]", prefix, i)
		rule := classify.Rule{Name: r.Name}
		ok := true
		fail := func(f, format string, args ...any) {
			add(field+f, format, args...)
			ok = false
		}

		then, err := classify.OutcomeFromThen(r.Then)
		if err != nil {
			fail(".then", "%s", err)
		}
		rule.Then = then

		if r.Status != nil {
			spec, err := statusSpec(r.Status.n)
			if err == nil {
				rule.Status, err = classify.ParseStatus(spec)
			}
			if err != nil {
				fail(".status", "%s", err)
			}
		}

		conds := 0
		if r.Equals != nil {
			conds++
			v, err := scalar(r.Equals.n)
			if err != nil {
				fail(".equals", "%s", err)
			}
			rule.Cond.Equals = &classify.Value{V: v}
		}
		if r.NotEquals != nil {
			conds++
			v, err := scalar(r.NotEquals.n)
			if err != nil {
				fail(".not_equals", "%s", err)
			}
			rule.Cond.NotEquals = &classify.Value{V: v}
		}
		if r.In != nil {
			conds++
			for j, n := range r.In {
				v, err := scalar(n.n)
				if err != nil {
					fail(fmt.Sprintf(".in[%d]", j), "%s", err)
				}
				rule.Cond.In = append(rule.Cond.In, classify.Value{V: v})
			}
			if len(r.In) == 0 {
				fail(".in", "must list at least one value")
			}
		}
		if r.Exists != nil {
			conds++
			rule.Cond.Exists = r.Exists
		}

		switch {
		case r.Body == "" && conds > 0:
			fail(".body", "is required with equals, not_equals, in or exists")
		case r.Body != "" && conds != 1:
			fail("", "a rule with body needs exactly one of equals, not_equals, in or exists")
		case r.Body == "" && r.Status == nil:
			fail("", "needs status, body, or both")
		}
		if r.Body != "" {
			if rule.Path, err = classify.ParsePath(r.Body); err != nil {
				fail(".body", "%s", err)
			}
		}
		if ok {
			rules = append(rules, rule)
		}
	}
	return rules
}

// statusSpec accepts 200, "4xx", "500-599" or a list of those.
func statusSpec(n *yaml.Node) (string, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		return n.Value, nil
	case yaml.SequenceNode:
		spec := ""
		for i, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return "", fmt.Errorf("status list entries must be codes, classes or ranges")
			}
			if i > 0 {
				spec += ","
			}
			spec += c.Value
		}
		return spec, nil
	}
	return "", fmt.Errorf("status must be a code, a class such as 4xx, a range such as 500-599, or a list")
}

// scalar decodes a YAML scalar into a JSON-comparable value.
func scalar(n *yaml.Node) (any, error) {
	if n.Kind != yaml.ScalarNode {
		return nil, fmt.Errorf("must be a single value (string, number, true, false or null)")
	}
	switch n.Tag {
	case "!!null":
		return nil, nil
	case "!!bool":
		return strconv.ParseBool(n.Value)
	case "!!int", "!!float":
		return strconv.ParseFloat(n.Value, 64)
	default:
		return n.Value, nil
	}
}
