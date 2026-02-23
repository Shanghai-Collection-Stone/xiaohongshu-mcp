package cookiestore

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/xpzouying/xiaohongshu-mcp/cookies"
	"github.com/xpzouying/xiaohongshu-mcp/modules/userpool"
)

type Store struct {
	dataDir string
}

func NewStore(dataDir string) *Store {
	return &Store{dataDir: dataDir}
}

func (s *Store) CookiePathFor(account string, cookieFileHint string) (absPath string, relPath string) {
	account = strings.TrimSpace(account)
	if account == "" {
		account = "default"
	}

	if cookieFileHint != "" {
		if filepath.IsAbs(cookieFileHint) {
			return cookieFileHint, cookieFileHint
		}
		return filepath.Join(s.dataDir, cookieFileHint), cookieFileHint
	}

	safe := userpool.SafeAccount(account)
	defaultRel := filepath.ToSlash(filepath.Join("cookies", safe+".json"))
	defaultAbs := filepath.Join(s.dataDir, defaultRel)

	if account == "default" {
		legacy := cookies.GetCookiesFilePath()
		if legacy != "" {
			if _, err := os.Stat(legacy); err == nil {
				if _, err2 := os.Stat(defaultAbs); err2 != nil {
					return legacy, legacy
				}
			}
		}
	}

	return defaultAbs, defaultRel
}

func (s *Store) EnsureDir(absPath string) error {
	dir := filepath.Dir(absPath)
	return os.MkdirAll(dir, 0755)
}
