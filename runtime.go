package main

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	randv2 "math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/modules/cookiestore"
	"github.com/xpzouying/xiaohongshu-mcp/modules/ippool"
	"github.com/xpzouying/xiaohongshu-mcp/modules/userpool"
)

func shortenOneLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.TrimSpace(s)
	if max <= 0 {
		return s
	}
	if len(s) <= max {
		return s
	}
	return s[:max]
}

type Runtime struct {
	DataDir         string
	BrowserPoolSize int

	UserPool    *userpool.Manager
	IPPool      *ippool.Pool
	CookieStore *cookiestore.Store
	BatchTasks  *BatchTaskStore

	browserTokens chan struct{}
	accountLocks  sync.Map
}

func NewRuntime(dataDir string, browserPoolSize int) (*Runtime, error) {
	if dataDir == "" {
		dataDir = "."
	}
	if browserPoolSize < 1 {
		browserPoolSize = 1
	}

	up, err := userpool.NewManager(dataDir)
	if err != nil {
		return nil, err
	}
	ip, err := ippool.NewPool(dataDir)
	if err != nil {
		return nil, err
	}
	if up != nil {
		ips := ip.All()
		if err := up.EnsureSequentialIPRefs(len(ips)); err != nil {
			return nil, err
		}
	}
	cs := cookiestore.NewStore(dataDir)

	r := &Runtime{
		DataDir:         dataDir,
		BrowserPoolSize: browserPoolSize,
		UserPool:        up,
		IPPool:          ip,
		CookieStore:     cs,
		BatchTasks:      NewBatchTaskStore(5),
		browserTokens:   make(chan struct{}, browserPoolSize),
	}
	for i := 0; i < browserPoolSize; i++ {
		r.browserTokens <- struct{}{}
	}
	return r, nil
}

func (r *Runtime) AcquireBrowser(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.browserTokens:
		return nil
	}
}

func (r *Runtime) ReleaseBrowser() {
	select {
	case r.browserTokens <- struct{}{}:
	default:
	}
}

func (r *Runtime) AccountLock(account string) (*sync.Mutex, error) {
	if account == "" {
		return nil, errors.New("empty account")
	}
	v, _ := r.accountLocks.LoadOrStore(account, &sync.Mutex{})
	mu, ok := v.(*sync.Mutex)
	if !ok {
		return nil, errors.New("invalid account lock")
	}
	return mu, nil
}

type BatchTaskStatus string

const (
	BatchTaskStatusDraft     BatchTaskStatus = "draft"
	BatchTaskStatusRunning   BatchTaskStatus = "running"
	BatchTaskStatusCompleted BatchTaskStatus = "completed"
	BatchTaskStatusFailed    BatchTaskStatus = "failed"
)

type BatchPost struct {
	Title      string   `json:"title"`
	Content    string   `json:"content"`
	Images     []string `json:"images"`
	Tags       []string `json:"tags,omitempty"`
	Location   string   `json:"location,omitempty"`
	ScheduleAt string   `json:"schedule_at,omitempty"`
}

type BatchTaskRunConfig struct {
	Targets       TargetUsers `json:"targets,omitempty"`
	CallbackURL   string      `json:"callback_url,omitempty"`
	MinDelayMs    int         `json:"min_delay_ms,omitempty"`
	MaxDelayMs    int         `json:"max_delay_ms,omitempty"`
	MaxAccounts   int         `json:"max_accounts,omitempty"`
	ItemTimeoutMs int         `json:"item_timeout_ms,omitempty"`
}

type BatchTask struct {
	ID        string             `json:"id"`
	Status    BatchTaskStatus    `json:"status"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
	Total     int                `json:"total"`
	Done      int                `json:"done"`
	Failed    int                `json:"failed"`
	Error     string             `json:"error,omitempty"`
	Config    BatchTaskRunConfig `json:"config"`
	Items     []BatchPost        `json:"-"`
}

type BatchTaskSnapshot struct {
	ID        string             `json:"id"`
	Status    BatchTaskStatus    `json:"status"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
	Total     int                `json:"total"`
	Done      int                `json:"done"`
	Failed    int                `json:"failed"`
	Error     string             `json:"error,omitempty"`
	Config    BatchTaskRunConfig `json:"config"`
}

type BatchPublisher interface {
	PublishContentForAccount(ctx context.Context, account string, req *PublishRequest) (*PublishResponse, error)
}

type BatchTaskStore struct {
	cap   int
	mu    sync.Mutex
	order []string
	tasks map[string]*BatchTask
}

func NewBatchTaskStore(capacity int) *BatchTaskStore {
	if capacity < 1 {
		capacity = 1
	}
	return &BatchTaskStore{cap: capacity, order: nil, tasks: make(map[string]*BatchTask)}
}

func (s *BatchTaskStore) Create() *BatchTask {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.order) >= s.cap {
		oldest := s.order[0]
		delete(s.tasks, oldest)
		s.order = s.order[1:]
	}

	id := newBatchTaskID()
	now := time.Now()
	t := &BatchTask{ID: id, Status: BatchTaskStatusDraft, CreatedAt: now, UpdatedAt: now, Total: 0, Done: 0, Failed: 0, Items: nil}
	s.tasks[id] = t
	s.order = append(s.order, id)
	return t
}

func (s *BatchTaskStore) AddPost(taskID string, post BatchPost) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.tasks[taskID]
	if !ok {
		return errors.New("task not found")
	}
	if t.Status != BatchTaskStatusDraft {
		return errors.New("task is not draft")
	}
	t.Items = append(t.Items, post)
	t.Total = len(t.Items)
	t.UpdatedAt = time.Now()
	return nil
}

func (s *BatchTaskStore) Snapshot(taskID string) (BatchTaskSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.tasks[taskID]
	if !ok {
		return BatchTaskSnapshot{}, false
	}
	return BatchTaskSnapshot{
		ID:        t.ID,
		Status:    t.Status,
		CreatedAt: t.CreatedAt,
		UpdatedAt: t.UpdatedAt,
		Total:     t.Total,
		Done:      t.Done,
		Failed:    t.Failed,
		Error:     t.Error,
		Config:    t.Config,
	}, true
}

func (s *BatchTaskStore) waitDone(ctx context.Context, taskID string, pollInterval time.Duration) (BatchTaskSnapshot, error) {
	if pollInterval <= 0 {
		pollInterval = 500 * time.Millisecond
	}
	if _, ok := s.Snapshot(taskID); !ok {
		return BatchTaskSnapshot{}, errors.New("task not found")
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		if err := ctx.Err(); err != nil {
			snap, _ := s.Snapshot(taskID)
			return snap, err
		}

		snap, ok := s.Snapshot(taskID)
		if !ok {
			return BatchTaskSnapshot{}, errors.New("task not found")
		}
		if snap.Status == BatchTaskStatusCompleted || snap.Status == BatchTaskStatusFailed {
			return snap, nil
		}

		select {
		case <-ctx.Done():
			snap, _ := s.Snapshot(taskID)
			return snap, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *BatchTaskStore) StartRun(runtime *Runtime, publisher BatchPublisher, taskID string, cfg BatchTaskRunConfig) error {
	if runtime == nil || publisher == nil {
		return errors.New("missing runtime or publisher")
	}

	s.mu.Lock()
	t, ok := s.tasks[taskID]
	if !ok {
		s.mu.Unlock()
		return errors.New("task not found")
	}
	if t.Status != BatchTaskStatusDraft {
		s.mu.Unlock()
		return errors.New("task is not draft")
	}
	if len(t.Items) == 0 {
		s.mu.Unlock()
		return errors.New("task has no posts")
	}

	if cfg.MinDelayMs < 0 {
		cfg.MinDelayMs = 0
	}
	if cfg.MaxDelayMs < 0 {
		cfg.MaxDelayMs = 0
	}
	if cfg.MaxDelayMs > 0 && cfg.MinDelayMs > cfg.MaxDelayMs {
		cfg.MinDelayMs, cfg.MaxDelayMs = cfg.MaxDelayMs, cfg.MinDelayMs
	}
	if cfg.ItemTimeoutMs < 0 {
		cfg.ItemTimeoutMs = 0
	}

	t.Config = cfg
	t.Status = BatchTaskStatusRunning
	t.UpdatedAt = time.Now()
	items := append([]BatchPost(nil), t.Items...)
	logrus.WithFields(logrus.Fields{
		"task_id":           taskID,
		"total":             len(items),
		"max_accounts":      cfg.MaxAccounts,
		"browser_pool_size": runtime.BrowserPoolSize,
		"min_delay_ms":      cfg.MinDelayMs,
		"max_delay_ms":      cfg.MaxDelayMs,
		"item_timeout_ms":   cfg.ItemTimeoutMs,
		"callback_set":      cfg.CallbackURL != "",
	}).Info("batch: task started")
	s.mu.Unlock()

	go func() {
		s.run(runtime, publisher, taskID, items, cfg)
	}()

	return nil
}

func (s *BatchTaskStore) run(runtime *Runtime, publisher BatchPublisher, taskID string, items []BatchPost, cfg BatchTaskRunConfig) {
	itemTimeout := 6 * time.Minute
	if cfg.ItemTimeoutMs > 0 {
		itemTimeout = time.Duration(cfg.ItemTimeoutMs) * time.Millisecond
	}

	accounts := resolveBatchRunAccounts(runtime, cfg.Targets)
	if cfg.MaxAccounts > 0 && cfg.MaxAccounts < len(accounts) {
		accounts = accounts[:cfg.MaxAccounts]
	}
	if len(accounts) == 0 {
		accounts = []string{"default"}
	}
	if len(accounts) == 1 {
		logrus.WithFields(logrus.Fields{
			"task_id":        taskID,
			"account":        accounts[0],
			"max_accounts":   cfg.MaxAccounts,
			"targets_set":    cfg.Targets.AllEnabled || len(cfg.Targets.Accounts) > 0 || len(cfg.Targets.Indices) > 0,
			"targets":        summarizeTargets(cfg.Targets),
			"userpool_ready": runtime != nil && runtime.UserPool != nil,
		}).Warn("batch: only one account selected, rotation will not take effect")
	}

	workers := runtime.BrowserPoolSize
	if workers < 1 {
		workers = 1
	}

	logrus.WithFields(logrus.Fields{
		"task_id":         taskID,
		"accounts":        accounts,
		"workers":         workers,
		"total":           len(items),
		"item_timeout_ms": int(itemTimeout / time.Millisecond),
		"delay_ms":        []int{cfg.MinDelayMs, cfg.MaxDelayMs},
	}).Info("batch: run begin")
	if workers > len(items) {
		workers = len(items)
	}
	if cfg.MaxAccounts > 0 && workers > cfg.MaxAccounts {
		workers = cfg.MaxAccounts
	}
	if workers > len(accounts) {
		workers = len(accounts)
	}
	if workers < 1 {
		workers = 1
	}

	s.sendCallback(cfg.CallbackURL, taskID)

	type job struct {
		idx  int
		post BatchPost
	}
	jobs := make(chan job, len(items))
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		for j := range jobs {
			startedAt := time.Now()
			account := accounts[j.idx%len(accounts)]
			req := &PublishRequest{Title: j.post.Title, Content: j.post.Content, Images: j.post.Images, Tags: j.post.Tags, Location: j.post.Location, ScheduleAt: j.post.ScheduleAt}
			ctx, cancel := context.WithTimeout(context.Background(), itemTimeout)
			logrus.WithFields(logrus.Fields{
				"task_id":       taskID,
				"idx":           j.idx,
				"accounts_len":  len(accounts),
				"account_idx":   j.idx % len(accounts),
				"account":       account,
				"title":         shortenOneLine(req.Title, 32),
				"images":        len(req.Images),
				"tags":          len(req.Tags),
				"schedule_at":   shortenOneLine(req.ScheduleAt, 64),
				"location_set":  strings.TrimSpace(req.Location) != "",
				"callback_set":  cfg.CallbackURL != "",
				"delay_ms":      []int{cfg.MinDelayMs, cfg.MaxDelayMs},
				"max_accounts":  cfg.MaxAccounts,
				"targets":       summarizeTargets(cfg.Targets),
				"userpool_file": userPoolFilePath(runtime),
			}).Info("batch: publish begin")
			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						err = fmt.Errorf("panic: %v", r)
					}
				}()
				_, err = publisher.PublishContentForAccount(ctx, account, req)
			}()
			cancel()
			durationMs := int(time.Since(startedAt) / time.Millisecond)
			s.mu.Lock()
			t, ok := s.tasks[taskID]
			if ok {
				if err != nil {
					t.Failed++
					t.Error = err.Error()
				} else {
					t.Done++
				}
				t.UpdatedAt = time.Now()
			}
			s.mu.Unlock()
			if err != nil {
				typeLabel := "error"
				if errors.Is(err, context.DeadlineExceeded) {
					typeLabel = "timeout"
				}
				logrus.WithFields(logrus.Fields{
					"task_id":       taskID,
					"idx":           j.idx,
					"account":       account,
					"duration_ms":   durationMs,
					"error_type":    typeLabel,
					"error_message": err.Error(),
				}).Warn("batch: publish failed")
			} else {
				logrus.WithFields(logrus.Fields{
					"task_id":     taskID,
					"idx":         j.idx,
					"account":     account,
					"duration_ms": durationMs,
				}).Info("batch: publish success")
			}
			s.sendCallback(cfg.CallbackURL, taskID)

			delayMs := randomDelayMs(cfg.MinDelayMs, cfg.MaxDelayMs)
			if delayMs > 0 {
				time.Sleep(time.Duration(delayMs) * time.Millisecond)
			}
		}
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go worker()
	}

	for i, it := range items {
		jobs <- job{idx: i, post: it}
	}
	close(jobs)
	wg.Wait()

	s.mu.Lock()
	if t, ok := s.tasks[taskID]; ok {
		t.UpdatedAt = time.Now()
		if t.Failed > 0 {
			t.Status = BatchTaskStatusFailed
		} else {
			t.Status = BatchTaskStatusCompleted
		}
	}
	s.mu.Unlock()

	snap, ok := s.Snapshot(taskID)
	if ok {
		logrus.WithFields(logrus.Fields{
			"task_id": taskID,
			"status":  snap.Status,
			"total":   snap.Total,
			"done":    snap.Done,
			"failed":  snap.Failed,
			"error":   shortenOneLine(snap.Error, 200),
		}).Info("batch: run end")
	}

	s.sendCallback(cfg.CallbackURL, taskID)
}

func resolveBatchRunAccounts(runtime *Runtime, targets TargetUsers) []string {
	if runtime == nil || runtime.UserPool == nil {
		if targets.AllEnabled || len(targets.Accounts) > 0 || len(targets.Indices) > 0 {
			logrus.WithFields(logrus.Fields{
				"targets": summarizeTargets(targets),
			}).Warn("batch: userpool not ready, fall back to default account")
		}
		return []string{"default"}
	}

	summaries := runtime.UserPool.ListSummaries()
	enabled := runtime.UserPool.EnabledAccounts()
	logrus.WithFields(logrus.Fields{
		"targets":        summarizeTargets(targets),
		"userpool_file":  runtime.UserPool.FilePath(),
		"users_total":    len(summaries),
		"users_enabled":  len(enabled),
		"enabled_sample": shortenStringSlice(enabled, 10),
	}).Info("batch: resolve accounts begin")

	var out []string
	seen := make(map[string]struct{})

	appendAccount := func(a string) {
		a = strings.TrimSpace(a)
		if a == "" {
			return
		}
		if _, ok := seen[a]; ok {
			return
		}
		seen[a] = struct{}{}
		out = append(out, a)
	}

	if len(targets.Accounts) > 0 {
		for _, a := range targets.Accounts {
			appendAccount(a)
		}
		if len(out) > 0 {
			logrus.WithFields(logrus.Fields{
				"source":   "targets.accounts",
				"accounts": out,
			}).Info("batch: resolve accounts done")
			return out
		}
	}

	if len(targets.Indices) > 0 {
		for _, idx := range targets.Indices {
			i := idx
			u, err := runtime.UserPool.Resolve("", &i)
			if err != nil {
				continue
			}
			appendAccount(u.Account)
		}
		if len(out) > 0 {
			logrus.WithFields(logrus.Fields{
				"source":   "targets.indices",
				"accounts": out,
			}).Info("batch: resolve accounts done")
			return out
		}
	}

	if targets.AllEnabled || (len(targets.Accounts) == 0 && len(targets.Indices) == 0) {
		for _, a := range runtime.UserPool.EnabledAccounts() {
			appendAccount(a)
		}
	}

	if len(out) == 0 {
		logrus.WithFields(logrus.Fields{
			"source": "fallback_default",
		}).Warn("batch: resolve accounts empty, fall back to default account")
		return []string{"default"}
	}
	logrus.WithFields(logrus.Fields{
		"source":   "userpool.enabled_accounts",
		"accounts": out,
	}).Info("batch: resolve accounts done")
	return out
}

func shortenStringSlice(items []string, maxItems int) []string {
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
		out = append(out, shortenOneLine(items[i], 120))
	}
	return out
}

func summarizeTargets(t TargetUsers) map[string]any {
	out := map[string]any{
		"all_enabled": t.AllEnabled,
	}
	if len(t.Accounts) > 0 {
		out["accounts"] = shortenStringSlice(t.Accounts, 10)
		out["accounts_len"] = len(t.Accounts)
	}
	if len(t.Indices) > 0 {
		out["indices"] = t.Indices
		out["indices_len"] = len(t.Indices)
	}
	return out
}

func userPoolFilePath(runtime *Runtime) string {
	if runtime == nil || runtime.UserPool == nil {
		return ""
	}
	return runtime.UserPool.FilePath()
}

func (s *BatchTaskStore) sendCallback(callbackURL string, taskID string) {
	if callbackURL == "" {
		return
	}
	snap, ok := s.Snapshot(taskID)
	if !ok {
		return
	}
	body, err := json.Marshal(snap)
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, callbackURL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}

func newBatchTaskID() string {
	b := make([]byte, 16)
	if _, err := crand.Read(b); err == nil {
		return hex.EncodeToString(b)
	}
	return hex.EncodeToString([]byte(time.Now().Format("20060102150405.000000000")))
}

func randomDelayMs(minMs int, maxMs int) int {
	if minMs <= 0 && maxMs <= 0 {
		return 0
	}
	if maxMs <= 0 {
		return minMs
	}
	if minMs < 0 {
		minMs = 0
	}
	if maxMs < minMs {
		minMs, maxMs = maxMs, minMs
	}
	if minMs == maxMs {
		return minMs
	}
	return minMs + randv2.IntN(maxMs-minMs+1)
}
