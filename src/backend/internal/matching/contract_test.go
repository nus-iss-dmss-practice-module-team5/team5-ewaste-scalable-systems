package matching

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func golden(t *testing.T, name string) object {
	t.Helper()
	raw, err := os.ReadFile("../../../matcher/tests/fixtures/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	v, err := decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestApprovedGoldenParity(t *testing.T) {
	for _, name := range []string{"M-F01", "C2-EX01"} {
		t.Run(name, func(t *testing.T) {
			f := golden(t, name)
			out, err := expectedOutput(obj(f["input"]))
			if err != nil {
				t.Fatal(err)
			}
			if !equal(out, f["expected_output"]) {
				t.Fatalf("golden mismatch\nactual %s\nexpected %s", canonical(out), canonical(f["expected_output"]))
			}
		})
	}
}
func TestEvidenceRejectsAlteredHashesAndUsesExactCapacity(t *testing.T) {
	in := obj(golden(t, "M-F01")["input"])
	org := obj(arr(in["organisations"])[0])
	pool := obj(arr(org["capacity_pools"])[0])
	pool["reserved_kg"] = "0.01"
	if _, err := expectedOutput(in); err == nil {
		t.Fatal("tampered frozen hash accepted")
	}
	setHashes(in)
	out, err := expectedOutput(in)
	if err != nil {
		t.Fatal(err)
	}
	c := obj(arr(out["candidates"])[0])
	if c["is_matched"] != false || c["reason_code"] != "INSUFFICIENT_CAPACITY" || c["available_capacity_kg"] != "99.99" {
		t.Fatal(c)
	}
}
func TestStrictJSON(t *testing.T) {
	for _, raw := range []string{`{"x":1,"x":2}`, `{"a":{"x":1,"x":2}}`, `{} {}`, `[]`} {
		if _, err := decode([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestWorkloadAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	cfg := AuthConfig{Issuer: "test-issuer", Audience: "matching-api", Secret: strings.Repeat("x", 32), MaxBodyBytes: 1024}
	if err := Register(r, NewStore(nil), cfg); err != nil {
		t.Fatal(err)
	}
	base := jwt.RegisteredClaims{Issuer: cfg.Issuer, Subject: "matching-worker", Audience: jwt.ClaimStrings{cfg.Audience}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}
	for _, tc := range []struct {
		name, scope, issuer, subject, algorithm string
		expired                                 bool
		status                                  int
	}{
		{"wrong scope", "unrelated", cfg.Issuer, "matching-worker", "HS256", false, 403},
		{"wrong issuer", "matching.execute", "other", "matching-worker", "HS256", false, 401},
		{"user token", "matching.execute", cfg.Issuer, "donor", "HS256", false, 401},
		{"expired", "matching.execute", cfg.Issuer, "matching-worker", "HS256", true, 401},
		{"wrong algorithm", "matching.execute", cfg.Issuer, "matching-worker", "HS384", false, 401},
		{"authorised invalid body", "matching.execute", cfg.Issuer, "matching-worker", "HS256", false, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := workloadClaims{Scope: tc.scope, RegisteredClaims: base}
			claims.Issuer = tc.issuer
			claims.Subject = tc.subject
			if tc.expired {
				claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))
			}
			token, err := jwt.NewWithClaims(jwt.GetSigningMethod(tc.algorithm), claims).SignedString([]byte(cfg.Secret))
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/internal/v1/matching/runs", bytes.NewBufferString(`{}`))
			req.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestWorkerKeyCannotAuthorizeOperatorReruns(t *testing.T) {
	cfg := AuthConfig{Issuer: "test", Audience: "api", Secret: strings.Repeat("w", 32), OperatorSecret: strings.Repeat("o", 32), MaxBodyBytes: 1024}
	r := gin.New()
	if err := Register(r, NewStore(nil), cfg); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kid, subject, key string
		status            int
	}{
		{"worker", "matching-worker", cfg.Secret, 403},
		{"operator", "matching-operator", cfg.Secret, 401},
		{"operator", "matching-operator", cfg.OperatorSecret, 400},
	} {
		claims := workloadClaims{Scope: "matching.execute matching.rerun", RegisteredClaims: jwt.RegisteredClaims{Issuer: cfg.Issuer, Audience: jwt.ClaimStrings{cfg.Audience}, Subject: tc.subject, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}}
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		token.Header["kid"] = tc.kid
		signed, err := token.SignedString([]byte(tc.key))
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/internal/v1/matching/runs", strings.NewReader(`{"trigger_type":"EXPLICIT_RUN"}`))
		req.Header.Set("Authorization", "Bearer "+signed)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("kid %s: %d %s", tc.kid, w.Code, w.Body.String())
		}
	}
}
