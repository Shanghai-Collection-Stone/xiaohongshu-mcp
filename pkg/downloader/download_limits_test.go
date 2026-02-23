package downloader

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestImageDownloader_DownloadImage_RespectsMaxBytes(t *testing.T) {
	pngBase64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO0V9b0AAAAASUVORK5CYII="
	pngBytes, err := base64.StdEncoding.DecodeString(pngBase64)
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(pngBytes)
	}))
	defer srv.Close()

	t.Setenv("XHS_MCP_IMAGE_MAX_BYTES", "10")

	d := NewImageDownloader(t.TempDir())
	_, err = d.DownloadImage(srv.URL)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestImageDownloader_DownloadImage_AllowsSmallImage(t *testing.T) {
	pngBase64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO0V9b0AAAAASUVORK5CYII="
	pngBytes, err := base64.StdEncoding.DecodeString(pngBase64)
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(pngBytes)
	}))
	defer srv.Close()

	t.Setenv("XHS_MCP_IMAGE_MAX_BYTES", "1048576")

	d := NewImageDownloader(t.TempDir())
	path, err := d.DownloadImage(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("downloaded file missing: %v", err)
	}
}
