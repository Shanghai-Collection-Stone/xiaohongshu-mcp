package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/configs"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/downloader"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/xhsutil"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

// MCP 工具处理函数

func (s *AppServer) resolveAccount(sel *UserSelector) string {
	if sel == nil {
		return ""
	}
	if strings.TrimSpace(sel.Account) != "" {
		return strings.TrimSpace(sel.Account)
	}
	if sel.Index == nil {
		return ""
	}
	if s.runtime == nil || s.runtime.UserPool == nil {
		return ""
	}
	u, err := s.runtime.UserPool.Resolve("", sel.Index)
	if err != nil {
		return ""
	}
	return u.Account
}

func (s *AppServer) resolveAccountFromArgsMap(args map[string]any) string {
	v, ok := args["user"]
	if !ok || v == nil {
		return ""
	}
	sel, ok := v.(*UserSelector)
	if !ok {
		return ""
	}
	return s.resolveAccount(sel)
}

func (s *AppServer) resolveTargetAccounts(targets TargetUsers) []string {
	accounts := make([]string, 0)

	if len(targets.Accounts) > 0 {
		seen := make(map[string]struct{}, len(targets.Accounts))
		for _, a := range targets.Accounts {
			a = strings.TrimSpace(a)
			if a == "" {
				continue
			}
			if _, ok := seen[a]; ok {
				continue
			}
			seen[a] = struct{}{}
			accounts = append(accounts, a)
		}
		if len(accounts) > 0 {
			return accounts
		}
	}

	if len(targets.Indices) > 0 {
		if s.runtime != nil && s.runtime.UserPool != nil {
			seen := make(map[string]struct{}, len(targets.Indices))
			for _, idx := range targets.Indices {
				i := idx
				u, err := s.runtime.UserPool.Resolve("", &i)
				if err != nil {
					continue
				}
				a := strings.TrimSpace(u.Account)
				if a == "" {
					continue
				}
				if _, ok := seen[a]; ok {
					continue
				}
				seen[a] = struct{}{}
				accounts = append(accounts, a)
			}
			if len(accounts) > 0 {
				return accounts
			}
		}
	}

	useAll := targets.AllEnabled
	if !targets.AllEnabled && len(targets.Accounts) == 0 && len(targets.Indices) == 0 {
		useAll = true
	}
	if useAll {
		if s.runtime != nil && s.runtime.UserPool != nil {
			accounts = append(accounts, s.runtime.UserPool.EnabledAccounts()...)
		}
	}
	if len(accounts) == 0 {
		accounts = []string{"default"}
	}
	return accounts
}

func (s *AppServer) handleCheckLoginStatus(ctx context.Context, args LoginUserArgs) *MCPToolResult {
	logrus.Info("MCP: 检查登录状态")

	account := s.resolveAccount(args.User)
	effectiveAccount := s.xiaohongshuService.effectiveAccount(account)
	status, err := s.xiaohongshuService.CheckLoginStatusForAccount(ctx, effectiveAccount)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "检查登录状态失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	// 根据 IsLoggedIn 判断并返回友好的提示
	var resultText string
	if status.IsLoggedIn {
		resultText = fmt.Sprintf("✅ 已登录\n账号: %s\n用户名: %s\n\n你可以使用其他功能了。", effectiveAccount, status.Username)
	} else {
		resultText = fmt.Sprintf("❌ 未登录\n账号: %s\n\n请使用 get_login_qrcode 工具获取二维码进行登录。", effectiveAccount)
	}

	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: resultText,
		}},
	}
}

func (s *AppServer) handleCheckLoginStatusBatch(ctx context.Context, args CheckLoginStatusBatchArgs) *MCPToolResult {
	logrus.Info("MCP: 批量检查登录状态")

	accounts := s.resolveTargetAccounts(args.Targets)

	type item struct {
		Account    string `json:"account"`
		IsLoggedIn bool   `json:"is_logged_in"`
		Username   string `json:"username,omitempty"`
		Error      string `json:"error,omitempty"`
	}

	workers := 1
	if s.runtime != nil && s.runtime.BrowserPoolSize > 0 {
		workers = s.runtime.BrowserPoolSize
	}
	results := runAccountBatch(ctx, accounts, workers, s.xiaohongshuService.CheckLoginStatusForAccount)
	out := make([]item, 0, len(results))
	for _, r := range results {
		if r.Err != nil {
			out = append(out, item{Account: r.Account, IsLoggedIn: false, Error: r.Err.Error()})
			continue
		}
		if r.Value == nil {
			out = append(out, item{Account: r.Account, IsLoggedIn: false, Error: "empty status"})
			continue
		}
		out = append(out, item{Account: r.Account, IsLoggedIn: r.Value.IsLoggedIn, Username: r.Value.Username})
	}

	jsonData, err := json.MarshalIndent(map[string]any{"results": out}, "", "  ")
	if err != nil {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "序列化失败: " + err.Error()}}, IsError: true}
	}
	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: string(jsonData)}}}
}

type accountBatchResult[T any] struct {
	Account string
	Value   T
	Err     error
}

func runAccountBatch[T any](
	ctx context.Context,
	accounts []string,
	workers int,
	fn func(ctx context.Context, account string) (T, error),
) []accountBatchResult[T] {
	if len(accounts) == 0 {
		return nil
	}
	if workers < 1 {
		workers = 1
	}
	if workers > len(accounts) {
		workers = len(accounts)
	}

	type job struct {
		idx     int
		account string
	}
	type result struct {
		idx   int
		value accountBatchResult[T]
	}

	jobs := make(chan job)
	results := make(chan result, len(accounts))

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := range jobs {
				if err := ctx.Err(); err != nil {
					var zero T
					results <- result{idx: j.idx, value: accountBatchResult[T]{Account: j.account, Value: zero, Err: err}}
					continue
				}
				v, err := fn(ctx, j.account)
				results <- result{idx: j.idx, value: accountBatchResult[T]{Account: j.account, Value: v, Err: err}}
			}
		}()
	}

	go func() {
		for idx, a := range accounts {
			jobs <- job{idx: idx, account: a}
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	out := make([]accountBatchResult[T], len(accounts))
	for r := range results {
		out[r.idx] = r.value
	}
	return out
}

func (s *AppServer) handleGetLoginQrcode(ctx context.Context, args LoginUserArgs) *MCPToolResult {
	logrus.Info("MCP: 获取登录扫码图片")

	account := s.resolveAccount(args.User)
	effectiveAccount := s.xiaohongshuService.effectiveAccount(account)
	result, err := s.xiaohongshuService.GetLoginQrcodeForAccount(ctx, effectiveAccount)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: "获取登录扫码图片失败: " + err.Error()}},
			IsError: true,
		}
	}

	if result.IsLoggedIn {
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: "你当前已处于登录状态"}},
		}
	}

	now := time.Now()
	deadline := func() string {
		d, err := time.ParseDuration(result.Timeout)
		if err != nil {
			return now.Format("2006-01-02 15:04:05")
		}
		return now.Add(d).Format("2006-01-02 15:04:05")
	}()

	// 已登录：文本 + 图片
	text := "请用小红书 App 在 " + deadline + " 前扫码登录 👇"
	text = "账号 " + effectiveAccount + "：" + text
	contents := []MCPContent{
		{Type: "text", Text: text},
		{
			Type:     "image",
			MimeType: "image/png",
			Data:     strings.TrimPrefix(result.Img, "data:image/png;base64,"),
		},
	}
	return &MCPToolResult{Content: contents}
}

func (s *AppServer) handleDeleteCookies(ctx context.Context, args LoginUserArgs) *MCPToolResult {
	logrus.Info("MCP: 删除 cookies，重置登录状态")

	account := s.resolveAccount(args.User)
	effectiveAccount := s.xiaohongshuService.effectiveAccount(account)
	err := s.xiaohongshuService.DeleteCookiesForAccount(ctx, effectiveAccount)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: "删除 cookies 失败: " + err.Error()}},
			IsError: true,
		}
	}

	cookiePath := cookies.GetCookiesFilePath()
	if s.runtime != nil && s.runtime.CookieStore != nil {
		u := s.xiaohongshuService.resolveUser(effectiveAccount)
		abs, _ := s.runtime.CookieStore.CookiePathFor(effectiveAccount, u.CookieFile)
		cookiePath = abs
	}
	resultText := fmt.Sprintf("Cookies 已成功删除，登录状态已重置。\n\n删除的文件路径: %s\n\n下次操作时，需要重新登录。", cookiePath)
	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: resultText,
		}},
	}
}

// handlePublishContent 处理发布内容
func (s *AppServer) handlePublishContent(ctx context.Context, args map[string]any) *MCPToolResult {
	logrus.Info("MCP: 发布内容")

	// 解析参数
	title, _ := args["title"].(string)
	content, _ := args["content"].(string)
	imagePathsInterface, _ := args["images"].([]any)
	tagsInterface, _ := args["tags"].([]any)

	var imagePaths []string
	for _, path := range imagePathsInterface {
		if pathStr, ok := path.(string); ok {
			imagePaths = append(imagePaths, pathStr)
		}
	}

	var tags []string
	for _, tag := range tagsInterface {
		if tagStr, ok := tag.(string); ok {
			tags = append(tags, tagStr)
		}
	}

	// 解析定时发布参数
	scheduleAt, _ := args["schedule_at"].(string)
	location, _ := args["location"].(string)

	logrus.Infof("MCP: 发布内容 - 标题: %s, 图片数量: %d, 标签数量: %d, 地点: %s, 定时: %s", title, len(imagePaths), len(tags), location, scheduleAt)

	// 构建发布请求
	req := &PublishRequest{
		Title:      title,
		Content:    content,
		Images:     imagePaths,
		Tags:       tags,
		Location:   location,
		ScheduleAt: scheduleAt,
	}

	// 执行发布
	account := s.resolveAccountFromArgsMap(args)
	result, err := s.xiaohongshuService.PublishContentForAccount(ctx, account, req)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发布失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	resultText := fmt.Sprintf("内容发布成功: %+v", result)
	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: resultText,
		}},
	}
}

func (s *AppServer) handlePublishContentBatch(ctx context.Context, args PublishContentBatchArgs) *MCPToolResult {
	logrus.Info("MCP: 批量发布内容")

	if strings.TrimSpace(args.Title) == "" {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "发布失败: 缺少 title"}}, IsError: true}
	}
	if strings.TrimSpace(args.Content) == "" {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "发布失败: 缺少 content"}}, IsError: true}
	}
	if len(args.Images) == 0 {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "发布失败: images 至少需要 1 张"}}, IsError: true}
	}

	prepared, err := s.prepareBatchPostForQueue(ctx, BatchPost{
		Title:      args.Title,
		Content:    args.Content,
		Images:     args.Images,
		Tags:       args.Tags,
		Location:   args.Location,
		ScheduleAt: args.ScheduleAt,
	})
	if err != nil {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "预检失败: " + err.Error()}}, IsError: true}
	}

	accounts := s.resolveTargetAccounts(args.Targets)
	if args.MaxAccounts > 0 && args.MaxAccounts < len(accounts) {
		accounts = accounts[:args.MaxAccounts]
	}

	workers := 1
	if s.runtime != nil && s.runtime.BrowserPoolSize > 0 {
		workers = s.runtime.BrowserPoolSize
	}
	if workers > len(accounts) {
		workers = len(accounts)
	}

	req := &PublishRequest{
		Title:      prepared.Title,
		Content:    prepared.Content,
		Images:     prepared.Images,
		Tags:       prepared.Tags,
		Location:   prepared.Location,
		ScheduleAt: prepared.ScheduleAt,
	}

	type item struct {
		Account string           `json:"account"`
		Result  *PublishResponse `json:"result,omitempty"`
		Error   string           `json:"error,omitempty"`
	}

	results := runAccountBatch(ctx, accounts, workers, func(ctx context.Context, account string) (*PublishResponse, error) {
		return s.xiaohongshuService.PublishContentForAccount(ctx, account, req)
	})

	out := make([]item, 0, len(results))
	for _, r := range results {
		if r.Err != nil {
			out = append(out, item{Account: r.Account, Error: r.Err.Error()})
			continue
		}
		out = append(out, item{Account: r.Account, Result: r.Value})
	}

	jsonData, err := json.MarshalIndent(map[string]any{"results": out}, "", "  ")
	if err != nil {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "序列化失败: " + err.Error()}}, IsError: true}
	}
	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: string(jsonData)}}}
}

// handlePublishVideo 处理发布视频内容（仅本地单个视频文件）
func (s *AppServer) handlePublishVideo(ctx context.Context, args map[string]any) *MCPToolResult {
	logrus.Info("MCP: 发布视频内容（本地）")

	title, _ := args["title"].(string)
	content, _ := args["content"].(string)
	videoPath, _ := args["video"].(string)
	tagsInterface, _ := args["tags"].([]any)

	var tags []string
	for _, tag := range tagsInterface {
		if tagStr, ok := tag.(string); ok {
			tags = append(tags, tagStr)
		}
	}

	if videoPath == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发布失败: 缺少本地视频文件路径",
			}},
			IsError: true,
		}
	}

	// 解析定时发布参数
	scheduleAt, _ := args["schedule_at"].(string)
	location, _ := args["location"].(string)

	logrus.Infof("MCP: 发布视频 - 标题: %s, 标签数量: %d, 地点: %s, 定时: %s", title, len(tags), location, scheduleAt)

	// 构建发布请求
	req := &PublishVideoRequest{
		Title:      title,
		Content:    content,
		Video:      videoPath,
		Tags:       tags,
		Location:   location,
		ScheduleAt: scheduleAt,
	}

	// 执行发布
	account := s.resolveAccountFromArgsMap(args)
	result, err := s.xiaohongshuService.PublishVideoForAccount(ctx, account, req)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发布失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	resultText := fmt.Sprintf("视频发布成功: %+v", result)
	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: resultText,
		}},
	}
}

// handleListFeeds 处理获取Feeds列表
func (s *AppServer) handleListFeeds(ctx context.Context, args ListFeedsArgs) *MCPToolResult {
	logrus.Info("MCP: 获取Feeds列表")

	account := s.resolveAccount(args.User)
	result, err := s.xiaohongshuService.ListFeedsForAccount(ctx, account)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "获取Feeds列表失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	// 格式化输出，转换为JSON字符串
	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: fmt.Sprintf("获取Feeds列表成功，但序列化失败: %v", err),
			}},
			IsError: true,
		}
	}

	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: string(jsonData),
		}},
	}
}

// handleSearchFeeds 处理搜索Feeds
func (s *AppServer) handleSearchFeeds(ctx context.Context, args SearchFeedsArgs) *MCPToolResult {
	logrus.Info("MCP: 搜索Feeds")

	if args.Keyword == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "搜索Feeds失败: 缺少关键词参数",
			}},
			IsError: true,
		}
	}

	logrus.Infof("MCP: 搜索Feeds - 关键词: %s", args.Keyword)

	// 将 MCP 的 FilterOption 转换为 xiaohongshu.FilterOption
	filter := xiaohongshu.FilterOption{
		SortBy:      args.Filters.SortBy,
		NoteType:    args.Filters.NoteType,
		PublishTime: args.Filters.PublishTime,
		SearchScope: args.Filters.SearchScope,
		Location:    args.Filters.Location,
	}
	account := s.resolveAccount(args.User)
	result, err := s.xiaohongshuService.SearchFeedsForAccount(ctx, account, args.Keyword, filter)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "搜索Feeds失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	// 格式化输出，转换为JSON字符串
	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: fmt.Sprintf("搜索Feeds成功，但序列化失败: %v", err),
			}},
			IsError: true,
		}
	}

	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: string(jsonData),
		}},
	}
}

func (s *AppServer) handleSearchFeedsBatch(ctx context.Context, args SearchFeedsBatchArgs) *MCPToolResult {
	logrus.Info("MCP: 批量搜索Feeds")

	if strings.TrimSpace(args.Keyword) == "" {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "搜索Feeds失败: 缺少关键词参数"}}, IsError: true}
	}

	accounts := limitSearchBatchAccounts(s.resolveTargetAccounts(args.Targets), args.MaxAccounts)

	workers := 1
	if s.runtime != nil && s.runtime.BrowserPoolSize > 0 {
		workers = s.runtime.BrowserPoolSize
	}
	if workers > len(accounts) {
		workers = len(accounts)
	}

	filter := xiaohongshu.FilterOption{
		SortBy:      args.Filters.SortBy,
		NoteType:    args.Filters.NoteType,
		PublishTime: args.Filters.PublishTime,
		SearchScope: args.Filters.SearchScope,
		Location:    args.Filters.Location,
	}

	type item struct {
		Account string             `json:"account"`
		Result  *FeedsListResponse `json:"result,omitempty"`
		Error   string             `json:"error,omitempty"`
	}

	results := runAccountBatch(ctx, accounts, workers, func(ctx context.Context, account string) (*FeedsListResponse, error) {
		return s.xiaohongshuService.SearchFeedsForAccount(ctx, account, args.Keyword, filter)
	})

	out := make([]item, 0, len(results))
	for _, r := range results {
		if r.Err != nil {
			out = append(out, item{Account: r.Account, Error: r.Err.Error()})
			continue
		}
		out = append(out, item{Account: r.Account, Result: r.Value})
	}

	jsonData, err := json.MarshalIndent(map[string]any{"keyword": args.Keyword, "results": out}, "", "  ")
	if err != nil {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "序列化失败: " + err.Error()}}, IsError: true}
	}
	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: string(jsonData)}}}
}

func limitSearchBatchAccounts(accounts []string, maxAccounts int) []string {
	limit := 3
	if maxAccounts > 0 && maxAccounts < limit {
		limit = maxAccounts
	}
	if limit < 1 {
		limit = 1
	}
	if len(accounts) > limit {
		accounts = accounts[:limit]
	}
	return accounts
}

// handleGetFeedDetail 处理获取Feed详情
func (s *AppServer) handleGetFeedDetail(ctx context.Context, args map[string]any) *MCPToolResult {
	logrus.Info("MCP: 获取Feed详情")

	// 解析参数
	feedID, ok := args["feed_id"].(string)
	if !ok || feedID == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "获取Feed详情失败: 缺少feed_id参数",
			}},
			IsError: true,
		}
	}

	xsecToken, ok := args["xsec_token"].(string)
	if !ok || xsecToken == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "获取Feed详情失败: 缺少xsec_token参数",
			}},
			IsError: true,
		}
	}

	loadAll := false
	if raw, ok := args["load_all_comments"]; ok {
		switch v := raw.(type) {
		case bool:
			loadAll = v
		case string:
			if parsed, err := strconv.ParseBool(v); err == nil {
				loadAll = parsed
			}
		case float64:
			loadAll = v != 0
		}
	}

	// 解析评论配置参数，如果未提供则使用默认值
	config := xiaohongshu.DefaultCommentLoadConfig()

	if raw, ok := args["click_more_replies"]; ok {
		switch v := raw.(type) {
		case bool:
			config.ClickMoreReplies = v
		case string:
			if parsed, err := strconv.ParseBool(v); err == nil {
				config.ClickMoreReplies = parsed
			}
		}
	}

	if raw, ok := args["max_replies_threshold"]; ok {
		switch v := raw.(type) {
		case float64:
			config.MaxRepliesThreshold = int(v)
		case string:
			if parsed, err := strconv.Atoi(v); err == nil {
				config.MaxRepliesThreshold = parsed
			}
		case int:
			config.MaxRepliesThreshold = v
		}
	}

	if raw, ok := args["max_comment_items"]; ok {
		switch v := raw.(type) {
		case float64:
			config.MaxCommentItems = int(v)
		case string:
			if parsed, err := strconv.Atoi(v); err == nil {
				config.MaxCommentItems = parsed
			}
		case int:
			config.MaxCommentItems = v
		}
	}

	if raw, ok := args["scroll_speed"].(string); ok && raw != "" {
		config.ScrollSpeed = raw
	}

	logrus.Infof("MCP: 获取Feed详情 - Feed ID: %s, loadAllComments=%v, config=%+v", feedID, loadAll, config)

	account := s.resolveAccountFromArgsMap(args)
	result, err := s.xiaohongshuService.GetFeedDetailWithConfigForAccount(ctx, account, feedID, xsecToken, loadAll, config)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "获取Feed详情失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	// 格式化输出，转换为JSON字符串
	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: fmt.Sprintf("获取Feed详情成功，但序列化失败: %v", err),
			}},
			IsError: true,
		}
	}

	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: string(jsonData),
		}},
	}
}

// handleUserProfile 获取用户主页
func (s *AppServer) handleUserProfile(ctx context.Context, args map[string]any) *MCPToolResult {
	logrus.Info("MCP: 获取用户主页")

	// 解析参数
	userID, ok := args["user_id"].(string)
	if !ok || userID == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "获取用户主页失败: 缺少user_id参数",
			}},
			IsError: true,
		}
	}

	xsecToken, ok := args["xsec_token"].(string)
	if !ok || xsecToken == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "获取用户主页失败: 缺少xsec_token参数",
			}},
			IsError: true,
		}
	}

	logrus.Infof("MCP: 获取用户主页 - User ID: %s", userID)

	account := s.resolveAccountFromArgsMap(args)
	result, err := s.xiaohongshuService.UserProfileForAccount(ctx, account, userID, xsecToken)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "获取用户主页失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	// 格式化输出，转换为JSON字符串
	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: fmt.Sprintf("获取用户主页，但序列化失败: %v", err),
			}},
			IsError: true,
		}
	}

	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: string(jsonData),
		}},
	}
}

// handleLikeFeed 处理点赞/取消点赞
func (s *AppServer) handleLikeFeed(ctx context.Context, args map[string]any) *MCPToolResult {
	feedID, ok := args["feed_id"].(string)
	if !ok || feedID == "" {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "操作失败: 缺少feed_id参数"}}, IsError: true}
	}
	xsecToken, ok := args["xsec_token"].(string)
	if !ok || xsecToken == "" {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "操作失败: 缺少xsec_token参数"}}, IsError: true}
	}
	unlike, _ := args["unlike"].(bool)

	var res *ActionResult
	var err error

	if unlike {
		account := s.resolveAccountFromArgsMap(args)
		res, err = s.xiaohongshuService.UnlikeFeedForAccount(ctx, account, feedID, xsecToken)
	} else {
		account := s.resolveAccountFromArgsMap(args)
		res, err = s.xiaohongshuService.LikeFeedForAccount(ctx, account, feedID, xsecToken)
	}

	if err != nil {
		action := "点赞"
		if unlike {
			action = "取消点赞"
		}
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: action + "失败: " + err.Error()}}, IsError: true}
	}

	action := "点赞"
	if unlike {
		action = "取消点赞"
	}
	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: fmt.Sprintf("%s成功 - Feed ID: %s", action, res.FeedID)}}}
}

// handleFavoriteFeed 处理收藏/取消收藏
func (s *AppServer) handleFavoriteFeed(ctx context.Context, args map[string]any) *MCPToolResult {
	feedID, ok := args["feed_id"].(string)
	if !ok || feedID == "" {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "操作失败: 缺少feed_id参数"}}, IsError: true}
	}
	xsecToken, ok := args["xsec_token"].(string)
	if !ok || xsecToken == "" {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "操作失败: 缺少xsec_token参数"}}, IsError: true}
	}
	unfavorite, _ := args["unfavorite"].(bool)

	var res *ActionResult
	var err error

	if unfavorite {
		account := s.resolveAccountFromArgsMap(args)
		res, err = s.xiaohongshuService.UnfavoriteFeedForAccount(ctx, account, feedID, xsecToken)
	} else {
		account := s.resolveAccountFromArgsMap(args)
		res, err = s.xiaohongshuService.FavoriteFeedForAccount(ctx, account, feedID, xsecToken)
	}

	if err != nil {
		action := "收藏"
		if unfavorite {
			action = "取消收藏"
		}
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: action + "失败: " + err.Error()}}, IsError: true}
	}

	action := "收藏"
	if unfavorite {
		action = "取消收藏"
	}
	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: fmt.Sprintf("%s成功 - Feed ID: %s", action, res.FeedID)}}}
}

// handlePostComment 处理发表评论到Feed
func (s *AppServer) handlePostComment(ctx context.Context, args map[string]any) *MCPToolResult {
	logrus.Info("MCP: 发表评论到Feed")

	// 解析参数
	feedID, ok := args["feed_id"].(string)
	if !ok || feedID == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发表评论失败: 缺少feed_id参数",
			}},
			IsError: true,
		}
	}

	xsecToken, ok := args["xsec_token"].(string)
	if !ok || xsecToken == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发表评论失败: 缺少xsec_token参数",
			}},
			IsError: true,
		}
	}

	content, ok := args["content"].(string)
	if !ok || content == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发表评论失败: 缺少content参数",
			}},
			IsError: true,
		}
	}

	logrus.Infof("MCP: 发表评论 - Feed ID: %s, 内容长度: %d", feedID, len(content))

	// 发表评论
	account := s.resolveAccountFromArgsMap(args)
	result, err := s.xiaohongshuService.PostCommentToFeedForAccount(ctx, account, feedID, xsecToken, content)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发表评论失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	// 返回成功结果，只包含feed_id
	resultText := fmt.Sprintf("评论发表成功 - Feed ID: %s", result.FeedID)
	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: resultText,
		}},
	}
}

// handleReplyComment 处理回复评论
func (s *AppServer) handleReplyComment(ctx context.Context, args map[string]any) *MCPToolResult {
	logrus.Info("MCP: 回复评论")

	// 解析参数
	feedID, ok := args["feed_id"].(string)
	if !ok || feedID == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "回复评论失败: 缺少feed_id参数",
			}},
			IsError: true,
		}
	}

	xsecToken, ok := args["xsec_token"].(string)
	if !ok || xsecToken == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "回复评论失败: 缺少xsec_token参数",
			}},
			IsError: true,
		}
	}

	commentID, _ := args["comment_id"].(string)
	userID, _ := args["user_id"].(string)
	if commentID == "" && userID == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "回复评论失败: 缺少comment_id或user_id参数",
			}},
			IsError: true,
		}
	}

	content, ok := args["content"].(string)
	if !ok || content == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "回复评论失败: 缺少content参数",
			}},
			IsError: true,
		}
	}

	logrus.Infof("MCP: 回复评论 - Feed ID: %s, Comment ID: %s, User ID: %s, 内容长度: %d", feedID, commentID, userID, len(content))

	// 回复评论
	account := s.resolveAccountFromArgsMap(args)
	result, err := s.xiaohongshuService.ReplyCommentToFeedForAccount(ctx, account, feedID, xsecToken, commentID, userID, content)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "回复评论失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	// 返回成功结果
	responseText := fmt.Sprintf("评论回复成功 - Feed ID: %s, Comment ID: %s, User ID: %s", result.FeedID, result.TargetCommentID, result.TargetUserID)
	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: responseText,
		}},
	}
}

func (s *AppServer) handleListUsers(ctx context.Context) *MCPToolResult {
	_ = ctx
	if s.runtime == nil || s.runtime.UserPool == nil {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "用户池未初始化"}}, IsError: true}
	}
	users := s.runtime.UserPool.ListSummaries()
	jsonData, err := json.MarshalIndent(map[string]any{"users": users}, "", "  ")
	if err != nil {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "序列化失败: " + err.Error()}}, IsError: true}
	}
	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: string(jsonData)}}}
}

func (s *AppServer) handleBatchTaskOpen(ctx context.Context) *MCPToolResult {
	_ = ctx
	if s.runtime == nil || s.runtime.BatchTasks == nil {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "批量任务未初始化"}}, IsError: true}
	}
	t := s.runtime.BatchTasks.Create()
	snap, _ := s.runtime.BatchTasks.Snapshot(t.ID)
	jsonData, _ := json.MarshalIndent(map[string]any{"task_id": t.ID, "status": snap}, "", "  ")
	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: string(jsonData)}}}
}

func (s *AppServer) prepareBatchPostForQueue(ctx context.Context, post BatchPost) (BatchPost, error) {
	_ = ctx
	logrus.WithFields(logrus.Fields{
		"title":           shortenForLog(post.Title, 64),
		"title_len_units": xhsutil.CalcTitleLength(post.Title),
		"content_runes":   len([]rune(post.Content)),
		"images_in":       len(post.Images),
		"tags_in":         len(post.Tags),
		"location_set":    strings.TrimSpace(post.Location) != "",
		"schedule_at":     strings.TrimSpace(post.ScheduleAt),
		"max_content":     configs.GetContentMaxRunes(),
	}).Info("batch:add_post precheck begin")

	post.Title = strings.TrimSpace(post.Title)
	if xhsutil.CalcTitleLength(post.Title) > 20 {
		return BatchPost{}, fmt.Errorf("标题长度超过限制")
	}

	maxRunes := configs.GetContentMaxRunes()
	if maxRunes > 0 {
		if n := len([]rune(post.Content)); n > maxRunes {
			return BatchPost{}, fmt.Errorf("正文长度超过限制: %d/%d", n, maxRunes)
		}
	}

	if strings.TrimSpace(post.ScheduleAt) != "" {
		t, err := time.Parse(time.RFC3339, post.ScheduleAt)
		if err != nil {
			return BatchPost{}, fmt.Errorf("定时发布时间格式错误，请使用 ISO8601 格式: %v", err)
		}
		now := time.Now()
		minTime := now.Add(1 * time.Hour)
		maxTime := now.Add(14 * 24 * time.Hour)
		if t.Before(minTime) {
			return BatchPost{}, fmt.Errorf("定时发布时间必须至少在1小时后")
		}
		if t.After(maxTime) {
			return BatchPost{}, fmt.Errorf("定时发布时间不能超过14天")
		}
	}

	images := make([]string, 0, len(post.Images))
	for _, img := range post.Images {
		img = strings.TrimSpace(img)
		if img == "" {
			continue
		}
		images = append(images, img)
	}
	if len(images) == 0 {
		return BatchPost{}, fmt.Errorf("images 至少需要 1 张")
	}
	if len(images) > 18 {
		return BatchPost{}, fmt.Errorf("图片数量超过限制: %d/18", len(images))
	}

	maxImageBytes := configs.GetImageMaxBytes()
	urlCount := 0
	localCount := 0
	for _, img := range images {
		if downloader.IsImageURL(img) {
			urlCount++
		} else {
			localCount++
		}
	}
	logrus.WithFields(logrus.Fields{
		"images_total":          len(images),
		"images_url":            urlCount,
		"images_local":          localCount,
		"images_sample":         shortenSliceForLog(images, 3, 96),
		"image_max_bytes":       maxImageBytes,
		"downloader_output_dir": configs.GetImagesPath(),
	}).Info("batch:add_post images normalized")

	localErrs := make([]error, 0)
	for i, img := range images {
		if downloader.IsImageURL(img) {
			continue
		}
		abs, err := filepath.Abs(img)
		if err != nil {
			localErrs = append(localErrs, fmt.Errorf("图片路径无效: %s", img))
			continue
		}
		info, err := os.Stat(abs)
		if err != nil {
			localErrs = append(localErrs, fmt.Errorf("图片不可访问: %s", img))
			continue
		}
		if info.IsDir() {
			localErrs = append(localErrs, fmt.Errorf("图片路径是目录: %s", img))
			continue
		}
		if maxImageBytes > 0 && info.Size() > maxImageBytes {
			localErrs = append(localErrs, fmt.Errorf("图片过大: %s (%d bytes)", img, info.Size()))
			continue
		}
		images[i] = abs
	}
	if len(localErrs) > 0 {
		logrus.WithFields(logrus.Fields{
			"errors": len(localErrs),
		}).Warn("batch:add_post local image validation failed")
		return BatchPost{}, errors.Join(localErrs...)
	}

	processor := downloader.NewImageProcessor()
	localPaths, err := processor.ProcessImages(images)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"error": err.Error(),
		}).Warn("batch:add_post image processing failed")
		return BatchPost{}, err
	}

	logrus.WithFields(logrus.Fields{
		"images_out":        len(localPaths),
		"images_out_sample": shortenSliceForLog(localPaths, 3, 96),
	}).Info("batch:add_post precheck ok")

	post.Images = localPaths
	return post, nil
}

func (s *AppServer) handleBatchTaskAddPost(ctx context.Context, args BatchTaskAddPostArgs) *MCPToolResult {
	if s.runtime == nil || s.runtime.BatchTasks == nil {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "批量任务未初始化"}}, IsError: true}
	}
	if strings.TrimSpace(args.TaskID) == "" {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "缺少 task_id"}}, IsError: true}
	}
	logrus.WithFields(logrus.Fields{
		"task_id":          args.TaskID,
		"title":            shortenForLog(args.Post.Title, 64),
		"content_runes":    len([]rune(args.Post.Content)),
		"images_in":        len(args.Post.Images),
		"tags_in":          len(args.Post.Tags),
		"location_set":     strings.TrimSpace(args.Post.Location) != "",
		"schedule_at":      strings.TrimSpace(args.Post.ScheduleAt),
		"images_in_sample": shortenSliceForLog(args.Post.Images, 3, 96),
	}).Info("batch:add_post request received")

	prepared, err := s.prepareBatchPostForQueue(ctx, args.Post)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"task_id": args.TaskID,
			"error":   err.Error(),
		}).Warn("batch:add_post rejected")
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "预检失败: " + err.Error()}}, IsError: true}
	}
	if err := s.runtime.BatchTasks.AddPost(args.TaskID, prepared); err != nil {
		logrus.WithFields(logrus.Fields{
			"task_id": args.TaskID,
			"error":   err.Error(),
		}).Warn("batch:add_post store add failed")
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "添加失败: " + err.Error()}}, IsError: true}
	}
	snap, _ := s.runtime.BatchTasks.Snapshot(args.TaskID)
	logrus.WithFields(logrus.Fields{
		"task_id": args.TaskID,
		"status":  snap.Status,
		"total":   snap.Total,
		"done":    snap.Done,
		"failed":  snap.Failed,
	}).Info("batch:add_post stored")
	jsonData, _ := json.MarshalIndent(map[string]any{"task_id": args.TaskID, "status": snap}, "", "  ")
	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: string(jsonData)}}}
}

func shortenForLog(s string, maxRunes int) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", " "), "\n", " "))
	if maxRunes <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "..."
}

func shortenSliceForLog(items []string, maxItems int, maxRunes int) []string {
	if maxItems <= 0 {
		maxItems = 1
	}
	if len(items) == 0 {
		return nil
	}
	n := len(items)
	if n > maxItems {
		n = maxItems
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, shortenForLog(items[i], maxRunes))
	}
	return out
}

func (s *AppServer) handleBatchTaskRun(ctx context.Context, args BatchTaskRunArgs) *MCPToolResult {
	_ = ctx
	if s.runtime == nil || s.runtime.BatchTasks == nil {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "批量任务未初始化"}}, IsError: true}
	}
	if strings.TrimSpace(args.TaskID) == "" {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "缺少 task_id"}}, IsError: true}
	}
	cfg := BatchTaskRunConfig{Targets: args.Targets, CallbackURL: args.CallbackURL, MinDelayMs: args.MinDelayMs, MaxDelayMs: args.MaxDelayMs, MaxAccounts: args.MaxAccounts, ItemTimeoutMs: args.ItemTimeoutMs}
	if err := s.runtime.BatchTasks.StartRun(s.runtime, s.xiaohongshuService, args.TaskID, cfg); err != nil {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "运行失败: " + err.Error()}}, IsError: true}
	}
	snap, _ := s.runtime.BatchTasks.Snapshot(args.TaskID)
	jsonData, _ := json.MarshalIndent(map[string]any{"task_id": args.TaskID, "status": snap}, "", "  ")
	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: string(jsonData)}}}
}

func (s *AppServer) handleBatchTaskRunSync(ctx context.Context, args BatchTaskRunSyncArgs) *MCPToolResult {
	if s.runtime == nil || s.runtime.BatchTasks == nil {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "批量任务未初始化"}}, IsError: true}
	}
	if strings.TrimSpace(args.TaskID) == "" {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "缺少 task_id"}}, IsError: true}
	}

	cfg := BatchTaskRunConfig{Targets: args.Targets, CallbackURL: args.CallbackURL, MinDelayMs: args.MinDelayMs, MaxDelayMs: args.MaxDelayMs, MaxAccounts: args.MaxAccounts, ItemTimeoutMs: args.ItemTimeoutMs}
	if err := s.runtime.BatchTasks.StartRun(s.runtime, s.xiaohongshuService, args.TaskID, cfg); err != nil {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "运行失败: " + err.Error()}}, IsError: true}
	}

	pollInterval := time.Duration(args.PollIntervalMs) * time.Millisecond
	wctx := ctx
	var cancel context.CancelFunc
	if args.WaitTimeoutMs > 0 {
		wctx, cancel = context.WithTimeout(ctx, time.Duration(args.WaitTimeoutMs)*time.Millisecond)
	} else {
		if _, ok := ctx.Deadline(); !ok {
			wctx, cancel = context.WithTimeout(ctx, 30*time.Minute)
		}
	}
	if cancel != nil {
		defer cancel()
	}

	logrus.WithFields(logrus.Fields{
		"task_id":          args.TaskID,
		"wait_timeout_ms":  args.WaitTimeoutMs,
		"poll_interval_ms": args.PollIntervalMs,
	}).Info("batch: sync wait begin")

	snap, err := s.runtime.BatchTasks.waitDone(wctx, args.TaskID, pollInterval)
	out := map[string]any{"task_id": args.TaskID, "status": snap}
	if err != nil {
		out["wait_error"] = err.Error()
	}
	jsonData, _ := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: string(jsonData)}}, IsError: true}
	}
	if snap.Status == BatchTaskStatusFailed {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: string(jsonData)}}, IsError: true}
	}
	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: string(jsonData)}}}
}
