package configs

import (
	"os"
	"path/filepath"
	"strconv"
)

var (
	dataDir         = "."
	browserPoolSize = 1
)

func GetContentMaxRunes() int {
	v := os.Getenv("XHS_MCP_CONTENT_MAX_RUNES")
	if v == "" {
		return 1000
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 1000
	}
	return n
}

func InitDataDir(dir string) {
	if dir == "" {
		dataDir = "."
		return
	}
	dataDir = dir
}

func GetDataDir() string {
	return dataDir
}

func ResolveDataPath(p string) string {
	if p == "" {
		return dataDir
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dataDir, p)
}

func InitBrowserPoolSize(n int) {
	if n < 1 {
		browserPoolSize = 1
		return
	}
	browserPoolSize = n
}

func GetBrowserPoolSize() int {
	return browserPoolSize
}

func LoadRuntimeFromEnv() {
	if v := os.Getenv("XHS_MCP_DATA_DIR"); v != "" {
		InitDataDir(v)
	}
	if v := os.Getenv("XHS_MCP_BROWSER_POOL_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			InitBrowserPoolSize(n)
		}
	}
}
