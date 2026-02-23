package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPrepareBatchPostForQueue_TitleTooLong(t *testing.T) {
	s := &AppServer{}
	post := BatchPost{Title: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Content: "c", Images: []string{"/tmp/a.jpg"}}
	_, err := s.prepareBatchPostForQueue(context.Background(), post)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestPrepareBatchPostForQueue_ContentTooLong(t *testing.T) {
	t.Setenv("XHS_MCP_CONTENT_MAX_RUNES", "5")

	s := &AppServer{}
	post := BatchPost{Title: "t", Content: "123456", Images: []string{"/tmp/a.jpg"}}
	_, err := s.prepareBatchPostForQueue(context.Background(), post)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestPrepareBatchPostForQueue_ScheduleTooSoon(t *testing.T) {
	s := &AppServer{}
	post := BatchPost{Title: "t", Content: "c", Images: []string{"/tmp/a.jpg"}, ScheduleAt: time.Now().Add(30 * time.Minute).Format(time.RFC3339)}
	_, err := s.prepareBatchPostForQueue(context.Background(), post)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestPrepareBatchPostForQueue_LocalImageOk(t *testing.T) {
	pngBase64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO0V9b0AAAAASUVORK5CYII="
	pngBytes, err := base64.StdEncoding.DecodeString(pngBase64)
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}

	dir := t.TempDir()
	imgPath := filepath.Join(dir, "a.png")
	if err := os.WriteFile(imgPath, pngBytes, 0644); err != nil {
		t.Fatalf("write image: %v", err)
	}

	s := &AppServer{}
	post := BatchPost{Title: "t", Content: "c", Images: []string{imgPath}}
	prepared, err := s.prepareBatchPostForQueue(context.Background(), post)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(prepared.Images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(prepared.Images))
	}
	abs, _ := filepath.Abs(imgPath)
	if prepared.Images[0] != abs {
		t.Fatalf("expected abs path %q, got %q", abs, prepared.Images[0])
	}
}
