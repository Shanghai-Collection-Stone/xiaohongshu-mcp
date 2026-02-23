package main

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xpzouying/xiaohongshu-mcp/modules/userpool"
)

func TestRunAccountBatch_ConcurrentRespectsWorkersAndOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	accounts := []string{"a", "b", "c", "d"}
	workers := 2

	var current int32
	var max int32
	started := make(chan struct{}, len(accounts))
	block := make(chan struct{})

	checkFn := func(ctx context.Context, account string) (*LoginStatusResponse, error) {
		n := atomic.AddInt32(&current, 1)
		for {
			m := atomic.LoadInt32(&max)
			if n <= m {
				break
			}
			if atomic.CompareAndSwapInt32(&max, m, n) {
				break
			}
		}

		select {
		case started <- struct{}{}:
		case <-ctx.Done():
			atomic.AddInt32(&current, -1)
			return nil, ctx.Err()
		}

		select {
		case <-block:
		case <-ctx.Done():
			atomic.AddInt32(&current, -1)
			return nil, ctx.Err()
		}

		atomic.AddInt32(&current, -1)
		return &LoginStatusResponse{IsLoggedIn: true, Username: account}, nil
	}

	done := make(chan []accountBatchResult[*LoginStatusResponse], 1)
	go func() {
		res := runAccountBatch(ctx, accounts, workers, checkFn)
		done <- res
	}()

	for i := 0; i < workers; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatalf("timeout waiting for workers to start")
		}
	}
	close(block)

	var res []accountBatchResult[*LoginStatusResponse]
	select {
	case res = <-done:
	case <-ctx.Done():
		t.Fatalf("timeout waiting for results")
	}

	require.Equal(t, int32(workers), atomic.LoadInt32(&max))
	require.Len(t, res, len(accounts))
	for i, a := range accounts {
		require.Equal(t, a, res[i].Account)
		require.NoError(t, res[i].Err)
		require.NotNil(t, res[i].Value)
		require.True(t, res[i].Value.IsLoggedIn)
		require.Equal(t, a, res[i].Value.Username)
	}
}

func TestLimitSearchBatchAccounts_MaxThreeAndAllowSmaller(t *testing.T) {
	accounts := []string{"u1", "u2", "u3", "u4", "u5"}
	require.Equal(t, []string{"u1", "u2", "u3"}, limitSearchBatchAccounts(accounts, 0))
	require.Equal(t, []string{"u1", "u2"}, limitSearchBatchAccounts(accounts, 2))
	require.Equal(t, []string{"u1", "u2", "u3"}, limitSearchBatchAccounts(accounts, 10))
}

func TestResolveTargetAccounts_PriorityAndFallback(t *testing.T) {
	tempDir := t.TempDir()
	data := []byte(`{"version":1,"users":[{"account":"u1","enabled":true},{"account":"u2","enabled":true},{"account":"u3","enabled":false}]}`)
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "users.json"), data, 0644))

	up, err := userpool.NewManager(tempDir)
	require.NoError(t, err)

	s := &AppServer{runtime: &Runtime{UserPool: up}}

	got := s.resolveTargetAccounts(TargetUsers{Accounts: []string{" u2 ", "u2", "u1"}})
	require.Equal(t, []string{"u2", "u1"}, got)

	got = s.resolveTargetAccounts(TargetUsers{Indices: []int{1, 0, 1}})
	require.Equal(t, []string{"u2", "u1"}, got)

	got = s.resolveTargetAccounts(TargetUsers{})
	require.Equal(t, []string{"u1", "u2"}, got)

	got = (&AppServer{runtime: &Runtime{UserPool: &userpool.Manager{}}}).resolveTargetAccounts(TargetUsers{AllEnabled: true})
	require.Equal(t, []string{"default"}, got)
}
