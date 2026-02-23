package browser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
)

type browserConfig struct {
	binPath     string
	cookiesPath string
	proxyURL    string
}

type Option func(*browserConfig)

func WithBinPath(binPath string) Option {
	return func(c *browserConfig) {
		c.binPath = binPath
	}
}

func WithCookiesPath(path string) Option {
	return func(c *browserConfig) {
		c.cookiesPath = path
	}
}

func WithProxyURL(proxyURL string) Option {
	return func(c *browserConfig) {
		c.proxyURL = proxyURL
	}
}

type Browser struct {
	browser  *rod.Browser
	launcher *launcher.Launcher
}

var launchMu sync.Mutex

func detectChromeBinPath() string {
	paths := []string{}
	if v := strings.TrimSpace(os.Getenv("ROD_BROWSER_BIN")); v != "" {
		paths = append(paths, v)
	}
	if v := strings.TrimSpace(os.Getenv("CHROME_BIN")); v != "" {
		paths = append(paths, v)
	}
	if v := strings.TrimSpace(os.Getenv("GOOGLE_CHROME_BIN")); v != "" {
		paths = append(paths, v)
	}

	programFiles := os.Getenv("PROGRAMFILES")
	if programFiles == "" {
		programFiles = `C:\\Program Files`
	}
	programFilesX86 := os.Getenv("PROGRAMFILES(X86)")
	if programFilesX86 == "" {
		programFilesX86 = `C:\\Program Files (x86)`
	}
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData != "" {
		paths = append(paths, filepath.Join(localAppData, "Google", "Chrome", "Application", "chrome.exe"))
	}
	paths = append(paths,
		filepath.Join(programFiles, "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(programFilesX86, "Google", "Chrome", "Application", "chrome.exe"),
	)

	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func NewBrowser(headless bool, options ...Option) (*Browser, error) {
	cfg := &browserConfig{}
	for _, opt := range options {
		opt(cfg)
	}

	userAgent := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
	if ua := strings.TrimSpace(os.Getenv("XHS_MCP_USER_AGENT")); ua != "" {
		userAgent = ua
	}

	l := launcher.New().
		Headless(headless).
		Set("--no-sandbox").
		Set("user-agent", userAgent)

	if cfg.proxyURL != "" {
		l = l.Set("proxy-server", cfg.proxyURL)
	}
	binPath := strings.TrimSpace(cfg.binPath)
	if binPath == "" {
		binPath = detectChromeBinPath()
	}
	if binPath != "" {
		l = l.Bin(binPath)
	}

	var url string
	var err error
	shouldSerializeLaunch := strings.TrimSpace(cfg.binPath) == "" && binPath == ""
	if shouldSerializeLaunch {
		launchMu.Lock()
		url, err = l.Launch()
		launchMu.Unlock()
	} else {
		url, err = l.Launch()
	}
	if err != nil {
		if strings.Contains(err.Error(), "expected only one dir in") {
			return nil, fmt.Errorf("launch browser failed: %w (rod browser cache is broken, delete %s and retry, or set ROD_BROWSER_BIN)", err, filepath.Join(os.Getenv("APPDATA"), "rod", "browser"))
		}
		return nil, fmt.Errorf("launch browser failed: %w", err)
	}

	b := rod.New().ControlURL(url)
	if err := b.Connect(); err != nil {
		l.Cleanup()
		return nil, fmt.Errorf("connect browser failed: %w", err)
	}

	cookiesPath := cfg.cookiesPath
	if cookiesPath == "" {
		cookiesPath = cookies.GetCookiesFilePath()
	}
	if cookiesPath != "" {
		cookieLoader := cookies.NewLoadCookie(cookiesPath)
		if data, err := cookieLoader.LoadCookies(); err == nil {
			if len(data) == 0 {
				return &Browser{browser: b, launcher: l}, nil
			}
			var cks []*proto.NetworkCookie
			if err := json.Unmarshal(data, &cks); err == nil {
				if err := b.SetCookies(proto.CookiesToParams(cks)); err != nil {
					logrus.Warnf("failed to set cookies: %v", err)
				}
				logrus.Debugf("loaded cookies")
			} else {
				logrus.Warnf("failed to unmarshal cookies: %v", err)
			}
		} else {
			logrus.Warnf("failed to load cookies: %v", err)
		}
	}

	return &Browser{browser: b, launcher: l}, nil
}

func (b *Browser) Close() {
	if b == nil {
		return
	}
	if b.browser != nil {
		_ = b.browser.Close()
	}
	if b.launcher != nil {
		b.launcher.Cleanup()
	}
}

func (b *Browser) NewPage() *rod.Page {
	return stealth.MustPage(b.browser)
}
