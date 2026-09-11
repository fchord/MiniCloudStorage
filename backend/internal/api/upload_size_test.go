package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fchord/MiniCloudStorage/internal/config"
)

func postInitUpload(h *Handler, size int64) *httptest.ResponseRecorder {
	body, err := json.Marshal(map[string]any{
		"filename": "size-check.bin",
		"size":     size,
	})
	if err != nil {
		panic(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.initUpload(rec, req)
	return rec
}

func TestInitUploadRejectsOverMaxSize(t *testing.T) {
	h := &Handler{cfg: config.Config{MaxSize: 4 << 30, ChunkSize: 8 << 20}}
	rec := postInitUpload(h, (4<<30)+1)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["error"] != "invalid_size" {
		t.Fatalf("error = %v, want invalid_size", out["error"])
	}
	if out["max_size"] != "4294967296" {
		t.Fatalf("max_size = %v, want 4294967296", out["max_size"])
	}
}

func TestInitUploadRejectsZeroSize(t *testing.T) {
	h := &Handler{cfg: config.Config{MaxSize: 4 << 30, ChunkSize: 8 << 20}}
	rec := postInitUpload(h, 0)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["error"] != "invalid_size" {
		t.Fatalf("error = %v, want invalid_size", out["error"])
	}
}
