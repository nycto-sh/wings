package metrics

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRingKeepsTheNewestSamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.bin")
	r := New(path, 60, 3)

	for _, ts := range []int64{600, 660, 720, 780} {
		require.NoError(t, r.Write(Sample{Time: ts, CPUAvg: float32(ts)}))
	}

	got, err := r.Read(0, 10_000)
	require.NoError(t, err)
	require.Len(t, got, 3, "the oldest sample is overwritten")
	require.Equal(t, []int64{660, 720, 780}, []int64{got[0].Time, got[1].Time, got[2].Time})

	got, err = r.Read(700, 750)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, float32(720), got[0].CPUAvg)

	st, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, int64(headerSize+3*recordSize), st.Size(), "the file has a fixed size")
}

func TestRingRoundTrip(t *testing.T) {
	r := New(filepath.Join(t.TempDir(), "server.bin"), 60, 10)
	want := Sample{
		Time: 1_791_842_400, CPUAvg: 12.5, CPUMax: 87.25,
		MemoryAvg: 1 << 30, MemoryMax: 3 << 29, Disk: 5 << 30,
		NetworkRx: 123456, NetworkTx: 654321,
	}
	require.NoError(t, r.Write(want))
	got, err := r.Read(want.Time, want.Time)
	require.NoError(t, err)
	require.Equal(t, []Sample{want}, got)
}

// A file made with other settings, or a damaged one, holds no history and is
// started again when written to.
func TestRingWithOtherSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.bin")
	require.NoError(t, New(path, 60, 10).Write(Sample{Time: 600}))

	r := New(path, 60, 20)
	got, err := r.Read(0, 10_000)
	require.NoError(t, err)
	require.Empty(t, got)

	require.NoError(t, r.Write(Sample{Time: 660}))
	got, err = r.Read(0, 10_000)
	require.NoError(t, err)
	require.Len(t, got, 1)

	require.NoError(t, os.WriteFile(path, []byte("garbage"), 0o600))
	got, err = r.Read(0, 10_000)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestRingWithoutFile(t *testing.T) {
	r := New(filepath.Join(t.TempDir(), "missing.bin"), 60, 10)
	got, err := r.Read(0, 10_000)
	require.NoError(t, err)
	require.Empty(t, got)
	require.NoError(t, r.Remove())
}

func TestSummarize(t *testing.T) {
	samples := []Sample{
		{Time: 0, CPUAvg: 10, CPUMax: 30, MemoryAvg: 100, MemoryMax: 150, Disk: 1000, NetworkRx: 1, NetworkTx: 2},
		{Time: 60, CPUAvg: 20, CPUMax: 25, MemoryAvg: 300, MemoryMax: 400, Disk: 1100, NetworkRx: 3, NetworkTx: 4},
		// Nothing at 120 and 180, for example while the server was offline.
		{Time: 240, CPUAvg: 33.3, CPUMax: 50, MemoryAvg: 200, MemoryMax: 200, Disk: 1200},
	}

	points := Summarize(samples, 0, 299, 60, 3)
	require.Equal(t, []Point{
		{Time: 0, Span: 120, CPUAvg: 15, CPUMax: 30, MemoryAvg: 200, MemoryMax: 400, Disk: 1100, NetworkRx: 4, NetworkTx: 6},
		{Time: 240, Span: 120, CPUAvg: 33.3, CPUMax: 50, MemoryAvg: 200, MemoryMax: 200, Disk: 1200},
	}, points)

	// With enough points, every sample is its own point.
	require.Len(t, Summarize(samples, 0, 299, 60, 100), 3)
	require.Equal(t, []Point{}, Summarize(nil, 0, 299, 60, 10))
}
