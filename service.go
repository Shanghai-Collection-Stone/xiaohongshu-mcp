package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/browser"
	"github.com/xpzouying/xiaohongshu-mcp/configs"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
	"github.com/xpzouying/xiaohongshu-mcp/modules/cookiestore"
	"github.com/xpzouying/xiaohongshu-mcp/modules/userpool"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/downloader"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/xhsutil"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

// XiaohongshuService 小红书业务服务
type XiaohongshuService struct {
	runtime     *Runtime
	cookieStore *cookiestore.Store
}

// NewXiaohongshuService 创建小红书服务实例
func NewXiaohongshuService(runtime *Runtime) *XiaohongshuService {
	return &XiaohongshuService{
		runtime:     runtime,
		cookieStore: cookiestore.NewStore(configs.GetDataDir()),
	}
}

func (s *XiaohongshuService) normalizeAccount(account string) string {
	account = strings.TrimSpace(account)
	return account
}

func (s *XiaohongshuService) effectiveAccount(account string) string {
	account = s.normalizeAccount(account)
	if account != "" {
		return account
	}
	if s.runtime != nil && s.runtime.UserPool != nil {
		accounts := s.runtime.UserPool.EnabledAccounts()
		if len(accounts) > 0 {
			return accounts[0]
		}
		u, err := s.runtime.UserPool.Resolve("", nil)
		if err == nil {
			if v := strings.TrimSpace(u.Account); v != "" {
				return v
			}
		}
	}
	return "default"
}

func (s *XiaohongshuService) resolveUser(account string) userpool.User {
	account = s.effectiveAccount(account)
	if s.runtime == nil || s.runtime.UserPool == nil {
		return userpool.User{Account: account, Enabled: true}
	}
	u, err := s.runtime.UserPool.Resolve(account, nil)
	if err != nil {
		return userpool.User{Account: account, Enabled: true}
	}
	return u
}

func (s *XiaohongshuService) resolveCookieAndProxy(account string) (string, string, error) {
	account = s.effectiveAccount(account)
	u := s.resolveUser(account)

	cookieStore := s.cookieStore
	if cookieStore == nil {
		cookieStore = cookiestore.NewStore(configs.GetDataDir())
	}
	cookiePath, _ := cookieStore.CookiePathFor(account, u.CookieFile)

	if s.runtime == nil || s.runtime.IPPool == nil {
		return cookiePath, "", nil
	}
	ips := s.runtime.IPPool.All()
	if len(ips) == 0 {
		return cookiePath, "", nil
	}

	if proxy, ok := s.runtime.IPPool.Resolve(u.IPRef); ok {
		return cookiePath, proxy, nil
	}

	if s.runtime.UserPool != nil {
		if idx, ok := s.runtime.UserPool.IndexOfAccount(account); ok {
			if idx < 0 || idx >= len(ips) {
				return cookiePath, "", nil
			}
			proxy := ips[idx]
			_, _ = s.runtime.UserPool.UpsertIPRef(account, idx)
			return cookiePath, proxy, nil
		}
	}

	return cookiePath, "", nil
}

func (s *XiaohongshuService) withBrowserPageForAccount(ctx context.Context, account string, fn func(*rod.Page) error) (err error) {
	account = s.effectiveAccount(account)

	if s.runtime != nil {
		if err := s.runtime.AcquireBrowser(ctx); err != nil {
			return err
		}
		defer s.runtime.ReleaseBrowser()
	}

	if s.runtime != nil {
		if mu, err := s.runtime.AccountLock(account); err == nil {
			mu.Lock()
			defer mu.Unlock()
		}
	}

	var lastErr error
	for attempt := range 2 {
		cookiePath, proxyURL, err := s.resolveCookieAndProxy(account)
		if err != nil {
			return err
		}
		b, err := browser.NewBrowser(
			configs.IsHeadless(),
			browser.WithBinPath(configs.GetBinPath()),
			browser.WithCookiesPath(cookiePath),
			browser.WithProxyURL(proxyURL),
		)
		if err != nil {
			return err
		}

		page := b.NewPage()
		lastErr = func() (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panic: %v", r)
				}
			}()
			return fn(page)
		}()

		_ = page.Close()
		b.Close()

		if lastErr == nil {
			return nil
		}
		if attempt == 0 && isRodSessionNotFound(lastErr) {
			logrus.WithFields(logrus.Fields{"account": account}).Warn("rod session lost, retry once")
			time.Sleep(800 * time.Millisecond)
			continue
		}
		return lastErr
	}
	return lastErr
}

func isRodSessionNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "session with given id not found") || strings.Contains(s, "-32001")
}

// PublishRequest 发布请求
type PublishRequest struct {
	Title      string   `json:"title" binding:"required"`
	Content    string   `json:"content" binding:"required"`
	Images     []string `json:"images" binding:"required,min=1"`
	Tags       []string `json:"tags,omitempty"`
	Location   string   `json:"location,omitempty"`
	ScheduleAt string   `json:"schedule_at,omitempty"` // 定时发布时间，ISO8601格式，为空则立即发布
}

// LoginStatusResponse 登录状态响应
type LoginStatusResponse struct {
	IsLoggedIn bool   `json:"is_logged_in"`
	Username   string `json:"username,omitempty"`
}

// LoginQrcodeResponse 登录扫码二维码
type LoginQrcodeResponse struct {
	Timeout    string `json:"timeout"`
	IsLoggedIn bool   `json:"is_logged_in"`
	Img        string `json:"img,omitempty"`
}

// PublishResponse 发布响应
type PublishResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Images  int    `json:"images"`
	Status  string `json:"status"`
	PostID  string `json:"post_id,omitempty"`
}

// PublishVideoRequest 发布视频请求（仅支持本地单个视频文件）
type PublishVideoRequest struct {
	Title      string   `json:"title" binding:"required"`
	Content    string   `json:"content" binding:"required"`
	Video      string   `json:"video" binding:"required"`
	Tags       []string `json:"tags,omitempty"`
	Location   string   `json:"location,omitempty"`
	ScheduleAt string   `json:"schedule_at,omitempty"` // 定时发布时间，ISO8601格式，为空则立即发布
}

// PublishVideoResponse 发布视频响应
type PublishVideoResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Video   string `json:"video"`
	Status  string `json:"status"`
	PostID  string `json:"post_id,omitempty"`
}

// FeedsListResponse Feeds列表响应
type FeedsListResponse struct {
	Feeds []xiaohongshu.Feed `json:"feeds"`
	Count int                `json:"count"`
}

// UserProfileResponse 用户主页响应
type UserProfileResponse struct {
	UserBasicInfo xiaohongshu.UserBasicInfo      `json:"userBasicInfo"`
	Interactions  []xiaohongshu.UserInteractions `json:"interactions"`
	Feeds         []xiaohongshu.Feed             `json:"feeds"`
}

func (s *XiaohongshuService) DeleteCookies(ctx context.Context) error {
	return s.DeleteCookiesForAccount(ctx, "")
}

func (s *XiaohongshuService) DeleteCookiesForAccount(ctx context.Context, account string) error {
	account = s.effectiveAccount(account)
	u := s.resolveUser(account)

	cookieStore := s.cookieStore
	if cookieStore == nil {
		cookieStore = cookiestore.NewStore(configs.GetDataDir())
	}
	cookiePath, _ := cookieStore.CookiePathFor(account, u.CookieFile)
	cookieLoader := cookies.NewLoadCookie(cookiePath)
	return cookieLoader.DeleteCookies()
}

func (s *XiaohongshuService) CheckLoginStatus(ctx context.Context) (*LoginStatusResponse, error) {
	return s.CheckLoginStatusForAccount(ctx, "")
}

func (s *XiaohongshuService) CheckLoginStatusForAccount(ctx context.Context, account string) (*LoginStatusResponse, error) {
	var isLoggedIn bool
	err := s.withBrowserPageForAccount(ctx, account, func(page *rod.Page) error {
		loginAction := xiaohongshu.NewLogin(page)
		v, err := loginAction.CheckLoginStatus(ctx)
		if err != nil {
			return err
		}
		isLoggedIn = v
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &LoginStatusResponse{IsLoggedIn: isLoggedIn, Username: configs.Username}, nil
}

func (s *XiaohongshuService) GetLoginQrcode(ctx context.Context) (*LoginQrcodeResponse, error) {
	return s.GetLoginQrcodeForAccount(ctx, "")
}

func (s *XiaohongshuService) GetLoginQrcodeForAccount(ctx context.Context, account string) (*LoginQrcodeResponse, error) {
	account = s.effectiveAccount(account)

	if s.runtime != nil {
		if err := s.runtime.AcquireBrowser(ctx); err != nil {
			return nil, err
		}
	}
	if s.runtime != nil {
		if mu, err := s.runtime.AccountLock(account); err == nil {
			mu.Lock()
			defer mu.Unlock()
		}
	}

	cookiePath, proxyURL, err := s.resolveCookieAndProxy(account)
	if err != nil {
		if s.runtime != nil {
			s.runtime.ReleaseBrowser()
		}
		return nil, err
	}
	if s.cookieStore != nil {
		_ = s.cookieStore.EnsureDir(cookiePath)
	}

	b, err := browser.NewBrowser(
		configs.IsHeadless(),
		browser.WithBinPath(configs.GetBinPath()),
		browser.WithCookiesPath(cookiePath),
		browser.WithProxyURL(proxyURL),
	)
	if err != nil {
		if s.runtime != nil {
			s.runtime.ReleaseBrowser()
		}
		return nil, err
	}
	page := b.NewPage()

	deferFunc := func() {
		_ = page.Close()
		b.Close()
		if s.runtime != nil {
			s.runtime.ReleaseBrowser()
		}
	}

	loginAction := xiaohongshu.NewLogin(page)

	img, loggedIn, err := loginAction.FetchQrcodeImage(ctx)
	if err != nil || loggedIn {
		defer deferFunc()
	}
	if err != nil {
		return nil, err
	}

	timeout := 4 * time.Minute

	if !loggedIn {
		go func() {
			ctxTimeout, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			defer deferFunc()

			if loginAction.WaitForLogin(ctxTimeout) {
				if er := saveCookies(page, cookiePath); er != nil {
					logrus.Errorf("failed to save cookies: %v", er)
					return
				}
				if s.runtime != nil && s.runtime.UserPool != nil {
					cookieStore := s.cookieStore
					if cookieStore == nil {
						cookieStore = cookiestore.NewStore(configs.GetDataDir())
					}
					_, rel := cookieStore.CookiePathFor(account, "")
					_, _ = s.runtime.UserPool.UpsertCookie(account, rel)
				}
			}
		}()
	}

	return &LoginQrcodeResponse{
		Timeout: func() string {
			if loggedIn {
				return "0s"
			}
			return timeout.String()
		}(),
		Img:        img,
		IsLoggedIn: loggedIn,
	}, nil
}

// PublishContent 发布内容
func (s *XiaohongshuService) PublishContent(ctx context.Context, req *PublishRequest) (*PublishResponse, error) {
	return s.PublishContentForAccount(ctx, "", req)
}

func (s *XiaohongshuService) PublishContentForAccount(ctx context.Context, account string, req *PublishRequest) (*PublishResponse, error) {
	// 验证标题长度（小红书限制：最大20个字）
	if xhsutil.CalcTitleLength(req.Title) > 20 {
		return nil, fmt.Errorf("标题长度超过限制")
	}

	// 处理图片：下载URL图片或使用本地路径
	imagePaths, err := s.processImages(req.Images)
	if err != nil {
		return nil, err
	}

	// 解析定时发布时间
	var scheduleTime *time.Time
	if req.ScheduleAt != "" {
		logrus.WithFields(logrus.Fields{
			"account":     s.effectiveAccount(account),
			"schedule_at": req.ScheduleAt,
		}).Info("publish: schedule requested")

		t, err := time.Parse(time.RFC3339, req.ScheduleAt)
		if err != nil {
			return nil, fmt.Errorf("定时发布时间格式错误，请使用 ISO8601 格式: %v", err)
		}

		// 校验定时发布时间范围：1小时至14天
		now := time.Now()
		minTime := now.Add(1 * time.Hour)
		maxTime := now.Add(14 * 24 * time.Hour)

		if t.Before(minTime) {
			return nil, fmt.Errorf("定时发布时间必须至少在1小时后，当前设置: %s，最早可选: %s",
				t.Format("2006-01-02 15:04"), minTime.Format("2006-01-02 15:04"))
		}
		if t.After(maxTime) {
			return nil, fmt.Errorf("定时发布时间不能超过14天，当前设置: %s，最晚可选: %s",
				t.Format("2006-01-02 15:04"), maxTime.Format("2006-01-02 15:04"))
		}

		scheduleTime = &t
		logrus.WithFields(logrus.Fields{
			"account":     s.effectiveAccount(account),
			"schedule_at": req.ScheduleAt,
			"schedule_ui": t.Format("2006-01-02 15:04"),
		}).Info("publish: schedule validated")
	}

	// 构建发布内容
	content := xiaohongshu.PublishImageContent{
		Title:        req.Title,
		Content:      req.Content,
		Tags:         req.Tags,
		ImagePaths:   imagePaths,
		Location:     req.Location,
		ScheduleTime: scheduleTime,
	}

	// 执行发布
	if err := s.publishContentForAccount(ctx, account, content); err != nil {
		logrus.Errorf("发布内容失败: title=%s %v", content.Title, err)
		return nil, err
	}

	response := &PublishResponse{
		Title:   req.Title,
		Content: req.Content,
		Images:  len(imagePaths),
		Status:  "发布完成",
	}

	return response, nil
}

// processImages 处理图片列表，支持URL下载和本地路径
func (s *XiaohongshuService) processImages(images []string) ([]string, error) {
	processor := downloader.NewImageProcessor()
	return processor.ProcessImages(images)
}

func (s *XiaohongshuService) publishContentForAccount(ctx context.Context, account string, content xiaohongshu.PublishImageContent) error {
	return s.withBrowserPageForAccount(ctx, account, func(page *rod.Page) error {
		action, err := xiaohongshu.NewPublishImageAction(page)
		if err != nil {
			return err
		}
		return action.Publish(ctx, content)
	})
}

// PublishVideo 发布视频（本地文件）
func (s *XiaohongshuService) PublishVideo(ctx context.Context, req *PublishVideoRequest) (*PublishVideoResponse, error) {
	return s.PublishVideoForAccount(ctx, "", req)
}

func (s *XiaohongshuService) PublishVideoForAccount(ctx context.Context, account string, req *PublishVideoRequest) (*PublishVideoResponse, error) {
	// 标题长度校验（小红书限制：最大20个字）
	if xhsutil.CalcTitleLength(req.Title) > 20 {
		return nil, fmt.Errorf("标题长度超过限制")
	}

	// 本地视频文件校验
	if req.Video == "" {
		return nil, fmt.Errorf("必须提供本地视频文件")
	}
	if _, err := os.Stat(req.Video); err != nil {
		return nil, fmt.Errorf("视频文件不存在或不可访问: %v", err)
	}

	// 解析定时发布时间
	var scheduleTime *time.Time
	if req.ScheduleAt != "" {
		t, err := time.Parse(time.RFC3339, req.ScheduleAt)
		if err != nil {
			return nil, fmt.Errorf("定时发布时间格式错误，请使用 ISO8601 格式: %v", err)
		}

		// 校验定时发布时间范围：1小时至14天
		now := time.Now()
		minTime := now.Add(1 * time.Hour)
		maxTime := now.Add(14 * 24 * time.Hour)

		if t.Before(minTime) {
			return nil, fmt.Errorf("定时发布时间必须至少在1小时后，当前设置: %s，最早可选: %s",
				t.Format("2006-01-02 15:04"), minTime.Format("2006-01-02 15:04"))
		}
		if t.After(maxTime) {
			return nil, fmt.Errorf("定时发布时间不能超过14天，当前设置: %s，最晚可选: %s",
				t.Format("2006-01-02 15:04"), maxTime.Format("2006-01-02 15:04"))
		}

		scheduleTime = &t
		logrus.Infof("设置定时发布时间: %s", t.Format("2006-01-02 15:04"))
	}

	// 构建发布内容
	content := xiaohongshu.PublishVideoContent{
		Title:        req.Title,
		Content:      req.Content,
		Tags:         req.Tags,
		VideoPath:    req.Video,
		Location:     req.Location,
		ScheduleTime: scheduleTime,
	}

	// 执行发布
	if err := s.publishVideoForAccount(ctx, account, content); err != nil {
		return nil, err
	}

	resp := &PublishVideoResponse{
		Title:   req.Title,
		Content: req.Content,
		Video:   req.Video,
		Status:  "发布完成",
	}
	return resp, nil
}

func (s *XiaohongshuService) publishVideoForAccount(ctx context.Context, account string, content xiaohongshu.PublishVideoContent) error {
	return s.withBrowserPageForAccount(ctx, account, func(page *rod.Page) error {
		action, err := xiaohongshu.NewPublishVideoAction(page)
		if err != nil {
			return err
		}
		return action.PublishVideo(ctx, content)
	})
}

// ListFeeds 获取Feeds列表
func (s *XiaohongshuService) ListFeeds(ctx context.Context) (*FeedsListResponse, error) {
	return s.ListFeedsForAccount(ctx, "")
}

func (s *XiaohongshuService) ListFeedsForAccount(ctx context.Context, account string) (*FeedsListResponse, error) {
	var feeds []xiaohongshu.Feed
	err := s.withBrowserPageForAccount(ctx, account, func(page *rod.Page) error {
		action := xiaohongshu.NewFeedsListAction(page)
		v, err := action.GetFeedsList(ctx)
		if err != nil {
			logrus.Errorf("获取 Feeds 列表失败: %v", err)
			return err
		}
		feeds = v
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &FeedsListResponse{Feeds: feeds, Count: len(feeds)}, nil
}

func (s *XiaohongshuService) SearchFeeds(ctx context.Context, keyword string, filters ...xiaohongshu.FilterOption) (*FeedsListResponse, error) {
	return s.SearchFeedsForAccount(ctx, "", keyword, filters...)
}

func (s *XiaohongshuService) SearchFeedsForAccount(ctx context.Context, account string, keyword string, filters ...xiaohongshu.FilterOption) (*FeedsListResponse, error) {
	var feeds []xiaohongshu.Feed
	err := s.withBrowserPageForAccount(ctx, account, func(page *rod.Page) error {
		action := xiaohongshu.NewSearchAction(page)
		v, err := action.Search(ctx, keyword, filters...)
		if err != nil {
			return err
		}
		feeds = v
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &FeedsListResponse{Feeds: feeds, Count: len(feeds)}, nil
}

// GetFeedDetail 获取Feed详情
func (s *XiaohongshuService) GetFeedDetail(ctx context.Context, feedID, xsecToken string, loadAllComments bool) (*FeedDetailResponse, error) {
	return s.GetFeedDetailWithConfigForAccount(ctx, "", feedID, xsecToken, loadAllComments, xiaohongshu.DefaultCommentLoadConfig())
}

// GetFeedDetailWithConfig 使用配置获取Feed详情
func (s *XiaohongshuService) GetFeedDetailWithConfig(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config xiaohongshu.CommentLoadConfig) (*FeedDetailResponse, error) {
	return s.GetFeedDetailWithConfigForAccount(ctx, "", feedID, xsecToken, loadAllComments, config)
}

func (s *XiaohongshuService) GetFeedDetailWithConfigForAccount(ctx context.Context, account string, feedID, xsecToken string, loadAllComments bool, config xiaohongshu.CommentLoadConfig) (*FeedDetailResponse, error) {
	var result *xiaohongshu.FeedDetailResponse
	err := s.withBrowserPageForAccount(ctx, account, func(page *rod.Page) error {
		action := xiaohongshu.NewFeedDetailAction(page)
		v, err := action.GetFeedDetailWithConfig(ctx, feedID, xsecToken, loadAllComments, config)
		if err != nil {
			return err
		}
		result = v
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &FeedDetailResponse{FeedID: feedID, Data: result}, nil
}

// UserProfile 获取用户信息
func (s *XiaohongshuService) UserProfile(ctx context.Context, userID, xsecToken string) (*UserProfileResponse, error) {
	return s.UserProfileForAccount(ctx, "", userID, xsecToken)

}

func (s *XiaohongshuService) UserProfileForAccount(ctx context.Context, account string, userID, xsecToken string) (*UserProfileResponse, error) {
	var result *xiaohongshu.UserProfileResponse
	err := s.withBrowserPageForAccount(ctx, account, func(page *rod.Page) error {
		action := xiaohongshu.NewUserProfileAction(page)
		v, err := action.UserProfile(ctx, userID, xsecToken)
		if err != nil {
			return err
		}
		result = v
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &UserProfileResponse{UserBasicInfo: result.UserBasicInfo, Interactions: result.Interactions, Feeds: result.Feeds}, nil
}

// PostCommentToFeed 发表评论到Feed
func (s *XiaohongshuService) PostCommentToFeed(ctx context.Context, feedID, xsecToken, content string) (*PostCommentResponse, error) {
	return s.PostCommentToFeedForAccount(ctx, "", feedID, xsecToken, content)
}

func (s *XiaohongshuService) PostCommentToFeedForAccount(ctx context.Context, account string, feedID, xsecToken, content string) (*PostCommentResponse, error) {
	err := s.withBrowserPageForAccount(ctx, account, func(page *rod.Page) error {
		action := xiaohongshu.NewCommentFeedAction(page)
		return action.PostComment(ctx, feedID, xsecToken, content)
	})
	if err != nil {
		return nil, err
	}
	return &PostCommentResponse{FeedID: feedID, Success: true, Message: "评论发表成功"}, nil
}

// LikeFeed 点赞笔记
func (s *XiaohongshuService) LikeFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	return s.LikeFeedForAccount(ctx, "", feedID, xsecToken)
}

func (s *XiaohongshuService) LikeFeedForAccount(ctx context.Context, account string, feedID, xsecToken string) (*ActionResult, error) {
	err := s.withBrowserPageForAccount(ctx, account, func(page *rod.Page) error {
		action := xiaohongshu.NewLikeAction(page)
		return action.Like(ctx, feedID, xsecToken)
	})
	if err != nil {
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "点赞成功或已点赞"}, nil
}

// UnlikeFeed 取消点赞笔记
func (s *XiaohongshuService) UnlikeFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	return s.UnlikeFeedForAccount(ctx, "", feedID, xsecToken)
}

func (s *XiaohongshuService) UnlikeFeedForAccount(ctx context.Context, account string, feedID, xsecToken string) (*ActionResult, error) {
	err := s.withBrowserPageForAccount(ctx, account, func(page *rod.Page) error {
		action := xiaohongshu.NewLikeAction(page)
		return action.Unlike(ctx, feedID, xsecToken)
	})
	if err != nil {
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "取消点赞成功或未点赞"}, nil
}

// FavoriteFeed 收藏笔记
func (s *XiaohongshuService) FavoriteFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	return s.FavoriteFeedForAccount(ctx, "", feedID, xsecToken)
}

func (s *XiaohongshuService) FavoriteFeedForAccount(ctx context.Context, account string, feedID, xsecToken string) (*ActionResult, error) {
	err := s.withBrowserPageForAccount(ctx, account, func(page *rod.Page) error {
		action := xiaohongshu.NewFavoriteAction(page)
		return action.Favorite(ctx, feedID, xsecToken)
	})
	if err != nil {
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "收藏成功或已收藏"}, nil
}

// UnfavoriteFeed 取消收藏笔记
func (s *XiaohongshuService) UnfavoriteFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	return s.UnfavoriteFeedForAccount(ctx, "", feedID, xsecToken)
}

func (s *XiaohongshuService) UnfavoriteFeedForAccount(ctx context.Context, account string, feedID, xsecToken string) (*ActionResult, error) {
	err := s.withBrowserPageForAccount(ctx, account, func(page *rod.Page) error {
		action := xiaohongshu.NewFavoriteAction(page)
		return action.Unfavorite(ctx, feedID, xsecToken)
	})
	if err != nil {
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "取消收藏成功或未收藏"}, nil
}

// ReplyCommentToFeed 回复指定评论
func (s *XiaohongshuService) ReplyCommentToFeed(ctx context.Context, feedID, xsecToken, commentID, userID, content string) (*ReplyCommentResponse, error) {
	return s.ReplyCommentToFeedForAccount(ctx, "", feedID, xsecToken, commentID, userID, content)
}

func (s *XiaohongshuService) ReplyCommentToFeedForAccount(ctx context.Context, account string, feedID, xsecToken, commentID, userID, content string) (*ReplyCommentResponse, error) {
	err := s.withBrowserPageForAccount(ctx, account, func(page *rod.Page) error {
		action := xiaohongshu.NewCommentFeedAction(page)
		return action.ReplyToComment(ctx, feedID, xsecToken, commentID, userID, content)
	})
	if err != nil {
		return nil, err
	}
	return &ReplyCommentResponse{FeedID: feedID, TargetCommentID: commentID, TargetUserID: userID, Success: true, Message: "评论回复成功"}, nil
}

func newBrowser() (*browser.Browser, error) {
	return browser.NewBrowser(configs.IsHeadless(), browser.WithBinPath(configs.GetBinPath()))
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

// withBrowserPage 执行需要浏览器页面的操作的通用函数
func withBrowserPage(fn func(*rod.Page) error) error {
	b, err := newBrowser()
	if err != nil {
		return err
	}
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	return fn(page)
}

var _ = withBrowserPage

// GetMyProfile 获取当前登录用户的个人信息
func (s *XiaohongshuService) GetMyProfile(ctx context.Context) (*UserProfileResponse, error) {
	return s.GetMyProfileForAccount(ctx, "")
}

func (s *XiaohongshuService) GetMyProfileForAccount(ctx context.Context, account string) (*UserProfileResponse, error) {
	var result *xiaohongshu.UserProfileResponse
	var err error
	err = s.withBrowserPageForAccount(ctx, account, func(page *rod.Page) error {
		action := xiaohongshu.NewUserProfileAction(page)
		result, err = action.GetMyProfileViaSidebar(ctx)
		return err
	})

	if err != nil {
		return nil, err
	}

	response := &UserProfileResponse{
		UserBasicInfo: result.UserBasicInfo,
		Interactions:  result.Interactions,
		Feeds:         result.Feeds,
	}

	return response, nil
}
