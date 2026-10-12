package filesystem

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nycto-sh/wings/config"
)

// Once the server's process has stopped and the files have been counted, the
// usage stays accurate without counting them again, since every other change
// goes through Wings.
func TestUsageIsCurrentWhileProcessIsStopped(t *testing.T) {
	fs, rfs := NewFs()
	t.Cleanup(func() { _ = os.RemoveAll(rfs.root) })

	require.NoError(t, rfs.CreateServerFile("a", randomData(t, 4096)))
	require.False(t, fs.UsageIsCurrent(), "the files have never been counted")

	usage, err := fs.DiskUsage(false)
	require.NoError(t, err)
	require.Equal(t, int64(4096), usage)
	require.True(t, fs.UsageIsCurrent())

	// The process changes files while it runs, so they have to be counted again
	// once it stops.
	fs.ProcessStarted()
	require.False(t, fs.UsageIsCurrent())
	require.NoError(t, rfs.CreateServerFile("b", randomData(t, 4096)))
	fs.ProcessStopped()
	require.Eventually(t, fs.UsageIsCurrent, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, int64(8192), fs.CachedUsage())

	// While the process is stopped, the files are not counted again however old
	// the count is.
	fs.lastLookupTime.Set(time.Now().Add(-24 * time.Hour))
	require.NoError(t, rfs.CreateServerFile("c", randomData(t, 4096)))
	usage, err = fs.DiskUsage(false)
	require.NoError(t, err)
	require.Equal(t, int64(8192), usage, "the files must not be counted again")
}

// A walk that started before the process stopped may have missed its last
// changes, so it does not make the usage current.
func TestWalkBeforeStopIsNotCurrent(t *testing.T) {
	fs, rfs := NewFs()
	t.Cleanup(func() { _ = os.RemoveAll(rfs.root) })

	_, err := fs.DiskUsage(false)
	require.NoError(t, err)
	fs.ProcessStarted()
	fs.walkStartedAt.Store(time.Now().UnixNano())
	fs.stoppedAt.Store(time.Now().Add(time.Second).UnixNano())
	fs.processRunning.Store(false)
	require.False(t, fs.UsageIsCurrent())
}

// A failed walk does not make the usage current.
func TestFailedWalkIsNotCurrent(t *testing.T) {
	fs, rfs := NewFs()
	t.Cleanup(func() { _ = os.RemoveAll(rfs.root) })

	require.NoError(t, os.RemoveAll(filepath.Join(rfs.root, "server")))
	_, err := fs.DiskUsage(false)
	require.Error(t, err)
	require.False(t, fs.UsageIsCurrent())
}

func TestAdaptiveCheckInterval(t *testing.T) {
	fs, rfs := NewFs()
	t.Cleanup(func() { _ = os.RemoveAll(rfs.root) })
	base := 150 * time.Second

	require.Equal(t, base, fs.checkInterval(), "adaptive checking is off")

	fs.adaptiveCheck = true
	require.Equal(t, 4*base, fs.checkInterval(), "a server without a disk limit")

	fs.SetDiskLimit(1000)
	fs.unixFS.SetUsage(100)
	require.Equal(t, 4*base, fs.checkInterval(), "below a quarter of the limit")
	fs.unixFS.SetUsage(300)
	require.Equal(t, 2*base, fs.checkInterval(), "below half of the limit")
	fs.unixFS.SetUsage(600)
	require.Equal(t, base, fs.checkInterval(), "above half of the limit")
}

func TestAdaptiveDiskCheckIsOnByDefault(t *testing.T) {
	cfg, err := config.NewAtPath(filepath.Join(t.TempDir(), "config.yml"))
	require.NoError(t, err)
	require.True(t, cfg.System.AdaptiveDiskCheck)
	require.Equal(t, int64(150), cfg.System.DiskCheckInterval)
	require.Equal(t, "auto", cfg.System.DiskQuota.Mode)
	require.False(t, cfg.System.DiskQuota.Enforce)
}
