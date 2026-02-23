package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xpzouying/xiaohongshu-mcp/modules/cookiestore"
	"github.com/xpzouying/xiaohongshu-mcp/modules/userpool"
)

func TestXiaohongshuService_EffectiveAccount_UsesFirstEnabledUser(t *testing.T) {
	tempDir := t.TempDir()
	data := []byte(`{"version":1,"users":[{"account":"u1","enabled":true},{"account":"u2","enabled":true}]}`)
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "users.json"), data, 0644))

	up, err := userpool.NewManager(tempDir)
	require.NoError(t, err)
	rt := &Runtime{UserPool: up}

	s := &XiaohongshuService{runtime: rt}
	require.Equal(t, "u1", s.effectiveAccount(""))
}

func TestXiaohongshuService_ResolveCookieAndProxy_UsesProxyWhenAvailableOtherwiseDirect(t *testing.T) {
	tempDir := t.TempDir()
	users := []byte(`{"version":1,"users":[{"account":"u1","enabled":true},{"account":"u2","enabled":true}]}`)
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "users.json"), users, 0644))
	ip := []byte("http://127.0.0.1:8080\n")
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "ip.txt"), ip, 0644))

	rt, err := NewRuntime(tempDir, 1)
	require.NoError(t, err)

	s := &XiaohongshuService{runtime: rt, cookieStore: cookiestore.NewStore(tempDir)}

	_, proxy, err := s.resolveCookieAndProxy("u1")
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:8080", proxy)

	_, proxy, err = s.resolveCookieAndProxy("u2")
	require.NoError(t, err)
	require.Equal(t, "", proxy)
}
