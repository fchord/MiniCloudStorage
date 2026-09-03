package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fchord/MiniCloudStorage/internal/totp"
)

func lanSetupRequest(h *Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	var r *bytes.Reader
	if body != nil {
		r = bytes.NewReader(body)
	} else {
		r = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, r)
	req.Host = "192.168.43.111"
	req.RemoteAddr = "192.168.43.10:9"
	req.Header.Set("X-Real-IP", "192.168.43.10")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	return rec
}

func TestSetupForbiddenWhenCloudflareOrPublicOrNonLAN(t *testing.T) {
	h := newUnenrolledAdminTestHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/setup", nil)
	req.Host = "192.168.43.111"
	req.RemoteAddr = "192.168.43.10:9"
	req.Header.Set("X-Real-IP", "192.168.43.10")
	req.Header.Set("CF-Connecting-IP", "203.0.113.90")
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("CF header status = %d, want 404", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/setup", nil)
	req.Host = "minicloudstorage.19121122.xyz"
	req.RemoteAddr = "192.168.43.10:9"
	req.Header.Set("X-Real-IP", "192.168.43.10")
	rec = httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("public host status = %d, want 404", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/setup", nil)
	req.Host = "192.168.43.111"
	req.RemoteAddr = "203.0.113.91:9"
	req.Header.Set("X-Real-IP", "203.0.113.91")
	rec = httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-LAN status = %d, want 404", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/setup", nil)
	req.Host = "192.168.43.111"
	req.RemoteAddr = "10.244.0.1:9"
	req.Header.Set("X-Real-IP", "10.244.0.1")
	rec = httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("NodePort SNAT status = %d, want 200", rec.Code)
	}

	rec = lanSetupRequest(h, http.MethodGet, "/api/v1/admin/setup", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("LAN status = %d, want 200", rec.Code)
	}
	out := decodeAdminJSON(t, rec)
	if out["totp_enrolled"] != false {
		t.Fatalf("totp_enrolled = %v, want false", out["totp_enrolled"])
	}
}

func TestSetupEnrollConfirmWrongCodeDoesNotWrite(t *testing.T) {
	h := newUnenrolledAdminTestHandler()
	h.acct.totpPending = testTOTPSecret

	loginBody, err := json.Marshal(map[string]string{"password": testAdminPassword})
	if err != nil {
		t.Fatal(err)
	}
	rec := lanSetupRequest(h, http.MethodPost, "/api/v1/admin/setup/login", loginBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup login status = %d, want 200", rec.Code)
	}
	token, _ := decodeAdminJSON(t, rec)["token"].(string)
	if token == "" {
		t.Fatal("missing setup token")
	}

	wrong, err := json.Marshal(map[string]string{"code": "000000"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/setup/totp/confirm", bytes.NewReader(wrong))
	req.Host = "192.168.43.111"
	req.RemoteAddr = "192.168.43.10:9"
	req.Header.Set("X-Real-IP", "192.168.43.10")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong code status = %d, want 401", rec.Code)
	}
	if got := decodeAdminJSON(t, rec)["error"]; got != "bad_totp" {
		t.Fatalf("error = %v, want bad_totp", got)
	}
	acct, ok := h.snapshotAccount()
	if !ok || acct.totpEnrolled || acct.totpSecret != "" {
		t.Fatal("wrong code must not enroll")
	}
	if acct.totpPending != testTOTPSecret {
		t.Fatal("pending secret should remain until a valid confirm")
	}

	goodCode := totpFor(h, testTOTPSecret)
	good, err := json.Marshal(map[string]string{"code": goodCode})
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/setup/totp/confirm", bytes.NewReader(good))
	req.Host = "192.168.43.111"
	req.RemoteAddr = "192.168.43.10:9"
	req.Header.Set("X-Real-IP", "192.168.43.10")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, want 200", rec.Code)
	}
	out := decodeAdminJSON(t, rec)
	if out["totp_enrolled"] != true {
		t.Fatalf("totp_enrolled = %v, want true", out["totp_enrolled"])
	}
	acct, ok = h.snapshotAccount()
	if !ok || !acct.totpEnrolled || acct.totpSecret != testTOTPSecret {
		t.Fatal("correct code must enroll")
	}
	if acct.totpPending != "" {
		t.Fatal("pending must be cleared after enroll")
	}

	rec = lanSetupRequest(h, http.MethodGet, "/api/v1/admin/setup", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status GET after enroll = %d, want 200", rec.Code)
	}
	if decodeAdminJSON(t, rec)["totp_enrolled"] != true {
		t.Fatal("status totp_enrolled want true")
	}
}

func TestSetupBeginThenConfirmEnrolls(t *testing.T) {
	h := newUnenrolledAdminTestHandler()
	loginBody, err := json.Marshal(map[string]string{"password": testAdminPassword})
	if err != nil {
		t.Fatal(err)
	}
	rec := lanSetupRequest(h, http.MethodPost, "/api/v1/admin/setup/login", loginBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup login status = %d, want 200", rec.Code)
	}
	token, _ := decodeAdminJSON(t, rec)["token"].(string)
	if token == "" {
		t.Fatal("missing setup token")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/setup/totp/begin", nil)
	req.Host = "192.168.43.111"
	req.RemoteAddr = "192.168.43.10:9"
	req.Header.Set("X-Real-IP", "192.168.43.10")
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("begin status = %d, want 200", rec.Code)
	}
	var begin map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &begin); err != nil {
		t.Fatalf("begin decode: %v", err)
	}
	secret, _ := begin["secret"].(string)
	otpauth, _ := begin["otpauth_url"].(string)
	if secret == "" || otpauth == "" {
		t.Fatal("begin missing fields")
	}
	if begin["totp_enrolled"] != false {
		t.Fatal("begin must not enroll")
	}
	acct, _ := h.snapshotAccount()
	if acct.totpEnrolled {
		t.Fatal("begin must not set enrolled")
	}

	code, err := totp.Code(secret, h.now())
	if err != nil {
		t.Fatal(err)
	}
	confirm, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/setup/totp/confirm", bytes.NewReader(confirm))
	req.Host = "192.168.43.111"
	req.RemoteAddr = "192.168.43.10:9"
	req.Header.Set("X-Real-IP", "192.168.43.10")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, want 200", rec.Code)
	}
	if decodeAdminJSON(t, rec)["totp_enrolled"] != true {
		t.Fatal("confirm totp_enrolled want true")
	}
}
