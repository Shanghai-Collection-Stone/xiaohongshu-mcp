# 批量化改造方案（IP 池 / Cookies 池 / 用户池 / 批量任务）

本文档描述如何把当前“小红书操作 MCP”从单账号单 cookies 的形态，改造成可管理多账号、多 cookies、多 IP，并支持批量发布任务。

## 0. 现状梳理（基于当前代码）

- 当前浏览器启动时仅加载单一 cookies 文件：由 [cookies.GetCookiesFilePath()](file:///d:/a-remote-job/xiaohongshu-mcp/cookies/cookies.go) 决定（默认 `cookies.json` 或 `COOKIES_PATH`）。
- 所有业务操作（发布/搜索/详情/评论等）都通过 [service.go:newBrowser()](file:///d:/a-remote-job/xiaohongshu-mcp/service.go) 创建全新浏览器实例，每次操作一个独立页面，天然“无状态”，但也意味着：
  - 只能代表“当前唯一登录态”；
  - 不能按用户隔离 cookies/网络；
  - 批量任务缺少任务模型、队列、状态与回调。
- MCP 工具在 [mcp_server.go](file:///d:/a-remote-job/xiaohongshu-mcp/mcp_server.go) 注册，HTTP API 路由在 [routes.go](file:///d:/a-remote-job/xiaohongshu-mcp/routes.go)；目前 API 文档在 [docs/API.md](file:///d:/a-remote-job/xiaohongshu-mcp/docs/API.md)。

目标是在尽量不破坏现有 API/工具的前提下，增加“用户选择维度”“可并发执行的浏览器池”和“批量任务系统”。

---

## 1. 配置与数据文件约定

### 1.1 IP 池（ip.txt）

- 位置：服务启动工作目录下 `ip.txt`（与可执行文件同目录或当前运行目录）。
- 格式：一行一个代理入口（建议统一为 URL 形式），支持 IPv4 / IPv6。
  - HTTP/HTTPS：`http://host:port`、`http://user:pass@host:port`、`http://[2001:db8::1]:8080`
  - SOCKS5：`socks5://host:port`、`socks5://user:pass@[2001:db8::1]:1080`
- 行级规则：
  - 允许空行与 `#` 开头注释行（读取时忽略）。
  - 去除首尾空白。

说明：当前代码未显式设置代理。实现层面将扩展浏览器封装层，支持“为某次操作指定代理”。

### 1.2 Cookies 池（每用户一个 cookies 文件）

- 目录建议：`./cookies/`（或 `./data/cookies/`，由一个统一的 DataDir 配置决定，见 1.4）。
- 文件命名规则（建议）：
  - `cookies/{safe_account}.json`
  - `safe_account`：将账号中的 `/\\:*?"<>|` 等非法字符替换为 `_`，并限制长度。
- 兼容策略：
  - 若存在历史单文件 cookies（如 `cookies.json` 或 `COOKIES_PATH` 指向的路径），将其视为默认用户 `default` 的 cookies。
  - 新增模式下，默认用户 cookies 文件路径为 `cookies/default.json`，并可提供迁移指令（复制/重命名）。

### 1.3 用户池（users.json）

- 位置建议：`./users.json`（或 `./data/users.json`）。
- 结构建议（示例）：

```json
{
  "version": 1,
  "users": [
    {
      "account": "userA",
      "password": "passA",
      "cookie_file": "cookies/userA.json",
      "ip_ref": 0,
      "enabled": true
    },
    {
      "account": "userB",
      "password": "passB",
      "cookie_file": "cookies/userB.json",
      "ip_ref": "http://[2001:db8::1]:8080",
      "enabled": true
    }·
  ]
}
```

- 字段约定：
  - `account`：唯一键，用于 MCP 与 HTTP API 的用户选择。
  - `password`：仅用于未来“自动登录/风控恢复”等场景；批量发布并不强依赖它。
  - `cookie_file`：可选。为空时按命名规则自动推导：`cookies/{safe_account}.json`。
  - `ip_ref`：可选。支持两种引用：
    - `number`：引用 ip.txt 的行号（从 0 开始或从 1 开始需要明确，建议从 0，便于程序处理）；
    - `string`：直接写代理 URL。
  - `enabled`：是否参与“批量/默认分配”。

### 1.4 DataDir（建议增加）

为了容器/本地一致性，建议引入一个统一数据根目录：

- 环境变量：`XHS_MCP_DATA_DIR`（建议）
- 默认：当前工作目录 `.`
- 约定：
  - `users.json`、`ip.txt`、`cookies/`、批量任务落盘文件都以 DataDir 为根。

### 1.5 浏览器池并发（启动可配置）

为了支持“同时执行多个账号的发布/操作”，服务启动时应支持配置浏览器并发池大小：

- 命令行参数（建议）：`-browser_pool_size=3`
- 环境变量（建议）：`XHS_MCP_BROWSER_POOL_SIZE=3`
- 默认值建议：`1`

行为约定：

- 浏览器池并发数是“全局最大并发执行数”。
- 批量任务会在该并发上限内执行；单用户同一时间仍建议只跑一个会话（见 5.2）。

---

## 2. 核心架构设计（模块化）

为避免在 `main/` 包中堆积逻辑，建议拆分以下模块（每个模块单一职责）：

> 实施时每个模块目录都包含 `module.tip`，说明边界、依赖、实体、对外接口。

### 2.1 user-pool 模块

- 责任：读取/校验/查询 users.json，提供“按账号/序号选择用户”的能力。
- 对外接口（建议）：
  - `ListUsers() []UserSummary`
  - `ResolveUser(selector UserSelector) (User, error)`

### 2.2 ip-pool 模块

- 责任：读取 ip.txt，提供代理入口列表；支持按 `ip_ref`（索引或直写）解析为实际代理配置。

### 2.3 cookie-store 模块

- 责任：按用户读取/保存/删除 cookies 文件（替代当前全局的 `GetCookiesFilePath()` 单例逻辑）。
- 关键点：并发保护（同一用户 cookies 文件不可被并发写）。

### 2.4 browser-factory 模块

- 责任：基于“用户上下文”（cookiesPath + proxy + headless + binPath）创建浏览器实例。
- 当前入口在 [browser.NewBrowser](file:///d:/a-remote-job/xiaohongshu-mcp/browser/browser.go)，建议扩展为“可注入 cookiesPath/proxy”。

### 2.5 batch-task 模块

- 责任：批量任务模型、队列、状态机、执行器、回调投递。
- 设计目标：
  - 支持“创建任务 → 追加文章 → 启动执行（带随机延迟）”。
  - 批量任务按 cookies 池顺序分发账号，并可限制使用账号数量（见 5.1）。
  - 支持浏览器池并发执行（启动可配置）。
  - 支持任务状态查询（HTTP API）与任务级回调（仅整体进度）。

---

## 3. 用户选择协议（MCP 与 HTTP API 通用）

### 3.1 UserSelector 统一结构

建议引入统一选择器（可用于 MCP args / HTTP 请求体）：

```json
{
  "user": {
    "account": "userA",
    "index": 0
  }
}
```

规则：

- `account` 与 `index` 二选一，优先 `account`。
- 未提供则使用默认用户（兼容当前行为）。
- `index` 指 users.json 中的数组顺序（从 0 开始）。

### 3.2 MCP 工具改造原则

- 现有工具名称保持不变（避免客户端配置大规模迁移）。
- 为每个工具参数结构体新增可选字段 `user`（或展开为 `account/index`），使其支持：
  - 指定用户执行；
  - 未来扩展为批量用户执行（返回按用户拆分的结果列表）。

### 3.3 新增 MCP 工具：list_users

- 目的：让 MCP 客户端能发现“当前有哪些用户可用”，并得到账号与序号。
- 返回建议字段：
  - `index`、`account`、`enabled`、`cookie_file`、`ip_ref`（脱敏显示）

---

## 4. 批量任务能力设计

你提出的 3 个 MCP 函数将落在 batch-task 模块中，并在 MCP 层暴露：

### 4.1 MCP：开启批量任务（返回任务 ID）

- 工具名建议：`batch_task_open`
- 入参建议：
  - `task_name`：可选
  - `targets`：可选，支持：
    - `all_enabled: true`
    - `accounts: ["userA","userB"]`
    - `indices: [0,1]`
  - `max_accounts`：可选，用多少个账号发（从目标集合的顺序头部截取），默认使用全部
  - `dispatch`：可选，默认 `sequential_cookie_pool`
- 返回：
  - `task_id`

### 4.2 MCP：往批量任务塞文章内容

- 工具名建议：`batch_task_add_post`
- 入参建议：
  - `task_id`：必填
  - `post`：文章内容
    - 图文：`title/content/images/tags/schedule_at`
    - （可选扩展）视频：`video`
- 返回：
  - `item_id`（或追加后的队列长度）

说明：这里的 `post` 结构建议直接复用当前 MCP 的发布入参结构（见 [mcp_server.go:PublishContentArgs](file:///d:/a-remote-job/xiaohongshu-mcp/mcp_server.go)），避免重复定义与双向漂移。

### 4.3 MCP：运行批量任务（可设置回调 URL / 随机延迟）

- 工具名建议：`batch_task_run`
- 入参建议：
  - `task_id`：必填
  - `callback_url`：可选，任务级回调地址（仅回传整体进度/状态）
  - `min_delay_ms`：可选，默认 1000
  - `max_delay_ms`：可选，默认 5000
  - `dry_run`：可选，true 时仅模拟分发与校验，不实际发布
- 返回：
  - `task_id`、`status`（running）

### 4.4 普通 HTTP API：查看批量任务状态

- 端点建议：`GET /api/v1/batch/tasks/{task_id}`
- 响应建议：

```json
{
  "success": true,
  "data": {
    "task_id": "bt_20260202_xxx",
    "status": "running",
    "created_at": "2026-02-02T12:00:00+08:00",
    "started_at": "2026-02-02T12:01:00+08:00",
    "finished_at": null,
    "total": 10,
    "done": 3,
    "failed": 0
  },
  "message": "ok"
}
```

说明：若需要调试级明细，可在后续扩展 `?detail=true` 返回 item 列表；但回调只回传整体进度（见 7）。

### 4.5 任务状态存储：仅内存，最多保留 5 条

任务状态不落盘，服务进程内存保存，且固定只保留最近 5 条任务记录：

- 容量：5（常量或配置项，默认 5）
- 淘汰：当创建第 6 条任务时，替换最旧的任务记录（按创建时间/插入顺序淘汰）
- 查询：
  - `GET /api/v1/batch/tasks/{task_id}`：若任务已被淘汰，返回 `404 TASK_NOT_FOUND`
  - （可选扩展）`GET /api/v1/batch/tasks`：返回当前内存中的 5 条任务摘要（便于发现可查询的 task_id）

---

## 5. 执行与并发策略（重要）

### 5.1 账号分发：顺序向下使用 cookies 池，并可限制账号数

批量任务默认按 cookies 池（用户池顺序）依次使用账号：

- 目标账号集合：
  - 未指定 `targets` 时：使用 `users.json` 中 `enabled=true` 的用户，按数组顺序。
  - 指定 `targets` 时：按传入顺序解析成账号列表。
- 账号数量限制：
  - `max_accounts=N` 时，仅取列表前 N 个账号参与发布。
  - 若 N 大于可用账号数，则等价于使用全部可用账号。
- 分发规则：
  - 第 1 篇用第 1 个账号，第 2 篇用第 2 个账号……用完后从第 1 个账号继续循环。

### 5.2 用户级互斥（避免 cookies 冲突）

同一个账号（同一 cookies 文件）不可并发执行“浏览器写 cookies/发布”类操作，否则会造成：

- cookies 文件被并发覆盖；
- 登录态抖动；
- 风控概率上升。

方案：batch-task 执行器按用户维度加锁（`account -> mutex`），确保同一时刻同一用户只有一个浏览器会话在跑。

### 5.3 浏览器池并发（全局 worker pool）

批量任务执行器采用 worker pool，worker 数量等于浏览器池并发配置（见 1.5）：

- 并发上限：同时最多处理 `browser_pool_size` 篇文章。
- 账号上限：同时活跃账号数量也受 `max_accounts` 影响。
- 实际并发：`min(browser_pool_size, max_accounts, remaining_items)`。

### 5.4 延迟策略

- 每处理一条文章后 sleep 一个随机延迟：`rand(min_delay_ms..max_delay_ms)`。
- 延迟与用户维度解耦：默认全局延迟；可扩展“每用户独立延迟”。

---

## 6. 需要改动的关键代码点（按文件/职责归类）

### 6.1 cookies 存取从“全局单文件”升级为“按用户”

- 当前入口：
  - [cookies.GetCookiesFilePath()](file:///d:/a-remote-job/xiaohongshu-mcp/cookies/cookies.go)
  - [browser.NewBrowser](file:///d:/a-remote-job/xiaohongshu-mcp/browser/browser.go) 会在启动时加载 cookies。
- 改造方向：
  - 提供 `GetCookiesFilePathForAccount(account string)` 或通过 cookie-store 模块解析路径。
  - `browser.NewBrowser` 增加 option：`WithCookiesPath(path)`，替代内部固定调用 `GetCookiesFilePath()`。

### 6.2 newBrowser() 需要接收“用户上下文”

- 当前：`service.go:newBrowser()`无参，始终使用同一 cookies。
- 改造：
  - `newBrowser(ctx UserContext)`：把 cookiesPath + proxy 传入 browser-factory。
  - `XiaohongshuService` 的所有方法增加可选 user selector（MCP/HTTP 层传入）。

### 6.3 MCP 工具参数结构体需要增加 user selector

- 当前 MCP 参数结构体都在 [mcp_server.go](file:///d:/a-remote-job/xiaohongshu-mcp/mcp_server.go)。
- 改造：
  - 为 `PublishContentArgs / PublishVideoArgs / SearchFeedsArgs / FeedDetailArgs / UserProfileArgs / PostCommentArgs / ReplyCommentArgs / LikeFeedArgs / FavoriteFeedArgs` 增加 `User` 字段（可选）。
  - 现有工具不传该字段时保持原行为。

### 6.4 HTTP API 增加可选用户选择方式

- 对现有端点：
  - 推荐支持 Query：`?account=userA` 或 Header：`X-Xhs-Account: userA`（二选一即可）。
  - 未提供时沿用默认用户。
- 增加批量状态端点：`GET /api/v1/batch/tasks/{task_id}`。

---

## 7. 回调设计（callback_url）

### 7.1 回调时机

回调仅针对“批量任务整体状态/进度”，不包含单篇文章明细：

- `task_progress`：进度发生变化时触发（建议：每完成 1 篇后触发一次）
- `task_finished`：任务完成（全部成功/部分失败/全部失败）

### 7.2 回调载荷建议

```json
{
  "event": "task_progress",
  "task_id": "bt_20260202_xxx",
  "status": "running",
  "total": 10,
  "done": 3,
  "failed": 0,
  "started_at": "2026-02-02T12:01:00+08:00",
  "finished_at": null,
  "ts": "2026-02-02T12:01:23+08:00"
}
```

### 7.3 安全建议（可选）

- 支持配置 `XHS_MCP_CALLBACK_SECRET`，回调时携带签名头（如 `X-Signature` = HMAC-SHA256）。
- 避免在回调中泄露 cookies、密码等敏感信息。

---

## 8. 兼容性与迁移策略

- 默认用户：
  - 未配置 users.json 时，系统自动构造一个 `default` 用户，并继续使用当前 cookies 路径逻辑（`COOKIES_PATH`/`cookies.json`）。
- 登录同步：
  - 登录流程本身在当前项目已存在（二维码登录与登录工具）。改造后登录应支持指定 `account`，并在“登录成功保存 cookies”时同步更新：
    - cookies 池：把 cookies 保存到该账号对应的 cookies 文件；
    - 用户池：若 users.json 不存在该账号条目则创建，存在则更新 `cookie_file`/`enabled` 等字段。
  - 若用户不传 `account`，则写入/更新 `default`。
- 增量启用：
  - 先把“用户选择”加到 MCP/HTTP（仍只配置一个用户也能跑）。
  - 再引入批量任务模块与新 MCP 工具。
- Docker 场景：
  - 现有 `COOKIES_PATH=/app/data/cookies.json` 仍可用，但建议迁移到 `/app/data/cookies/default.json`。

---

## 9. 交付清单（实现阶段的工作项）

1. 增加 users.json/ip.txt/cookies 目录的解析与路径规则（含兼容）。
2. 浏览器工厂支持：按用户加载 cookies、按用户设置代理、支持浏览器池并发。
3. 登录流程支持指定账号并同步更新用户池/ cookies 池。
4. 业务服务层全部支持 user selector，并保证默认行为不变。
5. MCP 新增：`list_users`、`batch_task_open`、`batch_task_add_post`、`batch_task_run`。
6. HTTP API 新增：`GET /api/v1/batch/tasks/{task_id}`。
7. 增加最小可用的单元测试（用户解析、ip 解析、任务状态机）。
