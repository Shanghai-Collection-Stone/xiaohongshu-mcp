package cookies

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocalCookie_LoadCookies_MissingFileReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "cookies.json")
	c := NewLoadCookie(path)
	data, err := c.LoadCookies()
	require.NoError(t, err)
	require.Len(t, data, 0)
}

func TestLocalCookie_SaveCookies_CreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "cookies.json")
	c := NewLoadCookie(path)
	require.NoError(t, c.SaveCookies([]byte("[]")))
	_, err := os.Stat(path)
	require.NoError(t, err)
}
