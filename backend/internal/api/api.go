package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
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
	cfg   config.Config
	store *db.Pool
	filer *storage.Filer
}

func New(cfg config.Config, store *db.Pool, filer *storage.Filer) *Handler {
	return &Handler{cfg: cfg, store: store, filer: filer}
}

func (h *Handler) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/health", h.health)
		r.Post("/uploads", h.initUpload)
		r.Put("/uploads/{id}/chunks/{index}", h.putChunk)
		r.Post("/uploads/{id}/complete", h.completeUpload)
		r.Get("/files/{code}", h.getFile)
		r.Get("/files/{code}/download", h.downloadFile)
	})

	r.Get("/", h.spa().ServeHTTP)
	r.Handle("/*", h.spa())
	return r
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := h.store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "degraded", "db": err.Error()})
		return
	}
	if err := h.filer.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "degraded", "filer": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type initUploadReq struct {
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
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
	objectPath := h.filer.TmpPath(sess.ID.String(), index)
	if err := h.filer.Put(r.Context(), objectPath, io.LimitReader(r.Body, expected), expected, "application/octet-stream", h.cfg.SeaweedTmpTTL); err != nil {
		log.Printf("chunk put failed session=%s index=%d: %v", sess.ID, index, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "storage_error"})
		return
	}
	if _, err := h.store.MarkChunk(r.Context(), sess.ID, index); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	log.Printf("chunk stored session=%s index=%d/%d", sess.ID, index, sess.TotalChunks-1)
	w.WriteHeader(http.StatusNoContent)
}

type completeReq struct {
	EnablePassword bool `json:"enable_password"`
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

	code, err := h.uniqueCode(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "code_error"})
		return
	}

	src := make([]string, sess.TotalChunks)
	for i := 0; i < sess.TotalChunks; i++ {
		src[i] = h.filer.TmpPath(sess.ID.String(), i)
	}
	dest := h.filer.FilePath(code)
	if err := h.filer.Concat(r.Context(), src, dest, sess.TotalSize, sess.ContentType, h.cfg.SeaweedFileTTL); err != nil {
		log.Printf("concat failed session=%s: %v", sess.ID, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "storage_error"})
		return
	}
	if err := h.filer.Delete(r.Context(), strings.TrimRight(h.filer.TmpDir(sess.ID.String()), "/")); err != nil {
		log.Printf("tmp cleanup failed session=%s: %v", sess.ID, err)
	}

	now := time.Now().UTC()
	file := db.File{
		Code:        code,
		Filename:    sess.Filename,
		ContentType: sess.ContentType,
		SizeBytes:   sess.TotalSize,
		SeaweedPath: dest,
		CreatedAt:   now,
		ExpiresAt:   now.Add(h.cfg.FileTTL),
		Status:      "ready",
	}
	var pin string
	if req.EnablePassword {
		pin, err = shortcode.FourDigitPIN()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "password_error"})
			return
		}
		file.Password = &pin
	}
	if err := h.store.InsertFile(r.Context(), file); err != nil {
		log.Printf("insert file failed code=%s: %v", code, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	_ = h.store.DeleteSession(r.Context(), sess.ID)

	publicURL := strings.TrimRight(h.cfg.PublicBaseURL, "/") + "/" + code
	log.Printf("upload complete code=%s filename=%q size=%d password=%s expires_at=%s", code, file.Filename, file.SizeBytes, pin, file.ExpiresAt.Format(time.RFC3339))

	resp := map[string]any{
		"code":       code,
		"url":        publicURL,
		"expires_at": file.ExpiresAt.Format(time.RFC3339),
	}
	if pin != "" {
		resp["password"] = pin
		resp["url_with_password"] = publicURL + "?p=" + url.QueryEscape(pin)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) getFile(w http.ResponseWriter, r *http.Request) {
	file, err := h.lookupFile(r.Context(), chi.URLParam(r, "code"))
	if err != nil {
		writeFileErr(w, err)
		return
	}
	if status, body := h.authorize(file, passwordFromRequest(r)); status != 0 {
		writeJSON(w, status, body)
		return
	}
	writeJSON(w, http.StatusOK, fileMetaJSON(file))
}

func (h *Handler) downloadFile(w http.ResponseWriter, r *http.Request) {
	file, err := h.lookupFile(r.Context(), chi.URLParam(r, "code"))
	if err != nil {
		writeFileErr(w, err)
		return
	}
	if status, body := h.authorize(file, passwordFromRequest(r)); status != 0 {
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
	log.Printf("download start code=%s filename=%q password=%s", file.Code, file.Filename, deref(file.Password))
	if _, err := io.Copy(w, body); err != nil {
		log.Printf("download copy failed code=%s: %v", file.Code, err)
	}
}

func (h *Handler) lookupFile(ctx context.Context, code string) (db.File, error) {
	if !validCode(code) {
		return db.File{}, errNotFound
	}
	file, err := h.store.GetFile(ctx, code)
	if err != nil {
		if db.IsNoRows(err) {
			return db.File{}, errNotFound
		}
		return db.File{}, err
	}
	if file.Status != "ready" || time.Now().UTC().After(file.ExpiresAt) {
		return db.File{}, errExpired
	}
	return file, nil
}

func (h *Handler) authorize(file db.File, provided string) (int, map[string]string) {
	if file.Password == nil || *file.Password == "" {
		return 0, nil
	}
	if provided == "" {
		return http.StatusUnauthorized, map[string]string{"error": "need_password"}
	}
	if provided != *file.Password {
		log.Printf("bad password code=%s provided=%s expected=%s", file.Code, provided, *file.Password)
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
			fileServer.ServeHTTP(w, r)
			return
		}
		index := filepath.Join(dir, "index.html")
		if _, err := os.Stat(index); err != nil {
			http.Error(w, "frontend not built", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, index)
	})
}

func (h *Handler) Cleanup(ctx context.Context) error {
	now := time.Now().UTC()
	files, err := h.store.ExpiredFiles(ctx, now)
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := h.filer.Delete(ctx, file.SeaweedPath); err != nil {
			log.Printf("cleanup filer delete failed code=%s path=%s: %v", file.Code, file.SeaweedPath, err)
			continue
		}
		if err := h.store.MarkExpiredAndDeleteRow(ctx, file.Code); err != nil {
			log.Printf("cleanup db delete failed code=%s: %v", file.Code, err)
			continue
		}
		log.Printf("expired file removed code=%s password=%s", file.Code, deref(file.Password))
	}

	stale, err := h.store.StaleSessions(ctx, now.Add(-h.cfg.SessionTTL))
	if err != nil {
		return err
	}
	for _, sess := range stale {
		if err := h.filer.Delete(ctx, strings.TrimRight(h.filer.TmpDir(sess.ID.String()), "/")); err != nil {
			log.Printf("stale session filer delete failed id=%s: %v", sess.ID, err)
		}
		if err := h.store.DeleteSession(ctx, sess.ID); err != nil {
			log.Printf("stale session db delete failed id=%s: %v", sess.ID, err)
			continue
		}
		log.Printf("stale upload session removed id=%s filename=%q", sess.ID, sess.Filename)
	}
	log.Printf("cleanup finished expired_files=%d stale_sessions=%d", len(files), len(stale))
	return nil
}

var (
	errNotFound = errors.New("not_found")
	errExpired  = errors.New("expired")
)

func writeFileErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
	case errors.Is(err, errExpired):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "expired"})
	default:
		log.Printf("file lookup error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
	}
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

func fileMetaJSON(file db.File) map[string]any {
	remaining := time.Until(file.ExpiresAt)
	if remaining < 0 {
		remaining = 0
	}
	days := int((remaining + 24*time.Hour - time.Nanosecond) / (24 * time.Hour))
	return map[string]any{
		"code":            file.Code,
		"filename":        file.Filename,
		"size_bytes":      file.SizeBytes,
		"content_type":    file.ContentType,
		"created_at":      file.CreatedAt.UTC().Format(time.RFC3339),
		"expires_at":      file.ExpiresAt.UTC().Format(time.RFC3339),
		"days_remaining":  days,
		"has_password":    file.Password != nil && *file.Password != "",
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

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
