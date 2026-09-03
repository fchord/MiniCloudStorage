package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type Filer struct {
	base       string
	prefix     string
	collection string
	client     *http.Client
}

func NewFiler(base, prefix, collection string) *Filer {
	return &Filer{
		base:       strings.TrimRight(base, "/"),
		prefix:     strings.TrimRight(prefix, "/"),
		collection: collection,
		client: &http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				MaxIdleConns:        64,
				MaxIdleConnsPerHost: 16,
				IdleConnTimeout:     90 * time.Second,
				ForceAttemptHTTP2:   true,
			},
		},
	}
}

func (f *Filer) Prefix() string { return f.prefix }

func (f *Filer) TmpPath(uploadID string, index int) string {
	return f.prefix + "/tmp/" + uploadID + "/" + fmt.Sprintf("%d", index)
}

func (f *Filer) TmpDir(uploadID string) string {
	return f.prefix + "/tmp/" + uploadID + "/"
}

func (f *Filer) FilePath(code string) string {
	return f.prefix + "/files/" + code
}

// Put writes body to Filer. filerMs is HTTP POST duration only (client.Do),
// including timeouts; it is 0 if the POST never started.
func (f *Filer) Put(ctx context.Context, objectPath string, body io.Reader, size int64, contentType, ttl string) (filerMs int64, err error) {
	if !f.allowed(objectPath) {
		return 0, fmt.Errorf("refusing to write outside prefix %s: %s", f.prefix, objectPath)
	}
	if size >= 0 && size <= 16<<20 {
		return f.putBuffered(ctx, objectPath, body, size, ttl)
	}
	return f.putStream(ctx, objectPath, body, ttl)
}

func (f *Filer) putBuffered(ctx context.Context, objectPath string, body io.Reader, size int64, ttl string) (int64, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", path.Base(objectPath))
	if err != nil {
		return 0, err
	}
	if _, err := io.CopyN(part, body, size); err != nil && err != io.EOF {
		return 0, err
	}
	if err := mw.Close(); err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.base+path.Clean(objectPath), bytes.NewReader(buf.Bytes()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Seaweed-Collection", f.collection)
	if ttl != "" {
		req.Header.Set("Seaweed-TTL", ttl)
	}
	req.ContentLength = int64(buf.Len())
	return f.doPut(req, objectPath)
}

func (f *Filer) putStream(ctx context.Context, objectPath string, body io.Reader, ttl string) (int64, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		var err error
		defer func() {
			_ = mw.Close()
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			_ = pw.Close()
		}()
		part, e := mw.CreateFormFile("file", path.Base(objectPath))
		if e != nil {
			err = e
			return
		}
		if _, e = io.Copy(part, body); e != nil {
			err = e
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.base+path.Clean(objectPath), pr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Seaweed-Collection", f.collection)
	if ttl != "" {
		req.Header.Set("Seaweed-TTL", ttl)
	}
	return f.doPut(req, objectPath)
}

func (f *Filer) doPut(req *http.Request, objectPath string) (int64, error) {
	start := time.Now()
	resp, err := f.client.Do(req)
	filerMs := time.Since(start).Milliseconds()
	if err != nil {
		return filerMs, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return filerMs, fmt.Errorf("filer put %s: %s %s", objectPath, resp.Status, strings.TrimSpace(string(msg)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return filerMs, nil
}

func (f *Filer) Get(ctx context.Context, objectPath string) (io.ReadCloser, int64, string, error) {
	if !f.allowed(objectPath) {
		return nil, 0, "", fmt.Errorf("refusing to read outside prefix %s: %s", f.prefix, objectPath)
	}
	u := f.base + path.Clean(objectPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, "", err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, 0, "", fmt.Errorf("filer get %s: %s %s", objectPath, resp.Status, strings.TrimSpace(string(msg)))
	}
	return resp.Body, resp.ContentLength, resp.Header.Get("Content-Type"), nil
}

func (f *Filer) Delete(ctx context.Context, objectPath string) error {
	if !f.allowed(objectPath) {
		return fmt.Errorf("refusing to delete outside prefix %s: %s", f.prefix, objectPath)
	}
	u := f.base + path.Clean(objectPath) + "?recursive=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("filer delete %s: %s %s", objectPath, resp.Status, strings.TrimSpace(string(msg)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (f *Filer) Concat(ctx context.Context, srcPaths []string, dest string, totalSize int64, contentType, ttl string) error {
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		defer pw.Close()
		for _, p := range srcPaths {
			if err := ctx.Err(); err != nil {
				pw.CloseWithError(err)
				errCh <- err
				return
			}
			body, _, _, err := f.Get(ctx, p)
			if err != nil {
				pw.CloseWithError(err)
				errCh <- err
				return
			}
			_, copyErr := io.Copy(pw, body)
			body.Close()
			if copyErr != nil {
				pw.CloseWithError(copyErr)
				errCh <- copyErr
				return
			}
		}
		errCh <- nil
	}()
	_, putErr := f.Put(ctx, dest, pr, totalSize, contentType, ttl)
	readErr := <-errCh
	if putErr != nil {
		return putErr
	}
	return readErr
}

func (f *Filer) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	u, err := url.Parse(f.base + "/")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("filer ping: %s", resp.Status)
	}
	return nil
}

type dirListing struct {
	Entries []struct {
		FullPath string `json:"FullPath"`
		Name     string `json:"Name"`
	} `json:"Entries"`
}

func (f *Filer) ListNames(ctx context.Context, dir string) ([]string, error) {
	if !f.allowed(dir) {
		return nil, fmt.Errorf("refusing to list outside prefix %s: %s", f.prefix, dir)
	}
	u := f.base + path.Clean(dir) + "/?pretty=y"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("filer list %s: %s %s", dir, resp.Status, strings.TrimSpace(string(msg)))
	}
	var listing dirListing
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(listing.Entries))
	for _, e := range listing.Entries {
		out = append(out, e.Name)
	}
	return out, nil
}

func (f *Filer) allowed(objectPath string) bool {
	clean := path.Clean(objectPath)
	return clean == f.prefix || strings.HasPrefix(clean, f.prefix+"/")
}
