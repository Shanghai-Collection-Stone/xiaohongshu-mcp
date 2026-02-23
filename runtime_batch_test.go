package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xpzouying/xiaohongshu-mcp/modules/userpool"
)

type stubPublisher struct {
	mu       sync.Mutex
	accounts []string
	failOnce bool
}

func (p *stubPublisher) PublishContentForAccount(ctx context.Context, account string, req *PublishRequest) (*PublishResponse, error) {
	p.mu.Lock()
	p.accounts = append(p.accounts, account)
	shouldFail := p.failOnce
	if p.failOnce {
		p.failOnce = false
	}
	p.mu.Unlock()

	if shouldFail {
		return nil, errors.New("publish failed")
	}
	return &PublishResponse{Title: req.Title, Content: req.Content, Images: len(req.Images), Status: "ok"}, nil
}

func (p *stubPublisher) Accounts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.accounts))
	copy(out, p.accounts)
	return out
}

type blockingPublisher struct{}

func (p *blockingPublisher) PublishContentForAccount(ctx context.Context, account string, req *PublishRequest) (*PublishResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestBatchTaskStore_EvictOldest(t *testing.T) {
	store := NewBatchTaskStore(5)
	ids := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		ids = append(ids, store.Create().ID)
	}
	_, ok := store.Snapshot(ids[0])
	require.False(t, ok)
	_, ok = store.Snapshot(ids[5])
	require.True(t, ok)
}

func TestBatchTaskStore_Run_RespectsMaxAccounts(t *testing.T) {
	tempDir := t.TempDir()
	data := []byte(`{"version":1,"users":[{"account":"u1","enabled":true},{"account":"u2","enabled":true},{"account":"u3","enabled":true}]}`)
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "users.json"), data, 0644))

	up, err := userpool.NewManager(tempDir)
	require.NoError(t, err)
	rt := &Runtime{BrowserPoolSize: 4, UserPool: up}

	store := NewBatchTaskStore(5)
	task := store.Create()
	for i := 0; i < 10; i++ {
		require.NoError(t, store.AddPost(task.ID, BatchPost{Title: "t", Content: "c", Images: []string{"/tmp/a.jpg"}}))
	}

	publisher := &stubPublisher{}
	require.NoError(t, store.StartRun(rt, publisher, task.ID, BatchTaskRunConfig{MaxAccounts: 2}))

	deadline := time.Now().Add(2 * time.Second)
	for {
		snap, ok := store.Snapshot(task.ID)
		require.True(t, ok)
		if snap.Status == BatchTaskStatusCompleted || snap.Status == BatchTaskStatusFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for completion: %+v", snap)
		}
		time.Sleep(10 * time.Millisecond)
	}

	for _, a := range publisher.Accounts() {
		require.NotEqual(t, "u3", a)
	}
}

func TestBatchTaskStore_Run_UpdatesProgress(t *testing.T) {
	rt := &Runtime{BrowserPoolSize: 2}
	store := NewBatchTaskStore(5)
	task := store.Create()
	for i := 0; i < 6; i++ {
		require.NoError(t, store.AddPost(task.ID, BatchPost{Title: "t", Content: "c", Images: []string{"/tmp/a.jpg"}}))
	}

	publisher := &stubPublisher{}
	require.NoError(t, store.StartRun(rt, publisher, task.ID, BatchTaskRunConfig{}))

	deadline := time.Now().Add(2 * time.Second)
	for {
		snap, ok := store.Snapshot(task.ID)
		require.True(t, ok)
		if snap.Status == BatchTaskStatusCompleted {
			require.Equal(t, 6, snap.Total)
			require.Equal(t, 6, snap.Done)
			require.Equal(t, 0, snap.Failed)
			break
		}
		if snap.Status == BatchTaskStatusFailed {
			t.Fatalf("unexpected failure: %+v", snap)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for completion: %+v", snap)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBatchTaskStore_Run_ItemTimeoutMarksFailedAndCompletes(t *testing.T) {
	rt := &Runtime{BrowserPoolSize: 1}
	store := NewBatchTaskStore(5)
	task := store.Create()
	require.NoError(t, store.AddPost(task.ID, BatchPost{Title: "t", Content: "c", Images: []string{"/tmp/a.jpg"}}))

	publisher := &blockingPublisher{}
	require.NoError(t, store.StartRun(rt, publisher, task.ID, BatchTaskRunConfig{ItemTimeoutMs: 30}))

	deadline := time.Now().Add(2 * time.Second)
	for {
		snap, ok := store.Snapshot(task.ID)
		require.True(t, ok)
		if snap.Status == BatchTaskStatusFailed || snap.Status == BatchTaskStatusCompleted {
			require.Equal(t, 1, snap.Total)
			require.Equal(t, 0, snap.Done)
			require.Equal(t, 1, snap.Failed)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for completion: %+v", snap)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBatchTaskStore_WaitDone_ReturnsFinalSnapshot(t *testing.T) {
	rt := &Runtime{BrowserPoolSize: 1}
	store := NewBatchTaskStore(5)
	task := store.Create()
	require.NoError(t, store.AddPost(task.ID, BatchPost{Title: "t1", Content: "c", Images: []string{"/tmp/a.jpg"}}))
	require.NoError(t, store.AddPost(task.ID, BatchPost{Title: "t2", Content: "c", Images: []string{"/tmp/a.jpg"}}))

	publisher := &stubPublisher{}
	require.NoError(t, store.StartRun(rt, publisher, task.ID, BatchTaskRunConfig{}))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	snap, err := store.waitDone(ctx, task.ID, 5*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, BatchTaskStatusCompleted, snap.Status)
	require.Equal(t, 2, snap.Total)
	require.Equal(t, 2, snap.Done)
	require.Equal(t, 0, snap.Failed)
}

func TestBatchTaskStore_WaitDone_ContextTimeoutReturnsSnapshot(t *testing.T) {
	rt := &Runtime{BrowserPoolSize: 1}
	store := NewBatchTaskStore(5)
	task := store.Create()
	require.NoError(t, store.AddPost(task.ID, BatchPost{Title: "t", Content: "c", Images: []string{"/tmp/a.jpg"}}))

	publisher := &blockingPublisher{}
	require.NoError(t, store.StartRun(rt, publisher, task.ID, BatchTaskRunConfig{ItemTimeoutMs: 2000}))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	snap, err := store.waitDone(ctx, task.ID, 5*time.Millisecond)
	require.Error(t, err)
	require.Equal(t, BatchTaskStatusRunning, snap.Status)
}
