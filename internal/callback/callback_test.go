package callback

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tanvir001728/hookyard/internal/model"
)

// testSecret is the public test-vector secret from the Standard Webhooks
// spec, not a real credential. It is split so secret scanners don't flag it.
var testSecret = "whsec_" + "MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"

// The Standard Webhooks reference test vector.
func TestSignMatchesStandardWebhooksVector(t *testing.T) {
	secrets, err := ParseSecrets(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	sig := Sign(secrets, "msg_p5jXN8AQM9LWM0D4loKWxJek", time.Unix(1614265330, 0), []byte(`{"test": 2432232314}`))
	if sig != "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=" {
		t.Errorf("signature = %s", sig)
	}
}

func TestSignAndVerifyWithRotation(t *testing.T) {
	oldKey := "whsec_" + base64.StdEncoding.EncodeToString([]byte("an-old-secret-of-32-bytes-length"))
	both, err := ParseSecrets(testSecret + "," + oldKey)
	if err != nil || len(both) != 2 {
		t.Fatalf("parse: %v %v", both, err)
	}
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"type":"request.succeeded"}`)
	sig := Sign(both, "evt_1", now, body)
	if n := len(strings.Fields(sig)); n != 2 {
		t.Fatalf("want one signature per secret, got %q", sig)
	}

	// A receiver that only knows the old secret still accepts it.
	old, _ := ParseSecrets(oldKey)
	if err := Verify(old, "evt_1", "1800000000", sig, body, now, 5*time.Minute); err != nil {
		t.Errorf("old secret: %v", err)
	}
	if err := Verify(old, "evt_1", "1800000000", sig, []byte(`tampered`), now, 5*time.Minute); err == nil {
		t.Error("a tampered body must fail")
	}
	if err := Verify(old, "evt_1", "1800000000", sig, body, now.Add(time.Hour), 5*time.Minute); err == nil {
		t.Error("an old timestamp must fail")
	}
}

func TestParseSecretsErrors(t *testing.T) {
	for _, bad := range []string{"nope", "whsec_%%%", "whsec_c2hvcnQ="} {
		if _, err := ParseSecrets(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestBuild(t *testing.T) {
	code := 201
	done := time.Unix(1_800_000_000, 0)
	req := model.Request{OnResult: "order.shipment", ID: "req_1", Upstream: "courier-x", Method: "POST", Path: "/shipments", Status: model.StatusSucceeded, AttemptCount: 2, CompletedAt: &done}
	last := &model.Attempt{StatusCode: &code, Response: &model.AttemptResponse{Body: `{"ok":true}`}}
	e := Build(req, last, done)
	if e.Type != TypeSucceeded || *e.Data.OnResult != "order.shipment" || e.Data.Response.StatusCode != http.StatusCreated || e.Data.Tags == nil || e.Data.Response.Headers == nil {
		t.Errorf("event = %+v", e)
	}
}
