package classify

import (
	"testing"

	"github.com/tanvir001728/hookyard/internal/model"
)

func status(t *testing.T, spec string) *StatusMatcher {
	t.Helper()
	m, err := ParseStatus(spec)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func path(t *testing.T, p string) []string {
	t.Helper()
	parts, err := ParsePath(p)
	if err != nil {
		t.Fatal(err)
	}
	return parts
}

func TestParseStatus(t *testing.T) {
	m, err := ParseStatus("200, 4xx,500-502")
	if err != nil {
		t.Fatal(err)
	}
	for code, want := range map[int]bool{200: true, 201: false, 404: true, 499: true, 500: true, 502: true, 503: false} {
		if got := m.Match(code); got != want {
			t.Errorf("Match(%d) = %v, want %v", code, got, want)
		}
	}
	for _, bad := range []string{"", "abc", "6xx", "99", "600", "500-400", "x-500"} {
		if _, err := ParseStatus(bad); err == nil {
			t.Errorf("ParseStatus(%q) should fail", bad)
		}
	}
}

func TestParsePath(t *testing.T) {
	if p, err := ParsePath("$.error.code"); err != nil || len(p) != 2 || p[1] != "code" {
		t.Errorf("got %v %v", p, err)
	}
	for _, bad := range []string{"", "a..b", ".a"} {
		if _, err := ParsePath(bad); err == nil {
			t.Errorf("ParsePath(%q) should fail", bad)
		}
	}
}

func TestClassify(t *testing.T) {
	yes, no := true, false
	rules := Rules{
		{Name: "fake success", Status: status(t, "200"), Path: path(t, "status"), Cond: Condition{Equals: &Value{"FAILED"}}, Then: model.OutcomeRetryableFailure},
		{Status: status(t, "409"), Then: model.OutcomeSuccess},
		{Status: status(t, "4xx"), Path: path(t, "error.code"), Cond: Condition{In: []Value{{"busy"}, {"try_later"}}}, Then: model.OutcomeRetryableFailure},
		{Status: status(t, "2xx"), Path: path(t, "items.0.attempts"), Cond: Condition{Equals: &Value{3}}, Then: model.OutcomePermanentFailure},
		{Status: status(t, "2xx"), Path: path(t, "warning"), Cond: Condition{Exists: &yes}, Then: model.OutcomeRetryableFailure},
		{Status: status(t, "202"), Path: path(t, "queued"), Cond: Condition{NotEquals: &Value{true}}, Then: model.OutcomePermanentFailure},
		{Status: status(t, "204"), Path: path(t, "anything"), Cond: Condition{Exists: &no}, Then: model.OutcomeSuccess},
	}

	tests := []struct {
		name      string
		status    int
		body      string
		want      model.AttemptOutcome
		wantLabel string
		match     bool
	}{
		{"fake 200 error", 200, `{"status":"FAILED"}`, model.OutcomeRetryableFailure, "fake success", true},
		{"real 200", 200, `{"status":"OK"}`, "", "", false},
		{"409 counts as success", 409, `nope`, model.OutcomeSuccess, "rule 2", true},
		{"4xx with retryable code", 400, `{"error":{"code":"busy"}}`, model.OutcomeRetryableFailure, "rule 3", true},
		{"4xx with other code", 400, `{"error":{"code":"invalid"}}`, "", "", false},
		{"array index and number", 201, `{"items":[{"attempts":3.0}]}`, model.OutcomePermanentFailure, "rule 4", true},
		{"exists", 200, `{"warning":"slow"}`, model.OutcomeRetryableFailure, "rule 5", true},
		{"not equals", 202, `{"queued":false}`, model.OutcomePermanentFailure, "rule 6", true},
		{"not equals needs the field", 202, `{}`, "", "", false},
		{"exists false on non-JSON body", 204, ``, "", "", false},
		{"exists false on JSON body", 204, `{}`, model.OutcomeSuccess, "rule 7", true},
		{"type matters for strings", 200, `{"status":1}`, "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, label, ok := rules.Classify(tt.status, []byte(tt.body))
			if ok != tt.match || got != tt.want || label != tt.wantLabel {
				t.Errorf("Classify = %q %q %v, want %q %q %v", got, label, ok, tt.want, tt.wantLabel, tt.match)
			}
		})
	}
}

func TestOutcomeFromThen(t *testing.T) {
	for then, want := range map[string]model.AttemptOutcome{"success": model.OutcomeSuccess, "retry": model.OutcomeRetryableFailure, "fail": model.OutcomePermanentFailure} {
		if got, err := OutcomeFromThen(then); err != nil || got != want {
			t.Errorf("%s: %v %v", then, got, err)
		}
	}
	if _, err := OutcomeFromThen("maybe"); err == nil {
		t.Error("unknown then must fail")
	}
}
