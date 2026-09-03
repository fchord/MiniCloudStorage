package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/fchord/MiniCloudStorage/internal/config"
	"github.com/fchord/MiniCloudStorage/internal/totp"
	"golang.org/x/crypto/bcrypt"
)

const testAdminPassword = "test-admin-secret"

// RFC 6238 SHA1 test secret (base32). Tests only; never logged.
const testTOTPSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func mustHash(pw string) string {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	return string(h)
}

func newAdminTestHandler() *Handler {
	return &Handler{
		cfg:       config.Config{AdminPassword: testAdminPassword, PublicBaseURL: "https://minicloudstorage.19121122.xyz"},
		adminTok:  make(map[string]time.Time),
		setupTok:  make(map[string]time.Time),
		adminFail: make(map[string]*adminLoginState),
		acct: &adminAccount{
			passwordHash: mustHash(testAdminPassword),
			totpSecret:   testTOTPSecret,
			totpEnrolled: true,
		},
	}
}

func newUnenrolledAdminTestHandler() *Handler {
	return &Handler{
		cfg:       config.Config{AdminPassword: testAdminPassword, PublicBaseURL: "https://minicloudstorage.19121122.xyz"},
		adminTok:  make(map[string]time.Time),
		setupTok:  make(map[string]time.Time),
		adminFail: make(map[string]*adminLoginState),
		acct: &adminAccount{
			passwordHash: mustHash(testAdminPassword),
		},
	}
}

func totpFor(h *Handler, secret string) string {
	now := time.Now()
	if h != nil && h.nowFn != nil {
		now = h.nowFn()
	}
	code, err := totp.Code(secret, now)
	if err != nil {
		panic(err)
	}
	return code
}

func getAdminLoginLock(h *Handler, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/login", nil)
	if ip != "" {
		req.Header.Set("CF-Connecting-IP", ip)
	}
	req.RemoteAddr = "127.0.0.1:9"
	rec := httptest.NewRecorder()
	h.adminLoginLockStatus(rec, req)
	return rec
}

func postAdminLogin(h *Handler, ip, password string) *httptest.ResponseRecorder {
	code := ""
	if password == testAdminPassword {
		code = totpFor(h, testTOTPSecret)
	}
	return postAdminLoginWith(h, ip, password, code)
}

func postAdminLoginWith(h *Handler, ip, password, totpCode string) *httptest.ResponseRecorder {
	body, err := json.Marshal(map[string]string{"password": password, "totp": totpCode})
	if err != nil {
		panic(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if ip != "" {
		req.Header.Set("CF-Connecting-IP", ip)
	}
	req.RemoteAddr = "127.0.0.1:9"
	rec := httptest.NewRecorder()
	h.adminLogin(rec, req)
	return rec
}

func postAdminLoginRaw(h *Handler, ip string, body io.Reader, hdr http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/login", body)
	req.Header.Set("Content-Type", "application/json")
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Set(k, v)
		}
	}
	if ip != "" && req.Header.Get("CF-Connecting-IP") == "" {
		req.Header.Set("CF-Connecting-IP", ip)
	}
	req.RemoteAddr = "127.0.0.1:9"
	rec := httptest.NewRecorder()
	h.adminLogin(rec, req)
	return rec
}

func decodeAdminJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode json: %v body=%s", err, rec.Body.String())
	}
	return out
}

func TestAdminTokenTTLIsTwoHours(t *testing.T) {
	if adminTokenTTL != 2*time.Hour {
		t.Fatalf("adminTokenTTL = %s, want 2h", adminTokenTTL)
	}
}

func TestAdminLoginIssuesTokenWithTwoHourExpiry(t *testing.T) {
	h := newAdminTestHandler()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	h.nowFn = func() time.Time { return now }

	rec := postAdminLogin(h, "203.0.113.10", testAdminPassword)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	out := decodeAdminJSON(t, rec)
	token, _ := out["token"].(string)
	if token == "" {
		t.Fatal("missing token")
	}
	h.adminMu.Lock()
	exp := h.adminTok[token]
	h.adminMu.Unlock()
	if !exp.Equal(now.Add(2 * time.Hour)) {
		t.Fatalf("token expiry = %s, want %s", exp, now.Add(2*time.Hour))
	}
}

func TestAdminLoginLocksAfterThreeFailures(t *testing.T) {
	h := newAdminTestHandler()
	ip := "203.0.113.20"
	for i := 0; i < 3; i++ {
		rec := postAdminLogin(h, ip, "wrong")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("fail %d: status = %d, want 401 body=%s", i+1, rec.Code, rec.Body.String())
		}
		if got := decodeAdminJSON(t, rec)["error"]; got != "bad_password" {
			t.Fatalf("fail %d: error = %v, want bad_password", i+1, got)
		}
	}
	rec := postAdminLogin(h, ip, "wrong")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked status = %d, want 429 body=%s", rec.Code, rec.Body.String())
	}
	out := decodeAdminJSON(t, rec)
	if out["error"] != "locked" {
		t.Fatalf("error = %v, want locked", out["error"])
	}
	retry, ok := out["retry_after"].(float64)
	if !ok || retry < 59 || retry > 60 {
		t.Fatalf("retry_after = %v, want ~60", out["retry_after"])
	}
}

func TestAdminLoginSuccessClearsFailures(t *testing.T) {
	h := newAdminTestHandler()
	ip := "203.0.113.21"
	for i := 0; i < 2; i++ {
		if rec := postAdminLogin(h, ip, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("setup fail %d: %d", i+1, rec.Code)
		}
	}
	rec := postAdminLogin(h, ip, testAdminPassword)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	for i := 0; i < 3; i++ {
		rec := postAdminLogin(h, ip, "wrong")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("after clear, fail %d status = %d, want 401 (counter should have reset)", i+1, rec.Code)
		}
	}
	rec = postAdminLogin(h, ip, "wrong")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after reset, 4th fail status = %d, want 429", rec.Code)
	}
}

func TestAdminLoginLockDoesNotIncrement(t *testing.T) {
	h := newAdminTestHandler()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	h.nowFn = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	setNow := func(t time.Time) {
		mu.Lock()
		now = t
		mu.Unlock()
	}

	ip := "203.0.113.22"
	for i := 0; i < 3; i++ {
		postAdminLogin(h, ip, "wrong")
	}
	for i := 0; i < 20; i++ {
		rec := postAdminLogin(h, ip, "wrong")
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("spam %d: status = %d, want 429", i+1, rec.Code)
		}
		rec = postAdminLogin(h, ip, testAdminPassword)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("correct password during lock %d: status = %d, want 429 (must not act as oracle)", i+1, rec.Code)
		}
	}
	setNow(now.Add(time.Minute + time.Second))
	rec := postAdminLogin(h, ip, "wrong")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("after lock expired, fail status = %d, want 401 (one more failure, not already at cap)", rec.Code)
	}
	rec = postAdminLogin(h, ip, "wrong")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("extended lock status = %d, want 429", rec.Code)
	}
	out := decodeAdminJSON(t, rec)
	retry, _ := out["retry_after"].(float64)
	if retry < 119 || retry > 120 {
		t.Fatalf("retry_after = %v, want ~120 (2 min backoff, not 15 min cap)", out["retry_after"])
	}
}

func TestAdminLoginBackoffExtends(t *testing.T) {
	h := newAdminTestHandler()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	h.nowFn = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	setNow := func(t time.Time) {
		mu.Lock()
		now = t
		mu.Unlock()
	}

	ip := "203.0.113.23"
	wantSecs := []int{60, 120, 240, 480, 900, 900}
	for i := 0; i < 3; i++ {
		if rec := postAdminLogin(h, ip, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("seed fail %d: %d", i+1, rec.Code)
		}
	}
	cur := now
	for i, want := range wantSecs {
		rec := postAdminLogin(h, ip, "wrong")
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("lock %d: status = %d, want 429 body=%s", i+1, rec.Code, rec.Body.String())
		}
		retry, _ := decodeAdminJSON(t, rec)["retry_after"].(float64)
		if int(retry) != want {
			t.Fatalf("lock %d: retry_after = %v, want %d", i+1, retry, want)
		}
		cur = cur.Add(time.Duration(want)*time.Second + time.Second)
		setNow(cur)
		rec = postAdminLogin(h, ip, "wrong")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("post-expiry fail %d: status = %d, want 401", i+1, rec.Code)
		}
	}
}

func TestAdminLoginIPsAreIndependent(t *testing.T) {
	h := newAdminTestHandler()
	locked := "203.0.113.30"
	other := "203.0.113.31"
	for i := 0; i < 3; i++ {
		postAdminLogin(h, locked, "wrong")
	}
	if rec := postAdminLogin(h, locked, "wrong"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked ip status = %d, want 429", rec.Code)
	}
	rec := postAdminLogin(h, other, "wrong")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("other ip status = %d, want 401", rec.Code)
	}
	rec = postAdminLogin(h, other, testAdminPassword)
	if rec.Code != http.StatusOK {
		t.Fatalf("other ip login status = %d, want 200", rec.Code)
	}
}

func TestAdminLoginInvalidJSONCountsAsFailure(t *testing.T) {
	h := newAdminTestHandler()
	ip := "203.0.113.40"
	for i := 0; i < 3; i++ {
		rec := postAdminLoginRaw(h, ip, bytes.NewReader([]byte("{")), nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid json %d: status = %d, want 400", i+1, rec.Code)
		}
	}
	rec := postAdminLogin(h, ip, testAdminPassword)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after 3 invalid json, status = %d, want 429", rec.Code)
	}
}

func TestClientIPPrefersCloudflareThenXFF(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.1, 10.0.0.8")
	req.Header.Set("CF-Connecting-IP", "203.0.113.50")
	if got := clientIP(req); got != "203.0.113.50" {
		t.Fatalf("CF-Connecting-IP: got %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.2, 10.0.0.8")
	if got := clientIP(req); got != "198.51.100.2" {
		t.Fatalf("X-Forwarded-For: got %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "192.0.2.9:5555"
	if got := clientIP(req); got != "192.0.2.9" {
		t.Fatalf("RemoteAddr: got %q", got)
	}
}

func TestAdminLoginLockStatusWhenUnlocked(t *testing.T) {
	h := newAdminTestHandler()
	rec := getAdminLoginLock(h, "203.0.113.60")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	out := decodeAdminJSON(t, rec)
	if out["locked"] != false {
		t.Fatalf("locked = %v, want false", out["locked"])
	}
	retry, _ := out["retry_after"].(float64)
	if retry != 0 {
		t.Fatalf("retry_after = %v, want 0", out["retry_after"])
	}
	if out["totp_enrolled"] != true {
		t.Fatalf("totp_enrolled = %v, want true", out["totp_enrolled"])
	}
}

func TestAdminLoginLockStatusWhenLocked(t *testing.T) {
	h := newAdminTestHandler()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	h.nowFn = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	setNow := func(t time.Time) {
		mu.Lock()
		now = t
		mu.Unlock()
	}

	ip := "203.0.113.61"
	for i := 0; i < 3; i++ {
		if rec := postAdminLogin(h, ip, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("fail %d: status = %d, want 401", i+1, rec.Code)
		}
	}
	rec := getAdminLoginLock(h, ip)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	out := decodeAdminJSON(t, rec)
	if out["locked"] != true {
		t.Fatalf("locked = %v, want true", out["locked"])
	}
	retry, _ := out["retry_after"].(float64)
	if retry < 59 || retry > 60 {
		t.Fatalf("retry_after = %v, want ~60", out["retry_after"])
	}

	setNow(now.Add(15 * time.Second))
	out = decodeAdminJSON(t, getAdminLoginLock(h, ip))
	retry, _ = out["retry_after"].(float64)
	if out["locked"] != true {
		t.Fatalf("after 15s locked = %v, want true", out["locked"])
	}
	if retry < 44 || retry > 45 {
		t.Fatalf("after 15s retry_after = %v, want ~45", out["retry_after"])
	}
}

func TestAdminLoginLockStatusDoesNotIncrementFailures(t *testing.T) {
	h := newAdminTestHandler()
	ip := "203.0.113.62"
	for i := 0; i < 2; i++ {
		if rec := postAdminLogin(h, ip, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("setup fail %d: %d", i+1, rec.Code)
		}
	}
	for i := 0; i < 20; i++ {
		rec := getAdminLoginLock(h, ip)
		if rec.Code != http.StatusOK {
			t.Fatalf("get %d: status = %d, want 200", i+1, rec.Code)
		}
		out := decodeAdminJSON(t, rec)
		if out["locked"] != false {
			t.Fatalf("get %d: locked = %v, want false (GET must not count as a failure)", i+1, out["locked"])
		}
	}
	rec := postAdminLogin(h, ip, "wrong")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("3rd fail status = %d, want 401 (GET must not have incremented)", rec.Code)
	}
	rec = postAdminLogin(h, ip, "wrong")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("4th fail status = %d, want 429", rec.Code)
	}

	for i := 0; i < 10; i++ {
		rec := getAdminLoginLock(h, ip)
		if rec.Code != http.StatusOK {
			t.Fatalf("locked get %d: status = %d, want 200", i+1, rec.Code)
		}
		if decodeAdminJSON(t, rec)["locked"] != true {
			t.Fatalf("locked get %d: want locked=true", i+1)
		}
	}
	rec = postAdminLogin(h, ip, "wrong")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("POST during lock after GETs status = %d, want 429 (GET must not extend lock)", rec.Code)
	}
}

func TestAdminLoginLockStatusIsPublic(t *testing.T) {
	h := newAdminTestHandler()
	ip := "203.0.113.63"
	for i := 0; i < 3; i++ {
		postAdminLogin(h, ip, "wrong")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/login", nil)
	req.Header.Set("CF-Connecting-IP", ip)
	req.RemoteAddr = "127.0.0.1:9"
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /login without auth status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	out := decodeAdminJSON(t, rec)
	if out["locked"] != true {
		t.Fatalf("locked = %v, want true", out["locked"])
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/records", nil)
	req.RemoteAddr = "127.0.0.1:9"
	rec = httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /records without auth status = %d, want 401 (other admin APIs must stay protected)", rec.Code)
	}
}

func TestAdminLoginLockDurationSequence(t *testing.T) {
	want := []time.Duration{
		time.Minute,
		2 * time.Minute,
		4 * time.Minute,
		8 * time.Minute,
		15 * time.Minute,
		15 * time.Minute,
	}
	for i, d := range want {
		failures := adminLoginFailLockAfter + i
		if got := adminLoginLockDuration(failures); got != d {
			t.Fatalf("failures=%d duration=%s, want %s", failures, got, d)
		}
	}
	if got := adminLoginLockDuration(2); got != 0 {
		t.Fatalf("failures=2 duration=%s, want 0", got)
	}
}

func TestPublicLoginRejectedWhenTOTPNotEnrolled(t *testing.T) {
	h := newUnenrolledAdminTestHandler()
	rec := postAdminLoginWith(h, "203.0.113.80", testAdminPassword, "123456")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := decodeAdminJSON(t, rec)["error"]; got != "totp_not_enrolled" {
		t.Fatalf("error = %v, want totp_not_enrolled", got)
	}
	rec = getAdminLoginLock(h, "203.0.113.80")
	out := decodeAdminJSON(t, rec)
	if out["totp_enrolled"] != false {
		t.Fatalf("totp_enrolled = %v, want false", out["totp_enrolled"])
	}
}

func TestEnrolledLoginWrongTOTPCountsTowardLock(t *testing.T) {
	h := newAdminTestHandler()
	ip := "203.0.113.81"
	for i := 0; i < 3; i++ {
		rec := postAdminLoginWith(h, ip, testAdminPassword, "000000")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("fail %d: status = %d, want 401", i+1, rec.Code)
		}
		if got := decodeAdminJSON(t, rec)["error"]; got != "bad_totp" {
			t.Fatalf("fail %d: error = %v, want bad_totp", i+1, got)
		}
	}
	rec := postAdminLogin(h, ip, testAdminPassword)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked status = %d, want 429", rec.Code)
	}
}
