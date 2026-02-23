package userpool

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManager_EnabledAccounts_OrderPreserved(t *testing.T) {
	tempDir := t.TempDir()
	data := []byte(`{"version":1,"users":[{"account":"b","enabled":true},{"account":"a","enabled":true}]}`)
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "users.json"), data, 0644))

	m, err := NewManager(tempDir)
	require.NoError(t, err)
	require.Equal(t, []string{"b", "a"}, m.EnabledAccounts())

	_, err = m.UpsertCookie("c", "cookies/c.json")
	require.NoError(t, err)
	require.Equal(t, []string{"b", "a", "c"}, m.EnabledAccounts())

	_, err = m.UpsertCookie("a", "cookies/a2.json")
	require.NoError(t, err)
	require.Equal(t, []string{"b", "a", "c"}, m.EnabledAccounts())
}

func TestManager_EnsureSequentialIPRefs_FillsMissingOnly(t *testing.T) {
	tempDir := t.TempDir()
	data := []byte(`{"version":1,"users":[{"account":"u1","enabled":true},{"account":"u2","enabled":true,"ip_ref":99},{"account":"u3","enabled":true}]}`)
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "users.json"), data, 0644))

	m, err := NewManager(tempDir)
	require.NoError(t, err)

	require.NoError(t, m.EnsureSequentialIPRefs(2))

	u, err := m.Resolve("u1", nil)
	require.NoError(t, err)
	require.Equal(t, 0, u.IPRef)

	u, err = m.Resolve("u2", nil)
	require.NoError(t, err)
	require.Equal(t, float64(99), u.IPRef)

	u, err = m.Resolve("u3", nil)
	require.NoError(t, err)
	require.Nil(t, u.IPRef)
}

func TestManager_IndexOfAccount_DefaultFallback(t *testing.T) {
	tempDir := t.TempDir()
	data := []byte(`{"version":1,"users":[{"account":"default","enabled":true},{"account":"u1","enabled":true}]}`)
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "users.json"), data, 0644))

	m, err := NewManager(tempDir)
	require.NoError(t, err)

	idx, ok := m.IndexOfAccount("")
	require.True(t, ok)
	require.Equal(t, 0, idx)
}
