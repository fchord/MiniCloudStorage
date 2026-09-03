package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/fchord/MiniCloudStorage/internal/db"
	"github.com/fchord/MiniCloudStorage/internal/totp"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type adminAccount struct {
	passwordHash string
	totpSecret   string
	totpPending  string
	totpEnrolled bool
}

const adminTokenTTL = 2 * time.Hour

const (
	adminLoginFailLockAfter = 3
	adminLoginLockMax       = 15 * time.Minute
	adminFailPruneAfter     = 30 * time.Minute
)

type adminLoginState struct {
	failures    int
	lockedUntil time.Time
	lastSeen    time.Time
}

func (h *Handler) now() time.Time {
	if h.nowFn != nil {
		return h.nowFn()
	}
	return time.Now()
}

func (h *Handler) adminLoginLockStatus(w http.ResponseWriter, r *http.Request) {
	retryAfter, locked := h.adminLoginLocked(clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"locked":        locked,
		"retry_after":   retryAfter,
		"totp_enrolled": h.totpEnrolled(),
	})
}

func (h *Handler) adminLogin(w http.ResponseWriter, r *http.Request) {
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
	if !acct.totpEnrolled || strings.TrimSpace(acct.totpSecret) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "totp_not_enrolled"})
		return
	}
	if !adminHashOK(req.Password, acct.passwordHash) {
		h.recordAdminLoginFailure(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad_password"})
		return
	}
	if !totp.Validate(acct.totpSecret, req.TOTP, h.now()) {
		h.recordAdminLoginFailure(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad_totp"})
		return
	}
	h.clearAdminLoginFailure(ip)
	token, err := h.issueNamedToken(&h.adminTok)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "token_error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (h *Handler) requireAdmin(next http.Handler) http.Handler {
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
		exp, ok := h.adminTok[token]
		h.adminMu.Unlock()
		if !ok || h.now().After(exp) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "need_auth"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) pruneAdminLocked() {
	now := h.now()
	for tok, exp := range h.adminTok {
		if now.After(exp) {
			delete(h.adminTok, tok)
		}
	}
	for tok, exp := range h.setupTok {
		if now.After(exp) {
			delete(h.setupTok, tok)
		}
	}
	for ip, st := range h.adminFail {
		if st == nil {
			delete(h.adminFail, ip)
			continue
		}
		if now.Before(st.lockedUntil) {
			continue
		}
		if now.Sub(st.lastSeen) > adminFailPruneAfter {
			delete(h.adminFail, ip)
		}
	}
}

func (h *Handler) adminLoginLocked(ip string) (retryAfter int, locked bool) {
	now := h.now()
	h.adminMu.Lock()
	defer h.adminMu.Unlock()
	h.pruneAdminLocked()
	st := h.adminFail[ip]
	if st == nil || !now.Before(st.lockedUntil) {
		return 0, false
	}
	sec := int(st.lockedUntil.Sub(now).Seconds())
	if sec < 1 {
		sec = 1
	}
	return sec, true
}

func (h *Handler) recordAdminLoginFailure(ip string) {
	if ip == "" {
		ip = "unknown"
	}
	now := h.now()
	h.adminMu.Lock()
	defer h.adminMu.Unlock()
	if h.adminFail == nil {
		h.adminFail = make(map[string]*adminLoginState)
	}
	h.pruneAdminLocked()
	st := h.adminFail[ip]
	if st == nil {
		st = &adminLoginState{}
		h.adminFail[ip] = st
	}
	if now.Before(st.lockedUntil) {
		return
	}
	st.failures++
	st.lastSeen = now
	if st.failures >= adminLoginFailLockAfter {
		st.lockedUntil = now.Add(adminLoginLockDuration(st.failures))
	}
}

func (h *Handler) clearAdminLoginFailure(ip string) {
	h.adminMu.Lock()
	defer h.adminMu.Unlock()
	delete(h.adminFail, ip)
}

func adminLoginLockDuration(failures int) time.Duration {
	shift := failures - adminLoginFailLockAfter
	if shift < 0 {
		return 0
	}
	mins := 1 << shift
	d := time.Duration(mins) * time.Minute
	if d > adminLoginLockMax {
		return adminLoginLockMax
	}
	return d
}

func clientIP(r *http.Request) string {
	if ip := parseClientIP(r.Header.Get("CF-Connecting-IP")); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first := strings.TrimSpace(strings.Split(xff, ",")[0])
		if ip := parseClientIP(first); ip != "" {
			return ip
		}
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

func parseClientIP(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(v); err == nil {
		v = host
	}
	ip := net.ParseIP(v)
	if ip == nil {
		return ""
	}
	return ip.String()
}

func (h *Handler) adminList(w http.ResponseWriter, r *http.Request) {
	tab := strings.TrimSpace(r.URL.Query().Get("tab"))
	if tab == "" {
		tab = db.TabInProgress
	}
	items, err := h.store.ListAdmin(r.Context(), tab)
	if err != nil {
		log.Printf("admin list: %v", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_tab"})
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		path := it.StoragePath
		switch tab {
		case db.TabInProgress:
			path = strings.TrimRight(h.filer.TmpDir(it.ID.String()), "/")
		case db.TabCancelled, db.TabOtherFailed, db.TabExpired:
			path = ""
		}
		row := map[string]any{
			"id":           it.ID.String(),
			"kind":         it.Kind,
			"code":         it.Code,
			"filename":     it.Filename,
			"size_bytes":   it.SizeBytes,
			"storage_path": path,
			"created_at":   it.CreatedAt.UTC().Format(time.RFC3339),
			"password":     deref(it.Password),
			"reason_code":  it.ReasonCode,
		}
		if it.ExpiresAt != nil {
			row["expires_at"] = it.ExpiresAt.UTC().Format(time.RFC3339)
		} else {
			row["expires_at"] = nil
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tab": tab, "items": out})
}

func (h *Handler) adminDelete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_id"})
		return
	}
	kind, err := h.store.LocateID(r.Context(), id)
	if err != nil {
		if db.IsNoRows(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	ctx := r.Context()
	switch kind {
	case db.TabInProgress:
		_ = h.filer.Delete(ctx, strings.TrimRight(h.filer.TmpDir(id.String()), "/"))
		if err := h.store.DeleteSessionRow(ctx, id); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
			return
		}
	case db.TabCompleted:
		file, err := h.store.GetCompletedByID(ctx, id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
			return
		}
		if file.SeaweedPath != "" {
			if err := h.filer.Delete(ctx, file.SeaweedPath); err != nil {
				log.Printf("admin delete filer code=%s: %v", file.Code, err)
			}
		}
		if err := h.store.DeleteCompletedRow(ctx, id); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
			return
		}
	case "failed":
		if err := h.store.DeleteFailedRow(ctx, id); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
			return
		}
	case db.TabExpired:
		exp, err := h.store.GetExpiredByID(ctx, id)
		if err == nil && exp.SeaweedPath != "" {
			_ = h.filer.Delete(ctx, exp.SeaweedPath)
		}
		if err := h.store.DeleteExpiredRow(ctx, id); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
			return
		}
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) adminClearPassword(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_id"})
		return
	}
	kind, err := h.store.ClearPassword(r.Context(), id)
	if err != nil {
		if db.IsNoRows(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no_password"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared", "kind": kind})
}

func bearerToken(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return strings.TrimSpace(r.Header.Get("X-Admin-Token"))
}

func adminHashOK(got, hash string) bool {
	if strings.TrimSpace(hash) == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(got)) == nil
}

func (h *Handler) snapshotAccount() (adminAccount, bool) {
	h.adminMu.Lock()
	defer h.adminMu.Unlock()
	if h.acct == nil {
		return adminAccount{}, false
	}
	return *h.acct, true
}

func (h *Handler) replaceAccount(a adminAccount) {
	h.adminMu.Lock()
	cp := a
	h.acct = &cp
	h.adminMu.Unlock()
}

func (h *Handler) persistAccount(ctx context.Context, a adminAccount) error {
	if h.store != nil {
		if err := h.store.UpsertAdminAccount(ctx, db.AdminAccount{
			PasswordHash: a.passwordHash,
			TOTPSecret:   a.totpSecret,
			TOTPPending:  a.totpPending,
			TOTPEnrolled: a.totpEnrolled,
		}); err != nil {
			return err
		}
	}
	h.replaceAccount(a)
	return nil
}

func (h *Handler) adminEnabled() bool {
	a, ok := h.snapshotAccount()
	return ok && strings.TrimSpace(a.passwordHash) != ""
}

func (h *Handler) totpEnrolled() bool {
	a, ok := h.snapshotAccount()
	return ok && a.totpEnrolled && strings.TrimSpace(a.totpSecret) != ""
}

func (h *Handler) issueNamedToken(dst *map[string]time.Time) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	h.adminMu.Lock()
	defer h.adminMu.Unlock()
	h.pruneAdminLocked()
	if *dst == nil {
		*dst = make(map[string]time.Time)
	}
	(*dst)[token] = h.now().Add(adminTokenTTL)
	return token, nil
}

func (h *Handler) clearPublicAdminSessions() {
	h.adminMu.Lock()
	h.adminTok = make(map[string]time.Time)
	h.adminMu.Unlock()
}

func (h *Handler) EnsureAdmin(ctx context.Context) error {
	if h.store == nil {
		return nil
	}
	row, err := h.store.GetAdminAccount(ctx)
	if err == nil {
		h.replaceAccount(adminAccount{
			passwordHash: row.PasswordHash,
			totpSecret:   row.TOTPSecret,
			totpPending:  row.TOTPPending,
			totpEnrolled: row.TOTPEnrolled,
		})
		return nil
	}
	if !db.IsNoRows(err) {
		return err
	}
	seed := strings.TrimSpace(h.cfg.AdminPassword)
	if seed == "" {
		log.Printf("admin_account empty; public admin disabled until seeded")
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(seed), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := h.persistAccount(ctx, adminAccount{passwordHash: string(hash)}); err != nil {
		return err
	}
	log.Printf("admin_account seeded totp_enrolled=false")
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
