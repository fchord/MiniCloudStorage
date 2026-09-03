package api

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/fchord/MiniCloudStorage/internal/totp"
	"golang.org/x/crypto/bcrypt"
)

const setupLANCIDR = "192.168.43.0/24"

var setupLANNet = func() *net.IPNet {
	_, n, err := net.ParseCIDR(setupLANCIDR)
	if err != nil {
		panic(err)
	}
	return n
}()

func (h *Handler) requireSetupIntranet(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if setupRequestForbidden(r, h.publicSetupHosts()) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) publicSetupHosts() map[string]bool {
	out := map[string]bool{
		"minicloudstorage.19121122.xyz": true,
		"119.136.88.183":                true,
	}
	if raw := strings.TrimSpace(h.cfg.PublicBaseURL); raw != "" {
		if u, err := url.Parse(raw); err == nil {
			if host := strings.ToLower(strings.TrimSpace(u.Hostname())); host != "" {
				out[host] = true
			}
		}
	}
	return out
}

func setupRequestForbidden(r *http.Request, publicHosts map[string]bool) bool {
	if strings.TrimSpace(r.Header.Get("CF-Connecting-IP")) != "" {
		return true
	}
	host := strings.ToLower(requestHost(r))
	if publicHosts[host] {
		return true
	}
	ip := net.ParseIP(setupPeerIP(r))
	if ip == nil {
		return true
	}
	if inSetupLAN(ip.String()) {
		return false
	}
	// NodePort often SNATs same-node clients to 10.244.0.0/16. Allow that
	// only when Host is the LAN vhost. Globally routable peers stay denied
	// so http://<public-ip>:30987 with a spoofed LAN Host cannot enroll.
	if ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
		return true
	}
	return !isSetupLANHost(host)
}

func isSetupLANHost(host string) bool {
	return host == "192.168.43.111"
}

func requestHost(r *http.Request) string {
	host := strings.TrimSpace(r.Host)
	if host == "" && r.URL != nil {
		host = strings.TrimSpace(r.URL.Host)
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

func setupPeerIP(r *http.Request) string {
	if ip := parseClientIP(r.Header.Get("X-Real-IP")); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		if ip := net.ParseIP(host); ip != nil {
			return ip.String()
		}
		return host
	}
	if ip := net.ParseIP(strings.TrimSpace(r.RemoteAddr)); ip != nil {
		return ip.String()
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func inSetupLAN(ipStr string) bool {
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return false
	}
	return setupLANNet.Contains(ip)
}

func (h *Handler) adminSetupStatus(w http.ResponseWriter, r *http.Request) {
	if !h.adminEnabled() {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "admin_disabled"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"totp_enrolled": h.totpEnrolled(),
	})
}

func (h *Handler) adminSetupLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if retryAfter, locked := h.adminLoginLocked(ip); locked {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":       "locked",
			"retry_after": retryAfter,
		})
		return
	}
	acct, ok := h.snapshotAccount()
	if !ok || strings.TrimSpace(acct.passwordHash) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "admin_disabled"})
		return
	}
	var req struct {
		Password string `json:"password"`
		TOTP     string `json:"totp"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		h.recordAdminLoginFailure(ip)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if !adminHashOK(req.Password, acct.passwordHash) {
		h.recordAdminLoginFailure(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad_password"})
		return
	}
	if acct.totpEnrolled && strings.TrimSpace(acct.totpSecret) != "" {
		if !totp.Validate(acct.totpSecret, req.TOTP, h.now()) {
			h.recordAdminLoginFailure(ip)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad_totp"})
			return
		}
	}
	h.clearAdminLoginFailure(ip)
	token, err := h.issueNamedToken(&h.setupTok)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "token_error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":         token,
		"totp_enrolled": acct.totpEnrolled && strings.TrimSpace(acct.totpSecret) != "",
	})
}

func (h *Handler) requireSetupAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.adminEnabled() {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "admin_disabled"})
			return
		}
		token := bearerToken(r)
		if token == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "need_auth"})
			return
		}
		h.adminMu.Lock()
		h.pruneAdminLocked()
		exp, ok := h.setupTok[token]
		h.adminMu.Unlock()
		if !ok || h.now().After(exp) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "need_auth"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) adminSetupPassword(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.snapshotAccount()
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "admin_disabled"})
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if !adminHashOK(req.CurrentPassword, acct.passwordHash) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad_password"})
		return
	}
	newPass := strings.TrimSpace(req.NewPassword)
	if newPass == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty_password"})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPass), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "hash_error"})
		return
	}
	acct.passwordHash = string(hash)
	if err := h.persistAccount(r.Context(), acct); err != nil {
		log.Printf("admin setup password persist failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	h.clearPublicAdminSessions()
	log.Printf("admin password updated")
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) adminSetupTOTPBegin(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.snapshotAccount()
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "admin_disabled"})
		return
	}
	secret, err := totp.GenerateSecret()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "totp_error"})
		return
	}
	acct.totpPending = secret
	if err := h.persistAccount(r.Context(), acct); err != nil {
		log.Printf("admin setup totp begin persist failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	log.Printf("admin totp enroll pending")
	writeJSON(w, http.StatusOK, map[string]any{
		"secret":        secret,
		"otpauth_url":   totp.OTPAuthURL(secret),
		"totp_enrolled": acct.totpEnrolled && strings.TrimSpace(acct.totpSecret) != "",
	})
}

func (h *Handler) adminSetupTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.snapshotAccount()
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "admin_disabled"})
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	pending := strings.TrimSpace(acct.totpPending)
	if pending == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no_pending"})
		return
	}
	if !totp.Validate(pending, req.Code, h.now()) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad_totp"})
		return
	}
	acct.totpSecret = pending
	acct.totpPending = ""
	acct.totpEnrolled = true
	if err := h.persistAccount(r.Context(), acct); err != nil {
		log.Printf("admin setup totp confirm persist failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	h.clearPublicAdminSessions()
	log.Printf("admin totp enrolled")
	writeJSON(w, http.StatusOK, map[string]any{
		"totp_enrolled": true,
		"status":        "enrolled",
	})
}
