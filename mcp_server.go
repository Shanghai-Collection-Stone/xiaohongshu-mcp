package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"runtime/debug"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sirupsen/logrus"
)

// Helper functions for annotation pointers
func boolPtr(b bool) *bool { return &b }

// MCP 工具参数结构体定义

type UserSelector struct {
	Account string `json:"account,omitempty" jsonschema:"账号（users.json中的account）"`
	Index   *int   `json:"index,omitempty" jsonschema:"用户序号（users.json中的索引，从0开始）"`
}

type LoginUserArgs struct {
	User *UserSelector `json:"user,omitempty" jsonschema:"可选用户选择器"`
}

type TargetUsers struct {
	AllEnabled bool     `json:"all_enabled,omitempty" jsonschema:"是否选择 users.json 中 enabled=true 的所有用户"`
	Accounts   []string `json:"accounts,omitempty" jsonschema:"指定账号列表（users.json 中的 account）"`
	Indices    []int    `json:"indices,omitempty" jsonschema:"指定用户序号列表（users.json 的索引，从0开始）"`
}

type CheckLoginStatusBatchArgs struct {
	Targets TargetUsers `json:"targets,omitempty" jsonschema:"批量目标选择（为空则默认 all_enabled=true）"`
}

type PublishContentBatchArgs struct {
	Targets     TargetUsers `json:"targets,omitempty" jsonschema:"批量目标选择（为空则默认 all_enabled=true）"`
	MaxAccounts int         `json:"max_accounts,omitempty" jsonschema:"最多使用多少个账号执行（从目标集合头部截取）"`
	Title       string      `json:"title" jsonschema:"内容标题（小红书限制：最多20个中文字或英文单词）"`
	Content     string      `json:"content" jsonschema:"正文内容，不包含以#开头的标签内容，所有话题标签都用tags参数来生成和提供即可"`
	Images      []string    `json:"images" jsonschema:"图片路径列表（至少需要1张图片）。支持两种方式：1. HTTP/HTTPS图片链接（自动下载）；2. 本地图片绝对路径（推荐，如:/Users/user/image.jpg）"`
	Tags        []string    `json:"tags,omitempty" jsonschema:"话题标签列表（可选参数），如 [美食, 旅行, 生活]"`
	Location    string      `json:"location,omitempty" jsonschema:"发布地点（可选）。示例：上海迪士尼度假区 / 北京·三里屯"`
	ScheduleAt  string      `json:"schedule_at,omitempty" jsonschema:"定时发布时间（可选），ISO8601格式如 2024-01-20T10:30:00+08:00，支持1小时至14天内。不填则立即发布"`
}

// PublishContentArgs 发布内容的参数
type PublishContentArgs struct {
	User       *UserSelector `json:"user,omitempty" jsonschema:"可选用户选择器"`
	Title      string        `json:"title" jsonschema:"内容标题（小红书限制：最多20个中文字或英文单词）"`
	Content    string        `json:"content" jsonschema:"正文内容，不包含以#开头的标签内容，所有话题标签都用tags参数来生成和提供即可"`
	Images     []string      `json:"images" jsonschema:"图片路径列表（至少需要1张图片）。支持两种方式：1. HTTP/HTTPS图片链接（自动下载）；2. 本地图片绝对路径（推荐，如:/Users/user/image.jpg）"`
	Tags       []string      `json:"tags,omitempty" jsonschema:"话题标签列表（可选参数），如 [美食, 旅行, 生活]"`
	Location   string        `json:"location,omitempty" jsonschema:"发布地点（可选）。示例：上海迪士尼度假区 / 北京·三里屯"`
	ScheduleAt string        `json:"schedule_at,omitempty" jsonschema:"定时发布时间（可选），ISO8601格式如 2024-01-20T10:30:00+08:00，支持1小时至14天内。不填则立即发布"`
}

// PublishVideoArgs 发布视频的参数（仅支持本地单个视频文件）
type PublishVideoArgs struct {
	User       *UserSelector `json:"user,omitempty" jsonschema:"可选用户选择器"`
	Title      string        `json:"title" jsonschema:"内容标题（小红书限制：最多20个中文字或英文单词）"`
	Content    string        `json:"content" jsonschema:"正文内容，不包含以#开头的标签内容，所有话题标签都用tags参数来生成和提供即可"`
	Video      string        `json:"video" jsonschema:"本地视频绝对路径（仅支持单个视频文件，如:/Users/user/video.mp4）"`
	Tags       []string      `json:"tags,omitempty" jsonschema:"话题标签列表（可选参数），如 [美食, 旅行, 生活]"`
	Location   string        `json:"location,omitempty" jsonschema:"发布地点（可选）。示例：上海迪士尼度假区 / 北京·三里屯"`
	ScheduleAt string        `json:"schedule_at,omitempty" jsonschema:"定时发布时间（可选），ISO8601格式如 2024-01-20T10:30:00+08:00，支持1小时至14天内。不填则立即发布"`
}

// SearchFeedsArgs 搜索内容的参数
type SearchFeedsArgs struct {
	User    *UserSelector `json:"user,omitempty" jsonschema:"可选用户选择器"`
	Keyword string        `json:"keyword" jsonschema:"搜索关键词"`
	Filters FilterOption  `json:"filters,omitempty" jsonschema:"筛选选项"`
}

type SearchFeedsBatchArgs struct {
	Targets     TargetUsers  `json:"targets,omitempty" jsonschema:"批量目标选择（为空则默认使用 users.json enabled=true 的前 3 个账号）"`
	MaxAccounts int          `json:"max_accounts,omitempty" jsonschema:"最多使用多少个账号执行搜索（最大 3；可填 1/2/3）"`
	Keyword     string       `json:"keyword" jsonschema:"搜索关键词"`
	Filters     FilterOption `json:"filters,omitempty" jsonschema:"筛选选项"`
}

type ListFeedsArgs struct {
	User *UserSelector `json:"user,omitempty" jsonschema:"可选用户选择器"`
}

// FilterOption 筛选选项结构体
type FilterOption struct {
	SortBy      string `json:"sort_by,omitempty" jsonschema:"排序依据: 综合|最新|最多点赞|最多评论|最多收藏,默认为'综合'"`
	NoteType    string `json:"note_type,omitempty" jsonschema:"笔记类型: 不限|视频|图文,默认为'不限'"`
	PublishTime string `json:"publish_time,omitempty" jsonschema:"发布时间: 不限|一天内|一周内|半年内,默认为'不限'"`
	SearchScope string `json:"search_scope,omitempty" jsonschema:"搜索范围: 不限|已看过|未看过|已关注,默认为'不限'"`
	Location    string `json:"location,omitempty" jsonschema:"位置距离: 不限|同城|附近,默认为'不限'"`
}

// FeedDetailArgs 获取Feed详情的参数
type FeedDetailArgs struct {
	User             *UserSelector `json:"user,omitempty" jsonschema:"可选用户选择器"`
	FeedID           string        `json:"feed_id" jsonschema:"小红书笔记ID，从Feed列表获取"`
	XsecToken        string        `json:"xsec_token" jsonschema:"访问令牌，从Feed列表的xsecToken字段获取"`
	LoadAllComments  bool          `json:"load_all_comments,omitempty" jsonschema:"是否加载全部评论。false仅返回前10条一级评论（默认），true滚动加载更多评论"`
	Limit            int           `json:"limit,omitempty" jsonschema:"【仅当load_all_comments为true时生效】限制加载的一级评论数量。例如20表示最多加载20条，默认20"`
	ClickMoreReplies bool          `json:"click_more_replies,omitempty" jsonschema:"【仅当load_all_comments为true时生效】是否展开二级回复。true展开子评论，false不展开（默认）"`
	ReplyLimit       int           `json:"reply_limit,omitempty" jsonschema:"【仅当click_more_replies为true时生效】跳过回复数过多的评论。例如10表示跳过超过10条回复的，默认10"`
	ScrollSpeed      string        `json:"scroll_speed,omitempty" jsonschema:"【仅当load_all_comments为true时生效】滚动速度slow慢速、normal正常、fast快速"`
}

// UserProfileArgs 获取用户主页的参数
type UserProfileArgs struct {
	User      *UserSelector `json:"user,omitempty" jsonschema:"可选用户选择器"`
	UserID    string        `json:"user_id" jsonschema:"小红书用户ID，从Feed列表获取"`
	XsecToken string        `json:"xsec_token" jsonschema:"访问令牌，从Feed列表的xsecToken字段获取"`
}

// PostCommentArgs 发表评论的参数
type PostCommentArgs struct {
	User      *UserSelector `json:"user,omitempty" jsonschema:"可选用户选择器"`
	FeedID    string        `json:"feed_id" jsonschema:"小红书笔记ID，从Feed列表获取"`
	XsecToken string        `json:"xsec_token" jsonschema:"访问令牌，从Feed列表的xsecToken字段获取"`
	Content   string        `json:"content" jsonschema:"评论内容"`
}

// ReplyCommentArgs 回复评论的参数
type ReplyCommentArgs struct {
	User      *UserSelector `json:"user,omitempty" jsonschema:"可选用户选择器"`
	FeedID    string        `json:"feed_id" jsonschema:"小红书笔记ID，从Feed列表获取"`
	XsecToken string        `json:"xsec_token" jsonschema:"访问令牌，从Feed列表的xsecToken字段获取"`
	CommentID string        `json:"comment_id,omitempty" jsonschema:"目标评论ID，从评论列表获取"`
	UserID    string        `json:"user_id,omitempty" jsonschema:"目标评论用户ID，从评论列表获取"`
	Content   string        `json:"content" jsonschema:"回复内容"`
}

// LikeFeedArgs 点赞参数
type LikeFeedArgs struct {
	User      *UserSelector `json:"user,omitempty" jsonschema:"可选用户选择器"`
	FeedID    string        `json:"feed_id" jsonschema:"小红书笔记ID，从Feed列表获取"`
	XsecToken string        `json:"xsec_token" jsonschema:"访问令牌，从Feed列表的xsecToken字段获取"`
	Unlike    bool          `json:"unlike,omitempty" jsonschema:"是否取消点赞，true为取消点赞，false或未设置则为点赞"`
}

// FavoriteFeedArgs 收藏参数
type FavoriteFeedArgs struct {
	User       *UserSelector `json:"user,omitempty" jsonschema:"可选用户选择器"`
	FeedID     string        `json:"feed_id" jsonschema:"小红书笔记ID，从Feed列表获取"`
	XsecToken  string        `json:"xsec_token" jsonschema:"访问令牌，从Feed列表的xsecToken字段获取"`
	Unfavorite bool          `json:"unfavorite,omitempty" jsonschema:"是否取消收藏，true为取消收藏，false或未设置则为收藏"`
}

type BatchTaskOpenArgs struct{}

type BatchTaskAddPostArgs struct {
	TaskID string    `json:"task_id" jsonschema:"批量任务ID"`
	Post   BatchPost `json:"post" jsonschema:"要加入批量任务的图文内容"`
}

type BatchTaskRunArgs struct {
	TaskID        string      `json:"task_id" jsonschema:"批量任务ID"`
	Targets       TargetUsers `json:"targets,omitempty" jsonschema:"可选账号集合（为空则使用 users.json enabled=true 的账号列表）"`
	CallbackURL   string      `json:"callback_url,omitempty" jsonschema:"回调URL（POST JSON：任务进度与状态）"`
	MinDelayMs    int         `json:"min_delay_ms,omitempty" jsonschema:"每篇发布后随机延迟最小值（毫秒）"`
	MaxDelayMs    int         `json:"max_delay_ms,omitempty" jsonschema:"每篇发布后随机延迟最大值（毫秒）"`
	MaxAccounts   int         `json:"max_accounts,omitempty" jsonschema:"最多使用多少个账号执行（从目标集合头部截取）"`
	ItemTimeoutMs int         `json:"item_timeout_ms,omitempty" jsonschema:"单条发布超时时间（毫秒），超时将计入失败并继续下一条；默认 360000ms"`
}

type BatchTaskRunSyncArgs struct {
	TaskID         string      `json:"task_id" jsonschema:"批量任务ID"`
	Targets        TargetUsers `json:"targets,omitempty" jsonschema:"可选账号集合（为空则使用 users.json enabled=true 的账号列表）"`
	CallbackURL    string      `json:"callback_url,omitempty" jsonschema:"回调URL（POST JSON：任务进度与状态）"`
	MinDelayMs     int         `json:"min_delay_ms,omitempty" jsonschema:"每篇发布后随机延迟最小值（毫秒）"`
	MaxDelayMs     int         `json:"max_delay_ms,omitempty" jsonschema:"每篇发布后随机延迟最大值（毫秒）"`
	MaxAccounts    int         `json:"max_accounts,omitempty" jsonschema:"最多使用多少个账号执行（从目标集合头部截取）"`
	ItemTimeoutMs  int         `json:"item_timeout_ms,omitempty" jsonschema:"单条发布超时时间（毫秒），超时将计入失败并继续下一条；默认 360000ms"`
	WaitTimeoutMs  int         `json:"wait_timeout_ms,omitempty" jsonschema:"等待批量任务完成的最长时间（毫秒）；默认 1800000ms"`
	PollIntervalMs int         `json:"poll_interval_ms,omitempty" jsonschema:"轮询间隔（毫秒），默认 500ms"`
}

// InitMCPServer 初始化 MCP Server
func InitMCPServer(appServer *AppServer) *mcp.Server {
	// 创建 MCP Server
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    "xiaohongshu-mcp",
			Version: "2.0.0",
		},
		nil,
	)

	// 注册所有工具
	registerTools(server, appServer)

	logrus.Info("MCP Server initialized with official SDK")

	return server
}

func withPanicRecovery[T any](
	toolName string,
	handler func(context.Context, *mcp.CallToolRequest, T) (*mcp.CallToolResult, any, error),
) func(context.Context, *mcp.CallToolRequest, T) (*mcp.CallToolResult, any, error) {

	return func(ctx context.Context, req *mcp.CallToolRequest, args T) (result *mcp.CallToolResult, resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				logrus.WithFields(logrus.Fields{
					"tool":  toolName,
					"panic": r,
				}).Error("Tool handler panicked")

				logrus.Errorf("Stack trace:\n%s", debug.Stack())

				result = &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{
							Text: fmt.Sprintf("工具 %s 执行时发生内部错误: %v\n\n请查看服务端日志获取详细信息。", toolName, r),
						},
					},
					IsError: true,
				}
				resp = nil
				err = nil
			}
		}()

		return handler(ctx, req, args)
	}
}

// registerTools 注册所有 MCP 工具
func registerTools(server *mcp.Server, appServer *AppServer) {
	// 工具 1: 检查登录状态
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "check_login_status",
			Description: "检查小红书登录状态",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Check Login Status",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("check_login_status", func(ctx context.Context, req *mcp.CallToolRequest, args LoginUserArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleCheckLoginStatus(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 1.1: 批量检查登录状态
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "check_login_status_batch",
			Description: "批量检查多个账号的小红书登录状态",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Check Login Status Batch",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("check_login_status_batch", func(ctx context.Context, req *mcp.CallToolRequest, args CheckLoginStatusBatchArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleCheckLoginStatusBatch(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 2: 获取登录二维码
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_login_qrcode",
			Description: "获取登录二维码（返回 Base64 图片和超时时间）",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Get Login QR Code",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("get_login_qrcode", func(ctx context.Context, req *mcp.CallToolRequest, args LoginUserArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleGetLoginQrcode(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 3: 删除 cookies（登录重置）
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "delete_cookies",
			Description: "删除 cookies 文件，重置登录状态。删除后需要重新登录。",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Delete Cookies",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("delete_cookies", func(ctx context.Context, req *mcp.CallToolRequest, args LoginUserArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleDeleteCookies(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 4: 发布内容
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "publish_content",
			Description: "发布小红书图文内容",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Publish Content",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("publish_content", func(ctx context.Context, req *mcp.CallToolRequest, args PublishContentArgs) (*mcp.CallToolResult, any, error) {
			// 转换参数格式到现有的 handler
			argsMap := map[string]interface{}{
				"user":        args.User,
				"title":       args.Title,
				"content":     args.Content,
				"images":      convertStringsToInterfaces(args.Images),
				"tags":        convertStringsToInterfaces(args.Tags),
				"location":    args.Location,
				"schedule_at": args.ScheduleAt,
			}
			result := appServer.handlePublishContent(ctx, argsMap)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 4.1: 批量发布内容
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "publish_content_batch",
			Description: "批量发布小红书图文内容（按账号并发执行）",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Publish Content Batch",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("publish_content_batch", func(ctx context.Context, req *mcp.CallToolRequest, args PublishContentBatchArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handlePublishContentBatch(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 5: 获取Feed列表
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "list_feeds",
			Description: "获取首页 Feeds 列表",
			Annotations: &mcp.ToolAnnotations{
				Title:        "List Feeds",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("list_feeds", func(ctx context.Context, req *mcp.CallToolRequest, args ListFeedsArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleListFeeds(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 6: 搜索内容
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "search_feeds",
			Description: "搜索小红书内容（需要已登录）",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Search Feeds",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("search_feeds", func(ctx context.Context, req *mcp.CallToolRequest, args SearchFeedsArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleSearchFeeds(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 6.1: 批量搜索内容（最多 3 个账号）
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "search_feeds_batch",
			Description: "批量搜索小红书内容（默认最多 3 个账号并发执行）",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Search Feeds Batch",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("search_feeds_batch", func(ctx context.Context, req *mcp.CallToolRequest, args SearchFeedsBatchArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleSearchFeedsBatch(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 7: 获取Feed详情
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_feed_detail",
			Description: "获取小红书笔记详情，返回笔记内容、图片、作者信息、互动数据（点赞/收藏/分享数）及评论列表。默认返回前10条一级评论，如需更多评论请设置load_all_comments=true",
			Annotations: &mcp.ToolAnnotations{
				Title:        "Get Feed Detail",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("get_feed_detail", func(ctx context.Context, req *mcp.CallToolRequest, args FeedDetailArgs) (*mcp.CallToolResult, any, error) {
			argsMap := map[string]interface{}{
				"user":              args.User,
				"feed_id":           args.FeedID,
				"xsec_token":        args.XsecToken,
				"load_all_comments": args.LoadAllComments,
			}

			// 只有当 load_all_comments=true 时，才处理其他参数
			if args.LoadAllComments {
				argsMap["click_more_replies"] = args.ClickMoreReplies

				// 设置评论数量限制，默认20
				limit := args.Limit
				if limit <= 0 {
					limit = 20
				}
				argsMap["max_comment_items"] = limit

				// 设置回复数量阈值，默认10
				replyLimit := args.ReplyLimit
				if replyLimit <= 0 {
					replyLimit = 10
				}
				argsMap["max_replies_threshold"] = replyLimit

				if args.ScrollSpeed != "" {
					argsMap["scroll_speed"] = args.ScrollSpeed
				}
			}

			result := appServer.handleGetFeedDetail(ctx, argsMap)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 8: 获取用户主页
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "user_profile",
			Description: "获取指定的小红书用户主页，返回用户基本信息，关注、粉丝、获赞量及其笔记内容",
			Annotations: &mcp.ToolAnnotations{
				Title:        "User Profile",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("user_profile", func(ctx context.Context, req *mcp.CallToolRequest, args UserProfileArgs) (*mcp.CallToolResult, any, error) {
			argsMap := map[string]interface{}{
				"user":       args.User,
				"user_id":    args.UserID,
				"xsec_token": args.XsecToken,
			}
			result := appServer.handleUserProfile(ctx, argsMap)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 9: 发表评论
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "post_comment_to_feed",
			Description: "发表评论到小红书笔记",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Post Comment",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("post_comment_to_feed", func(ctx context.Context, req *mcp.CallToolRequest, args PostCommentArgs) (*mcp.CallToolResult, any, error) {
			argsMap := map[string]interface{}{
				"user":       args.User,
				"feed_id":    args.FeedID,
				"xsec_token": args.XsecToken,
				"content":    args.Content,
			}
			result := appServer.handlePostComment(ctx, argsMap)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 10: 回复评论
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "reply_comment_in_feed",
			Description: "回复小红书笔记下的指定评论",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Reply Comment",
				DestructiveHint: boolPtr(true),
			},
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args ReplyCommentArgs) (*mcp.CallToolResult, any, error) {
			if args.CommentID == "" && args.UserID == "" {
				return &mcp.CallToolResult{
					IsError: true,
					Content: []mcp.Content{&mcp.TextContent{Text: "缺少 comment_id 或 user_id"}},
				}, nil, nil
			}

			argsMap := map[string]interface{}{
				"user":       args.User,
				"feed_id":    args.FeedID,
				"xsec_token": args.XsecToken,
				"comment_id": args.CommentID,
				"user_id":    args.UserID,
				"content":    args.Content,
			}
			result := appServer.handleReplyComment(ctx, argsMap)
			return convertToMCPResult(result), nil, nil
		},
	)

	// 工具 11: 发布视频（仅本地文件）
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "publish_with_video",
			Description: "发布小红书视频内容（仅支持本地单个视频文件）",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Publish Video",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("publish_with_video", func(ctx context.Context, req *mcp.CallToolRequest, args PublishVideoArgs) (*mcp.CallToolResult, any, error) {
			argsMap := map[string]interface{}{
				"user":        args.User,
				"title":       args.Title,
				"content":     args.Content,
				"video":       args.Video,
				"tags":        convertStringsToInterfaces(args.Tags),
				"location":    args.Location,
				"schedule_at": args.ScheduleAt,
			}
			result := appServer.handlePublishVideo(ctx, argsMap)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 12: 点赞笔记
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "like_feed",
			Description: "为指定笔记点赞或取消点赞（如已点赞将跳过点赞，如未点赞将跳过取消点赞）",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Like Feed",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("like_feed", func(ctx context.Context, req *mcp.CallToolRequest, args LikeFeedArgs) (*mcp.CallToolResult, any, error) {
			argsMap := map[string]interface{}{
				"user":       args.User,
				"feed_id":    args.FeedID,
				"xsec_token": args.XsecToken,
				"unlike":     args.Unlike,
			}
			result := appServer.handleLikeFeed(ctx, argsMap)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 13: 收藏笔记
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "favorite_feed",
			Description: "收藏指定笔记或取消收藏（如已收藏将跳过收藏，如未收藏将跳过取消收藏）",
			Annotations: &mcp.ToolAnnotations{
				Title:           "Favorite Feed",
				DestructiveHint: boolPtr(true),
			},
		},
		withPanicRecovery("favorite_feed", func(ctx context.Context, req *mcp.CallToolRequest, args FavoriteFeedArgs) (*mcp.CallToolResult, any, error) {
			argsMap := map[string]interface{}{
				"user":       args.User,
				"feed_id":    args.FeedID,
				"xsec_token": args.XsecToken,
				"unfavorite": args.Unfavorite,
			}
			result := appServer.handleFavoriteFeed(ctx, argsMap)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 14: 用户列表
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "list_users",
			Description: "列出用户池中的用户",
			Annotations: &mcp.ToolAnnotations{
				Title:        "List Users",
				ReadOnlyHint: true,
			},
		},
		withPanicRecovery("list_users", func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			result := appServer.handleListUsers(ctx)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 15: 开启批量任务
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "batch_task_open",
			Description: "开启批量任务并返回任务ID",
			Annotations: &mcp.ToolAnnotations{Title: "Batch Task Open"},
		},
		withPanicRecovery("batch_task_open", func(ctx context.Context, req *mcp.CallToolRequest, _ BatchTaskOpenArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleBatchTaskOpen(ctx)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 16: 往批量任务塞内容
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "batch_task_add_post",
			Description: "向批量任务添加一篇图文内容",
			Annotations: &mcp.ToolAnnotations{Title: "Batch Task Add Post", DestructiveHint: boolPtr(true)},
		},
		withPanicRecovery("batch_task_add_post", func(ctx context.Context, req *mcp.CallToolRequest, args BatchTaskAddPostArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleBatchTaskAddPost(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 17: 运行批量任务
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "batch_task_run",
			Description: "运行批量任务（可设置回调URL与随机延迟区间）",
			Annotations: &mcp.ToolAnnotations{Title: "Batch Task Run", DestructiveHint: boolPtr(true)},
		},
		withPanicRecovery("batch_task_run", func(ctx context.Context, req *mcp.CallToolRequest, args BatchTaskRunArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleBatchTaskRun(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	// 工具 18: 运行批量任务（同步等待完成）
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "batch_task_run_sync",
			Description: "运行批量任务并同步等待完成（适合不使用回调的场景）",
			Annotations: &mcp.ToolAnnotations{Title: "Batch Task Run Sync", DestructiveHint: boolPtr(true)},
		},
		withPanicRecovery("batch_task_run_sync", func(ctx context.Context, req *mcp.CallToolRequest, args BatchTaskRunSyncArgs) (*mcp.CallToolResult, any, error) {
			result := appServer.handleBatchTaskRunSync(ctx, args)
			return convertToMCPResult(result), nil, nil
		}),
	)

	logrus.Infof("Registered %d MCP tools", 19)
}

// convertToMCPResult 将自定义的 MCPToolResult 转换为官方 SDK 的格式
func convertToMCPResult(result *MCPToolResult) *mcp.CallToolResult {
	var contents []mcp.Content
	for _, c := range result.Content {
		switch c.Type {
		case "text":
			contents = append(contents, &mcp.TextContent{Text: c.Text})
		case "image":
			// 解码 base64 字符串为 []byte
			imageData, err := base64.StdEncoding.DecodeString(c.Data)
			if err != nil {
				logrus.WithError(err).Error("Failed to decode base64 image data")
				// 如果解码失败，添加错误文本
				contents = append(contents, &mcp.TextContent{
					Text: "图片数据解码失败: " + err.Error(),
				})
			} else {
				contents = append(contents, &mcp.ImageContent{
					Data:     imageData,
					MIMEType: c.MimeType,
				})
			}
		}
	}

	return &mcp.CallToolResult{
		Content: contents,
		IsError: result.IsError,
	}
}

// convertStringsToInterfaces 辅助函数：将 []string 转换为 []interface{}
func convertStringsToInterfaces(strs []string) []interface{} {
	result := make([]interface{}, len(strs))
	for i, s := range strs {
		result[i] = s
	}
	return result
}
