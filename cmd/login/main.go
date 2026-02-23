package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"strings"

	"github.com/go-rod/rod"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/browser"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
	"github.com/xpzouying/xiaohongshu-mcp/modules/cookiestore"
	"github.com/xpzouying/xiaohongshu-mcp/modules/ippool"
	"github.com/xpzouying/xiaohongshu-mcp/modules/userpool"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

func main() {
	var (
		binPath string
		dataDir string
		account string
		index   int
	)
	flag.StringVar(&binPath, "bin", "", "浏览器二进制文件路径")
	flag.StringVar(&dataDir, "data_dir", "", "数据目录（users.json/ip.txt/cookies等）")
	flag.StringVar(&account, "account", "", "账号（users.json中的account）")
	flag.IntVar(&index, "index", -1, "用户序号（users.json中的索引，从0开始）")
	flag.Parse()

	if dataDir == "" {
		dataDir = os.Getenv("XHS_MCP_DATA_DIR")
	}
	if dataDir == "" {
		dataDir = "."
	}
	if binPath == "" {
		binPath = os.Getenv("ROD_BROWSER_BIN")
	}

	up, err := userpool.NewManager(dataDir)
	if err != nil {
		logrus.Fatalf("failed to load users.json: %v", err)
	}
	ipPool, err := ippool.NewPool(dataDir)
	if err != nil {
		logrus.Fatalf("failed to load ip.txt: %v", err)
	}
	store := cookiestore.NewStore(dataDir)

	ips := ipPool.All()
	if len(ips) > 0 {
		if err := up.EnsureSequentialIPRefs(len(ips)); err != nil {
			logrus.Fatalf("failed to ensure sequential ip refs: %v", err)
		}
	}

	account = strings.TrimSpace(account)
	if account == "" {
		if index >= 0 {
			u, err := up.Resolve("", &index)
			if err != nil {
				logrus.Fatalf("failed to resolve user by index: %v", err)
			}
			account = u.Account
		} else {
			u, err := up.Resolve("", nil)
			if err == nil {
				account = u.Account
			}
			if strings.TrimSpace(account) == "" {
				account = "default"
			}
		}
	}

	u, err := up.Resolve(account, nil)
	if err != nil {
		u = userpool.User{Account: account, Enabled: true}
	}
	cookieAbs, cookieRel := store.CookiePathFor(account, u.CookieFile)
	_ = store.EnsureDir(cookieAbs)

	proxy := ""
	if len(ips) > 0 {
		if v, ok := ipPool.Resolve(u.IPRef); ok {
			proxy = v
		} else {
			if idx, ok := up.IndexOfAccount(account); ok {
				if idx < 0 || idx >= len(ips) {
					proxy = ""
				} else {
					proxy = ips[idx]
					_, _ = up.UpsertIPRef(account, idx)
				}
			} else {
				proxy = ""
			}
		}
	}

	// 登录的时候，需要界面，所以不能无头模式
	b, err := browser.NewBrowser(
		false,
		browser.WithBinPath(binPath),
		browser.WithCookiesPath(cookieAbs),
		browser.WithProxyURL(proxy),
	)
	if err != nil {
		logrus.Fatalf("failed to launch browser: %v", err)
	}
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewLogin(page)

	status, err := action.CheckLoginStatus(context.Background())
	if err != nil {
		logrus.Fatalf("failed to check login status: %v", err)
	}

	logrus.Infof("当前登录状态: %v", status)

	if status {
		return
	}

	// 开始登录流程
	logrus.Info("开始登录流程...")
	if err = action.Login(context.Background()); err != nil {
		logrus.Fatalf("登录失败: %v", err)
	} else {
		if err := saveCookies(page, cookieAbs); err != nil {
			logrus.Fatalf("failed to save cookies: %v", err)
		}
		_, _ = up.UpsertCookie(account, cookieRel)
	}

	// 再次检查登录状态确认成功
	status, err = action.CheckLoginStatus(context.Background())
	if err != nil {
		logrus.Fatalf("failed to check login status after login: %v", err)
	}

	if status {
		logrus.Info("登录成功！")
	} else {
		logrus.Error("登录流程完成但仍未登录")
	}

}

func saveCookies(page *rod.Page, cookiePath string) error {
	cks, err := page.Browser().GetCookies()
	if err != nil {
		return err
	}

	data, err := json.Marshal(cks)
	if err != nil {
		return err
	}

	cookieLoader := cookies.NewLoadCookie(cookiePath)
	return cookieLoader.SaveCookies(data)
}
