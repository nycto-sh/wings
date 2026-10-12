// Package metrics keeps a rolling history of a server's resource usage in a
// small file of fixed size, so that graphs can be drawn without a database.
//
// The file starts with a header, followed by a fixed number of slots. A sample
// taken at time t is written to slot (t / interval) % slots, so the newest
// samples overwrite the oldest without any bookkeeping, and a crash can never
// leave the file in a state that needs repairing. A slot whose sample is older
// than the history kept is simply ignored.
//
// @see docs/metrics.md.
package metrics

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const (
	magic      = "NYCTOMTR"
	version    = 1
	headerSize = 32
	recordSize = 56
)

// Sample is the resource usage of a server over one interval.
type Sample struct {
	// Time is the start of the interval, in Unix seconds.
	Time int64
	// CPUAvg and CPUMax are the average and highest CPU usage, where 100 is one
	// CPU core.
	CPUAvg float32
	CPUMax float32
	// MemoryAvg and MemoryMax are the average and highest memory usage, in bytes.
	MemoryAvg uint64
	MemoryMax uint64
	// Disk is the disk usage at the end of the interval, in bytes.
	Disk uint64
	// NetworkRx and NetworkTx are the bytes received and sent during the
	// interval.
	NetworkRx uint64
	NetworkTx uint64
}

// Ring is the history file of one server. It is safe for concurrent use.
type Ring struct {
	mu       sync.Mutex
	path     string
	interval int64
	slots    int64
}

// New returns the history file at path, holding slots samples taken every
// interval seconds. The file is created when the first sample is written. If
// it was created with a different interval or number of slots, its history is
// discarded.
func New(path string, interval int64, slots int64) *Ring {
	return &Ring{path: path, interval: max(interval, 1), slots: max(slots, 1)}
}

func (r *Ring) header() []byte {
	h := make([]byte, headerSize)
	copy(h, magic)
	binary.LittleEndian.PutUint32(h[8:], version)
	binary.LittleEndian.PutUint32(h[12:], uint32(r.interval))
	binary.LittleEndian.PutUint32(h[16:], uint32(r.slots))
	return h
}

func (r *Ring) size() int64 {
	return headerSize + r.slots*recordSize
}

// Write stores the sample in its slot.
func (r *Ring) Write(s Sample) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	// Start again with an empty history if the file is new or was made with
	// different settings.
	h := make([]byte, headerSize)
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if _, err := f.ReadAt(h, 0); err != nil || !bytes.Equal(h, r.header()) || st.Size() != r.size() {
		if err := f.Truncate(0); err != nil {
			return err
		}
		if err := f.Truncate(r.size()); err != nil {
			return err
		}
		if _, err := f.WriteAt(r.header(), 0); err != nil {
			return err
		}
	}

	_, err = f.WriteAt(encode(s), headerSize+r.slot(s.Time)*recordSize)
	return err
}

func (r *Ring) slot(t int64) int64 {
	n := (t / r.interval) % r.slots
	if n < 0 {
		n += r.slots
	}
	return n
}

// Read returns the samples taken from "from" to "to", inclusive, in order. A
// missing or unreadable file has no samples.
func (r *Ring) Read(from, to int64) ([]Sample, error) {
	r.mu.Lock()
	b, err := os.ReadFile(r.path)
	r.mu.Unlock()
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != r.size() || !bytes.Equal(b[:headerSize], r.header()) {
		return nil, nil
	}

	var out []Sample
	for i := int64(0); i < r.slots; i++ {
		s := decode(b[headerSize+i*recordSize : headerSize+(i+1)*recordSize])
		// An empty slot, or one holding a sample that does not belong in it.
		if s.Time <= 0 || r.slot(s.Time) != i {
			continue
		}
		if s.Time >= from && s.Time <= to {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out, nil
}

// Remove deletes the history file.
func (r *Ring) Remove() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := os.Remove(r.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func encode(s Sample) []byte {
	b := make([]byte, recordSize)
	binary.LittleEndian.PutUint64(b[0:], uint64(s.Time))
	binary.LittleEndian.PutUint32(b[8:], math.Float32bits(s.CPUAvg))
	binary.LittleEndian.PutUint32(b[12:], math.Float32bits(s.CPUMax))
	binary.LittleEndian.PutUint64(b[16:], s.MemoryAvg)
	binary.LittleEndian.PutUint64(b[24:], s.MemoryMax)
	binary.LittleEndian.PutUint64(b[32:], s.Disk)
	binary.LittleEndian.PutUint64(b[40:], s.NetworkRx)
	binary.LittleEndian.PutUint64(b[48:], s.NetworkTx)
	return b
}

func decode(b []byte) Sample {
	return Sample{
		Time:      int64(binary.LittleEndian.Uint64(b[0:])),
		CPUAvg:    math.Float32frombits(binary.LittleEndian.Uint32(b[8:])),
		CPUMax:    math.Float32frombits(binary.LittleEndian.Uint32(b[12:])),
		MemoryAvg: binary.LittleEndian.Uint64(b[16:]),
		MemoryMax: binary.LittleEndian.Uint64(b[24:]),
		Disk:      binary.LittleEndian.Uint64(b[32:]),
		NetworkRx: binary.LittleEndian.Uint64(b[40:]),
		NetworkTx: binary.LittleEndian.Uint64(b[48:]),
	}
}

// Point is a point on a graph, summarising the samples taken from Time for
// Span seconds.
type Point struct {
	Time      int64   `json:"time"`
	Span      int64   `json:"span"`
	CPUAvg    float64 `json:"cpu_avg"`
	CPUMax    float64 `json:"cpu_max"`
	MemoryAvg uint64  `json:"memory_avg_bytes"`
	MemoryMax uint64  `json:"memory_max_bytes"`
	Disk      uint64  `json:"disk_bytes"`
	NetworkRx uint64  `json:"network_rx_bytes"`
	NetworkTx uint64  `json:"network_tx_bytes"`
}

// Summarize turns the samples taken from "from" to "to", one every interval
// seconds, into at most points points. Each point covers an equal span of time,
// a whole number of intervals starting on one: CPU and memory are averaged,
// their highest values kept, the disk usage is the last one in the span, and
// network traffic is added up. Spans without any samples, such as while a
// server was offline, have no point.
func Summarize(samples []Sample, from, to, interval int64, points int) []Point {
	if points < 1 || to < from {
		return []Point{}
	}
	interval = max(interval, 1)
	// Spans start on the intervals the samples were taken on, so that a point
	// never splits a sample, and points of one span hold exactly one sample.
	base := from - ((from%interval)+interval)%interval
	span := (to - base + 1 + int64(points) - 1) / int64(points)
	span = max((span+interval-1)/interval*interval, interval)

	out := []Point{}
	var cur *Point
	var n int
	var cpuSum float64
	var memSum float64
	flush := func() {
		if cur != nil && n > 0 {
			cur.CPUAvg = round2(cpuSum / float64(n))
			cur.CPUMax = round2(cur.CPUMax)
			cur.MemoryAvg = uint64(memSum / float64(n))
			out = append(out, *cur)
		}
	}
	for _, s := range samples {
		if s.Time < from || s.Time > to {
			continue
		}
		start := base + (s.Time-base)/span*span
		if cur == nil || cur.Time != start {
			flush()
			cur = &Point{Time: start, Span: span}
			n, cpuSum, memSum = 0, 0, 0
		}
		n++
		cpuSum += float64(s.CPUAvg)
		memSum += float64(s.MemoryAvg)
		cur.CPUMax = max(cur.CPUMax, float64(s.CPUMax))
		cur.MemoryMax = max(cur.MemoryMax, s.MemoryMax)
		cur.Disk = s.Disk
		cur.NetworkRx += s.NetworkRx
		cur.NetworkTx += s.NetworkTx
	}
	flush()
	return out
}

// round2 rounds a CPU usage to two decimals, which also hides the imprecision of
// storing it as a float32.
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
