package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fchord/MiniCloudStorage/internal/config"
	"github.com/fchord/MiniCloudStorage/internal/db"
	"github.com/fchord/MiniCloudStorage/internal/shortcode"
	"github.com/fchord/MiniCloudStorage/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

type Handler struct {
	cfg           config.Config
	store         *db.Pool
	filer         *storage.Filer
	chunkGates    *chunkGate
	completeGates *sessionGate
	adminMu       sync.Mutex
	adminTok      map[string]time.Time
	setupTok      map[string]time.Time
	adminFail     map[string]*adminLoginState
	acct          *adminAccount
	nowFn         func() time.Time
}

func New(cfg config.Config, store *db.Pool, filer *storage.Filer) *Handler {
	return &Handler{
		cfg:           cfg,
		store:         store,
		filer:         filer,
		chunkGates:    newChunkGate(),
		completeGates: newSessionGate(),
		adminTok:      make(map[string]time.Time),
		setupTok:      make(map[string]time.Time),
		adminFail:     make(map[string]*adminLoginState),
	}
}

func (h *Handler) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/health", h.health)
		r.Post("/uploads", h.initUpload)
		r.Get("/uploads/{id}", h.getUpload)
		r.Put("/uploads/{id}/chunks/{index}", h.putChunk)
		r.Post("/uploads/{id}/complete", h.completeUpload)
		r.Post("/uploads/{id}/cancel", h.cancelUpload)
		r.Get("/files/{code}", h.getFile)
		r.Get("/files/{code}/download", h.downloadFile)

		r.Route("/admin", func(r chi.Router) {
			r.Post("/login", h.adminLogin)
			r.Get("/login", h.adminLoginLockStatus)
			r.Group(func(r chi.Router) {
				r.Use(h.requireSetupIntranet)
				r.Get("/setup", h.adminSetupStatus)
				r.Post("/setup/login", h.adminSetupLogin)
				r.Group(func(r chi.Router) {
					r.Use(h.requireSetupAuth)
					r.Post("/setup/password", h.adminSetupPassword)
					r.Post("/setup/totp/begin", h.adminSetupTOTPBegin)
					r.Post("/setup/totp/confirm", h.adminSetupTOTPConfirm)
				})
			})
			r.Group(func(r chi.Router) {
				r.Use(h.requireAdmin)
				r.Get("/records", h.adminList)
				r.Delete("/records/{id}", h.adminDelete)
				r.Post("/records/{id}/clear-password", h.adminClearPassword)
			})
		})
	})

	r.Get("/", h.spa().ServeHTTP)
	r.Handle("/*", h.spa())
	return r
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	// Independent of kubelet's probe deadline: always answer quickly so
	// a stuck postgres/filer ping cannot take the LAN vhost NotReady.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "degraded", "db": err.Error()})
		return
	}
	if err := h.filer.Ping(ctx); err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "degraded", "filer": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type initUploadReq struct {
	Filename       string `json:"filename"`
	Size           int64  `json:"size"`
	ContentType    string `json:"content_type"`
	EnablePassword bool   `json:"enable_password"`
}

func (h *Handler) initUpload(w http.ResponseWriter, r *http.Request) {
	var req initUploadReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	req.Filename = strings.TrimSpace(req.Filename)
	if !validFilename(req.Filename) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_filename"})
		return
	}
	if req.Size <= 0 || req.Size > h.cfg.MaxSize {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_size", "max_size": strconv.FormatInt(h.cfg.MaxSize, 10)})
		return
	}
	if req.ContentType == "" {
		req.ContentType = "application/octet-stream"
	}

	totalChunks := int((req.Size + h.cfg.ChunkSize - 1) / h.cfg.ChunkSize)
	sess := db.UploadSession{
		ID:             uuid.New(),
		Filename:       req.Filename,
		ContentType:    req.ContentType,
		TotalSize:      req.Size,
		ChunkSize:      int(h.cfg.ChunkSize),
		TotalChunks:    totalChunks,
		ReceivedChunks: make([]bool, totalChunks),
	}
	if req.EnablePassword {
		pin, err := shortcode.FourDigitPIN()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "password_error"})
			return
		}
		sess.Password = &pin
	}
	if err := h.store.InsertSession(r.Context(), sess); err != nil {
		log.Printf("init upload failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	log.Printf("upload session started id=%s filename=%q size=%d chunks=%d", sess.ID, sess.Filename, sess.TotalSize, sess.TotalChunks)
	writeJSON(w, http.StatusOK, map[string]any{
		"upload_id":    sess.ID.String(),
		"chunk_size":   sess.ChunkSize,
		"total_chunks": sess.TotalChunks,
	})
}

func (h *Handler) putChunk(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_upload_id"})
		return
	}
	index, err := strconv.Atoi(chi.URLParam(r, "index"))
	if err != nil || index < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_chunk_index"})
		return
	}
	sess, err := h.store.GetSession(r.Context(), id)
	if err != nil {
		if db.IsNoRows(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "upload_not_found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	if index >= sess.TotalChunks {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_chunk_index"})
		return
	}
	expected := int64(sess.ChunkSize)
	if index == sess.TotalChunks-1 {
		expected = sess.TotalSize - int64(index)*int64(sess.ChunkSize)
	}
	if r.ContentLength >= 0 && r.ContentLength != expected {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_chunk_size"})
		return
	}
	unlock := h.chunkGates.acquire(sess.ID, index)
	defer unlock()

	objectPath := h.filer.TmpPath(sess.ID.String(), index)
	putCtx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	recvStart := time.Now()
	buf := make([]byte, expected)
	body := &ctxReader{ctx: putCtx, r: io.LimitReader(r.Body, expected)}
	n, recvErr := io.ReadFull(body, buf)
	recvMs := time.Since(recvStart).Milliseconds()
	if recvErr != nil {
		buf = buf[:n]
		logChunkTiming(sess, index, recvMs, 0, recvErr)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "storage_error"})
		return
	}

	filerMs, err := h.filer.Put(putCtx, objectPath, bytes.NewReader(buf), expected, "application/octet-stream", h.cfg.SeaweedTmpTTL)
	logChunkTiming(sess, index, recvMs, filerMs, err)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "storage_error"})
		return
	}
	if _, err := h.store.MarkChunk(r.Context(), sess.ID, index); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func logChunkTiming(sess db.UploadSession, index int, recvMs, filerMs int64, err error) {
	status := "ok"
	errPart := ""
	if err != nil {
		if isTimeoutErr(err) {
			status = "timeout"
		} else {
			status = "err"
		}
		errPart = fmt.Sprintf(" error=%v", err)
	}
	log.Printf("chunk timing session=%s index=%d/%d recv_ms=%d filer_ms=%d total_ms=%d status=%s filename=%q%s",
		sess.ID, index, sess.TotalChunks, recvMs, filerMs, recvMs+filerMs, status, sess.Filename, errPart)
}

func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "timeout") || strings.Contains(s, "deadline exceeded")
}

type completeReq struct {
	EnablePassword bool `json:"enable_password"`
}

func (h *Handler) getUpload(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_upload_id"})
		return
	}
	if file, err := h.store.GetCompletedByID(r.Context(), id); err == nil {
		writeJSON(w, http.StatusOK, h.uploadCompletedJSON(file))
		return
	} else if !db.IsNoRows(err) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	if failed, err := h.store.GetFailedByID(r.Context(), id); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":         "failed",
			"upload_id":      failed.ID.String(),
			"filename":       failed.Filename,
			"reason_code":    failed.ReasonCode,
			"user_cancelled": failed.UserCancelled,
		})
		return
	} else if !db.IsNoRows(err) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	sess, err := h.store.GetSession(r.Context(), id)
	if err != nil {
		if db.IsNoRows(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "upload_not_found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	got := 0
	for _, ok := range sess.ReceivedChunks {
		if ok {
			got++
		}
	}
	status := "uploading"
	if h.completeGates.merging(id) {
		status = "merging"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":           status,
		"upload_id":        sess.ID.String(),
		"filename":         sess.Filename,
		"size_bytes":       sess.TotalSize,
		"received_chunks":  got,
		"received_flags":   sess.ReceivedChunks,
		"total_chunks":     sess.TotalChunks,
		"last_progress_at": sess.LastProgressAt.UTC().Format(time.RFC3339),
	})
}

// ctxReader fails Reads once ctx is done so a stalled client body cannot
// ignore the 90s chunk timeout (io.CopyN does not otherwise check context).
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := c.r.Read(p)
	if n > 0 {
		if cerr := c.ctx.Err(); cerr != nil {
			return n, cerr
		}
	}
	return n, err
}

func (h *Handler) completeUpload(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_upload_id"})
		return
	}
	var req completeReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}

	unlock := h.completeGates.acquire(id)
	released := false
	release := func() {
		if !released {
			released = true
			unlock()
		}
	}
	defer release()

	if file, err := h.store.GetCompletedByID(r.Context(), id); err == nil {
		log.Printf("complete idempotent session=%s code=%s", id, file.Code)
		writeJSON(w, http.StatusOK, h.completeResultJSON(file))
		return
	} else if !db.IsNoRows(err) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	if failed, err := h.store.GetFailedByID(r.Context(), id); err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already_failed", "reason_code": failed.ReasonCode})
		return
	} else if !db.IsNoRows(err) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}

	if h.completeGates.merging(id) {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "merging", "upload_id": id.String()})
		return
	}

	sess, err := h.store.GetSession(r.Context(), id)
	if err != nil {
		if db.IsNoRows(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "upload_not_found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	for i, ok := range sess.ReceivedChunks {
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing_chunks", "missing_index": strconv.Itoa(i)})
			return
		}
	}

	// Detach from the client request so CF ~100s / disconnect cannot abort concat.
	// The merge context is still cancelled by POST /cancel via sessionGate.
	parent, parentCancel := context.WithTimeout(context.Background(), 30*time.Minute)
	mergeCtx, started := h.completeGates.startMerge(id, parent)
	if !started {
		parentCancel()
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "merging", "upload_id": id.String()})
		return
	}
	go func() {
		defer parentCancel()
		defer h.completeGates.finishMerge(id)
		h.runMerge(mergeCtx, sess, req)
	}()
	release()
	log.Printf("merge started session=%s chunks=%d", id, sess.TotalChunks)
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "merging", "upload_id": id.String()})
}

func (h *Handler) runMerge(ctx context.Context, sess db.UploadSession, req completeReq) {
	code, err := h.uniqueCode(ctx)
	if err != nil {
		log.Printf("merge code failed session=%s: %v", sess.ID, err)
		commitCtx, commitCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer commitCancel()
		h.finishMergeFailed(commitCtx, sess, "", ctx)
		return
	}

	src := make([]string, sess.TotalChunks)
	for i := 0; i < sess.TotalChunks; i++ {
		src[i] = h.filer.TmpPath(sess.ID.String(), i)
	}
	dest := h.filer.FilePath(code)
	concatErr := h.filer.Concat(ctx, src, dest, sess.TotalSize, sess.ContentType, h.cfg.SeaweedFileTTL)

	unlock := h.completeGates.acquire(sess.ID)
	defer unlock()

	commitCtx, commitCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer commitCancel()

	if file, lookErr := h.store.GetCompletedByID(commitCtx, sess.ID); lookErr == nil {
		log.Printf("merge lost to completed session=%s code=%s", sess.ID, file.Code)
		return
	}
	if _, lookErr := h.store.GetFailedByID(commitCtx, sess.ID); lookErr == nil {
		log.Printf("merge lost to failed session=%s dest=%s", sess.ID, dest)
		if dest != "" {
			_ = h.filer.Delete(commitCtx, dest)
		}
		return
	}

	if concatErr != nil || ctx.Err() != nil {
		err := concatErr
		if err == nil {
			err = ctx.Err()
		}
		log.Printf("concat failed session=%s: %v", sess.ID, err)
		if dest != "" {
			_ = h.filer.Delete(commitCtx, dest)
		}
		h.finishMergeFailed(commitCtx, sess, err.Error(), ctx)
		return
	}

	if err := h.filer.Delete(commitCtx, strings.TrimRight(h.filer.TmpDir(sess.ID.String()), "/")); err != nil {
		log.Printf("tmp cleanup failed session=%s: %v", sess.ID, err)
	}

	now := time.Now().UTC()
	file := db.Completed{
		ID:          sess.ID,
		Code:        code,
		Filename:    sess.Filename,
		ContentType: sess.ContentType,
		SizeBytes:   sess.TotalSize,
		SeaweedPath: dest,
		CreatedAt:   now,
		ExpiresAt:   now.Add(h.cfg.FileTTL),
	}
	if sess.Password != nil && *sess.Password != "" {
		file.Password = sess.Password
	} else if req.EnablePassword {
		generated, genErr := shortcode.FourDigitPIN()
		if genErr != nil {
			_ = h.filer.Delete(commitCtx, dest)
			h.failUpload(commitCtx, sess, false, db.ReasonCompleteFailed, http.StatusInternalServerError, "password_error")
			return
		}
		file.Password = &generated
	}
	if err := h.store.CompleteSession(commitCtx, file); err != nil {
		if existing, lookErr := h.store.GetCompletedByID(commitCtx, sess.ID); lookErr == nil {
			log.Printf("complete race resolved session=%s code=%s", sess.ID, existing.Code)
			return
		}
		log.Printf("insert completed failed code=%s: %v", code, err)
		_ = h.filer.Delete(commitCtx, dest)
		h.failUpload(commitCtx, sess, false, db.ReasonCompleteFailed, http.StatusInternalServerError, err.Error())
		return
	}

	log.Printf("upload complete code=%s filename=%q size=%d expires_at=%s", code, file.Filename, file.SizeBytes, file.ExpiresAt.Format(time.RFC3339))
}

func (h *Handler) finishMergeFailed(ctx context.Context, sess db.UploadSession, detail string, mergeCtx context.Context) {
	if mergeCtx != nil && errors.Is(mergeCtx.Err(), context.Canceled) {
		h.failUpload(ctx, sess, true, db.ReasonUserCancel, 0, detail)
		return
	}
	status := http.StatusBadGateway
	if detail == "" {
		detail = "merge_failed"
	}
	h.failUpload(ctx, sess, false, db.ReasonCompleteFailed, status, detail)
}

func (h *Handler) completeResultJSON(file db.Completed) map[string]any {
	publicURL := strings.TrimRight(h.cfg.PublicBaseURL, "/") + "/" + file.Code
	resp := map[string]any{
		"status":     "completed",
		"code":       file.Code,
		"url":        publicURL,
		"expires_at": file.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if file.Password != nil && *file.Password != "" {
		resp["password"] = *file.Password
		resp["url_with_password"] = publicURL + "?p=" + url.QueryEscape(*file.Password)
	}
	return resp
}

func (h *Handler) uploadCompletedJSON(file db.Completed) map[string]any {
	resp := h.completeResultJSON(file)
	resp["status"] = "completed"
	resp["upload_id"] = file.ID.String()
	resp["filename"] = file.Filename
	resp["size_bytes"] = file.SizeBytes
	return resp
}

type cancelReq struct {
	ReasonCode string `json:"reason_code"`
}

func (h *Handler) cancelUpload(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_upload_id"})
		return
	}
	reason := strings.TrimSpace(r.URL.Query().Get("reason"))
	var req cancelReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		// Beacon may send text/plain; still honor query/default.
	}
	if strings.TrimSpace(req.ReasonCode) != "" {
		reason = strings.TrimSpace(req.ReasonCode)
	}
	switch reason {
	case db.ReasonPageClose, db.ReasonUserCancel:
	default:
		reason = db.ReasonUserCancel
	}

	unlock := h.completeGates.acquire(id)
	defer unlock()

	workCtx, workCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer workCancel()

	if file, err := h.store.GetCompletedByID(workCtx, id); err == nil {
		resp := h.completeResultJSON(file)
		resp["error"] = "already_completed"
		resp["status"] = "already_completed"
		writeJSON(w, http.StatusConflict, resp)
		return
	} else if !db.IsNoRows(err) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}

	// pagehide/beacon must not abort an in-flight merge.
	if reason == db.ReasonPageClose && h.completeGates.merging(id) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	h.completeGates.cancelMerge(id)

	if _, err := h.store.GetFailedByID(workCtx, id); err == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	} else if !db.IsNoRows(err) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}

	sess, err := h.store.GetSession(workCtx, id)
	if err != nil {
		if db.IsNoRows(err) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	h.failUpload(workCtx, sess, true, reason, 0, "")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getFile(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	if !validCode(code) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	file, err := h.store.GetCompleted(r.Context(), code)
	if err == nil {
		if status, body := h.authorize(file.Password, passwordFromRequest(r)); status != 0 {
			writeJSON(w, status, body)
			return
		}
		writeJSON(w, http.StatusOK, completedMetaJSON(file))
		return
	}
	if !db.IsNoRows(err) {
		log.Printf("get completed: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	expired, err := h.store.GetExpired(r.Context(), code)
	if err == nil {
		writeJSON(w, http.StatusGone, expiredMetaJSON(expired))
		return
	}
	if !db.IsNoRows(err) {
		log.Printf("get expired: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
}

func (h *Handler) downloadFile(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	if !validCode(code) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	file, err := h.store.GetCompleted(r.Context(), code)
	if err != nil {
		if db.IsNoRows(err) {
			if _, expErr := h.store.GetExpired(r.Context(), code); expErr == nil {
				writeJSON(w, http.StatusGone, map[string]string{"error": "expired", "status": "expired"})
				return
			}
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	if status, body := h.authorize(file.Password, passwordFromRequest(r)); status != 0 {
		writeJSON(w, status, body)
		return
	}

	body, size, contentType, err := h.filer.Get(r.Context(), file.SeaweedPath)
	if err != nil {
		log.Printf("download filer get failed code=%s: %v", file.Code, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "storage_error"})
		return
	}
	defer body.Close()

	if contentType == "" {
		contentType = file.ContentType
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", contentDisposition(file.Filename))
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(file.SizeBytes, 10))
	}
	log.Printf("download start code=%s filename=%q", file.Code, file.Filename)
	if _, err := io.Copy(w, body); err != nil {
		log.Printf("download copy failed code=%s: %v", file.Code, err)
	}
}

func (h *Handler) authorize(stored *string, provided string) (int, map[string]string) {
	if stored == nil || *stored == "" {
		return 0, nil
	}
	if provided == "" {
		return http.StatusUnauthorized, map[string]string{"error": "need_password"}
	}
	if provided != *stored {
		log.Printf("bad password for protected file")
		return http.StatusForbidden, map[string]string{"error": "bad_password"}
	}
	return 0, nil
}

func (h *Handler) uniqueCode(ctx context.Context) (string, error) {
	for i := 0; i < 8; i++ {
		code, err := shortcode.New(8)
		if err != nil {
			return "", err
		}
		exists, err := h.store.CodeExists(ctx, code)
		if err != nil {
			return "", err
		}
		if !exists {
			return code, nil
		}
	}
	return "", fmt.Errorf("could not allocate unique code")
}

func (h *Handler) spa() http.Handler {
	dir := h.cfg.StaticDir
	fileServer := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		clean := path.Clean(r.URL.Path)
		full := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(clean, "/")))
		if info, err := os.Stat(full); err == nil && !info.IsDir() {
			if cc := staticCacheControl(clean); cc != "" {
				w.Header().Set("Cache-Control", cc)
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		index := filepath.Join(dir, "index.html")
		if _, err := os.Stat(index); err != nil {
			http.Error(w, "frontend not built", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		http.ServeFile(w, r, index)
	})
}

// App entry JS/HTML must not be cached: a Flutter service worker or heuristic
// browser cache otherwise keeps an old main.dart.js after dist updates.
func staticCacheControl(p string) string {
	lower := strings.ToLower(p)
	if strings.Contains(lower, "/canvaskit/") || strings.Contains(lower, "/assets/") {
		return ""
	}
	base := path.Base(lower)
	switch {
	case strings.HasSuffix(base, ".js"), strings.HasSuffix(base, ".html"),
		base == "manifest.json", base == "version.json":
		return "no-cache, must-revalidate"
	}
	return ""
}

func (h *Handler) failUpload(ctx context.Context, sess db.UploadSession, userCancelled bool, reason string, httpStatus int, detail string) {
	tmp := strings.TrimRight(h.filer.TmpDir(sess.ID.String()), "/")
	if err := h.filer.Delete(ctx, tmp); err != nil {
		extra := "filer tmp delete: " + err.Error()
		if detail != "" {
			detail = detail + "; " + extra
		} else {
			detail = extra
		}
		log.Printf("fail-upload filer delete id=%s: %v", sess.ID, err)
	}
	var statusPtr *int
	if httpStatus > 0 {
		s := httpStatus
		statusPtr = &s
	}
	if err := h.store.FailSession(ctx, db.FailSessionParams{
		Session:       sess,
		UserCancelled: userCancelled,
		ReasonCode:    reason,
		HTTPStatus:    statusPtr,
		ReasonDetail:  detail,
	}); err != nil {
		log.Printf("fail session db id=%s: %v", sess.ID, err)
	}
}

func (h *Handler) ExpireDue(ctx context.Context) error {
	now := time.Now().UTC()
	files, err := h.store.DueCompleted(ctx, now)
	if err != nil {
		return err
	}
	moved := 0
	for _, file := range files {
		if err := h.filer.Delete(ctx, file.SeaweedPath); err != nil {
			log.Printf("expire filer delete failed code=%s path=%s: %v", file.Code, file.SeaweedPath, err)
			continue
		}
		if err := h.store.MoveCompletedToExpired(ctx, file, ""); err != nil {
			log.Printf("expire db move failed code=%s: %v", file.Code, err)
			continue
		}
		moved++
		log.Printf("expired file moved code=%s filename=%q", file.Code, file.Filename)
	}
	if moved > 0 || len(files) > 0 {
		log.Printf("expire-due scanned=%d moved=%d", len(files), moved)
	}
	return nil
}

func (h *Handler) Cleanup(ctx context.Context) error {
	if err := h.ExpireDue(ctx); err != nil {
		return err
	}

	stale, err := h.store.StaleSessions(ctx, time.Now().UTC().Add(-h.cfg.SessionTTL))
	if err != nil {
		return err
	}
	for _, sess := range stale {
		h.failUpload(ctx, sess, false, db.ReasonIdleTimeout, 0, "no chunk progress for 24h")
		log.Printf("stale upload session moved to failed id=%s filename=%q", sess.ID, sess.Filename)
	}

	cutoff := time.Now().UTC().Add(-h.cfg.RecordTTL)
	nFail, err := h.store.DeleteOldFailed(ctx, cutoff)
	if err != nil {
		return err
	}
	nExp, err := h.store.DeleteOldExpired(ctx, cutoff)
	if err != nil {
		return err
	}
	log.Printf("cleanup finished stale_sessions=%d old_failed_deleted=%d old_expired_deleted=%d", len(stale), nFail, nExp)
	return nil
}

func passwordFromRequest(r *http.Request) string {
	if p := strings.TrimSpace(r.URL.Query().Get("p")); p != "" {
		return p
	}
	if p := strings.TrimSpace(r.URL.Query().Get("password")); p != "" {
		return p
	}
	return strings.TrimSpace(r.Header.Get("X-Password"))
}

func completedMetaJSON(file db.Completed) map[string]any {
	remaining := time.Until(file.ExpiresAt)
	if remaining < 0 {
		remaining = 0
	}
	days := int((remaining + 24*time.Hour - time.Nanosecond) / (24 * time.Hour))
	return map[string]any{
		"status":         "ready",
		"code":           file.Code,
		"filename":       file.Filename,
		"size_bytes":     file.SizeBytes,
		"content_type":   file.ContentType,
		"created_at":     file.CreatedAt.UTC().Format(time.RFC3339),
		"expires_at":     file.ExpiresAt.UTC().Format(time.RFC3339),
		"days_remaining": days,
		"has_password":   file.Password != nil && *file.Password != "",
	}
}

func expiredMetaJSON(file db.Expired) map[string]any {
	return map[string]any{
		"status":         "expired",
		"error":          "expired",
		"code":           file.Code,
		"filename":       file.Filename,
		"size_bytes":     file.SizeBytes,
		"content_type":   file.ContentType,
		"created_at":     file.CreatedAt.UTC().Format(time.RFC3339),
		"expires_at":     file.ExpiresAt.UTC().Format(time.RFC3339),
		"expired_at":     file.ExpiredAt.UTC().Format(time.RFC3339),
		"days_remaining": 0,
		"has_password":   false,
	}
}

func validFilename(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > 512 {
		return false
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return false
	}
	return true
}

func validCode(code string) bool {
	if len(code) != 8 {
		return false
	}
	for _, c := range code {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func contentDisposition(filename string) string {
	escaped := strings.ReplaceAll(filename, `"`, `%22`)
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, escaped, url.PathEscape(filename))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
