package model

import (
	"strings"
	"testing"
)

func TestNewRequestID(t *testing.T) {
	a, b := NewRequestID(), NewRequestID()
	if !strings.HasPrefix(a, RequestIDPrefix) {
		t.Fatalf("id %q lacks prefix", a)
	}
	if a == b {
		t.Fatal("ids must be unique")
	}
	if a >= b {
		t.Errorf("ids must sort by creation time: %q >= %q", a, b)
	}
	if !IsRequestID(a) {
		t.Errorf("IsRequestID(%q) = false", a)
	}
}

func TestIsRequestID(t *testing.T) {
	for _, s := range []string{"", "req_", "01J9ZK3Q4W5E6R7T8Y9U0I1O2P", "req_nope", "job_01J9ZK3Q4W5E6R7T8Y9U0I1O2P"} {
		if IsRequestID(s) {
			t.Errorf("IsRequestID(%q) = true, want false", s)
		}
	}
}

func TestStatus(t *testing.T) {
	if !StatusDead.Valid() || Status("lost").Valid() {
		t.Fatal("Valid() is wrong")
	}
	final := map[Status]bool{StatusSucceeded: true, StatusDead: true, StatusUnknown: true, StatusCanceled: true}
	for _, s := range Statuses {
		if s.Final() != final[s] {
			t.Errorf("%s.Final() = %v, want %v", s, s.Final(), final[s])
		}
	}
}
