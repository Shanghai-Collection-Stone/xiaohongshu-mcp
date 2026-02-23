package configs

import (
	"os"
	"path/filepath"
	"strconv"
)

const (
	ImagesDir = "xiaohongshu_images"
)

func GetImagesPath() string {
	return filepath.Join(os.TempDir(), ImagesDir)
}

func GetImageMaxBytes() int64 {
	v := os.Getenv("XHS_MCP_IMAGE_MAX_BYTES")
	if v == "" {
		return 20 * 1024 * 1024
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 20 * 1024 * 1024
	}
	return n
}
