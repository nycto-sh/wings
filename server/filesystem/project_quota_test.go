package filesystem

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nycto-sh/wings/config"
	"github.com/nycto-sh/wings/internal/quota"
)

// Without project quotas, the files keep being counted.
func TestProjectQuotaNotSupported(t *testing.T) {
	fs, rfs := NewFs()
	t.Cleanup(func() { _ = os.RemoveAll(rfs.root) })
	if fs.ProjectQuotaSupported() {
		t.Skip("the temporary directory is on a filesystem with project quotas")
	}

	require.Error(t, fs.UseProjectQuota(quota.FirstID, false))
	require.False(t, fs.UsesProjectQuota())

	require.NoError(t, rfs.CreateServerFile("a", randomData(t, 4096)))
	usage, err := fs.DiskUsage(false)
	require.NoError(t, err)
	require.Equal(t, int64(4096), usage)
}

// TestProjectQuotaUsage needs a directory on a filesystem with project quotas
// enabled, set in NYCTO_QUOTA_TEST_DIR, and has to run as root.
func TestProjectQuotaUsage(t *testing.T) {
	root := os.Getenv("NYCTO_QUOTA_TEST_DIR")
	if root == "" {
		t.Skip("NYCTO_QUOTA_TEST_DIR is not set")
	}
	config.Set(&config.Configuration{
		AuthenticationToken: "abc",
		System:              config.SystemConfiguration{DiskCheckInterval: 150},
	})
	dir, err := os.MkdirTemp(root, "fs-quota-test-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// Files that exist before the project quota is used are given the project ID.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "world", "region"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "world", "region", "r.0.0.mca"), make([]byte, 1<<20), 0o600))
	require.NoError(t, os.Symlink("/etc", filepath.Join(dir, "link")))

	fs, err := New(dir, 0, []string{})
	require.NoError(t, err)
	require.True(t, fs.ProjectQuotaSupported())
	require.NoError(t, fs.UseProjectQuota(quota.FirstID-2, false))
	require.True(t, fs.UsesProjectQuota())

	usage, err := fs.DiskUsage(false)
	require.NoError(t, err)
	require.GreaterOrEqual(t, usage, int64(1<<20))

	// Files written later, here without going through Wings like a running server
	// would, are counted straight away without counting the files again.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "world", "level.dat"), make([]byte, 2<<20), 0o600))
	f, err := os.Open(dir)
	require.NoError(t, err)
	require.NoError(t, f.Sync())
	require.NoError(t, f.Close())
	usage, err = fs.DiskUsage(true)
	require.NoError(t, err)
	require.GreaterOrEqual(t, usage, int64(3<<20))

	// The symbolic link was not followed.
	id, err := fs.ProjectID()
	require.NoError(t, err)
	require.Equal(t, quota.FirstID-2, id)
}
