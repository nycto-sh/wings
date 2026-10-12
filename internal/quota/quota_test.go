package quota

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The structs passed to the kernel must have exactly the layout of the C
// structs.
func TestStructLayout(t *testing.T) {
	if s := unsafe.Sizeof(fsxattr{}); s != 28 {
		t.Errorf("struct fsxattr is 28 bytes, got %d", s)
	}
	if s := unsafe.Sizeof(dqblk{}); s != 72 {
		t.Errorf("struct if_dqblk is 72 bytes, got %d", s)
	}
	if s := unsafe.Sizeof(dqinfo{}); s != 24 {
		t.Errorf("struct if_dqinfo is 24 bytes, got %d", s)
	}
}

func TestRequestNumbers(t *testing.T) {
	// _IOR and _IOW from asm-generic/ioctl.h.
	ioc := func(dir, nr, size uintptr) uintptr { return dir<<30 | size<<16 | 'X'<<8 | nr }
	if got := ioc(2, 31, unsafe.Sizeof(fsxattr{})); got != fsIocFsgetxattr {
		t.Errorf("FS_IOC_FSGETXATTR is %#x, got %#x", got, fsIocFsgetxattr)
	}
	if got := ioc(1, 32, unsafe.Sizeof(fsxattr{})); got != fsIocFssetxattr {
		t.Errorf("FS_IOC_FSSETXATTR is %#x, got %#x", got, fsIocFssetxattr)
	}
	for cmd, want := range map[uint32]uint32{qGetinfo: 0x80000502, qGetquota: 0x80000702, qSetquota: 0x80000802} {
		if got := qcmd(cmd, prjQuota); got != want {
			t.Errorf("QCMD(%#x, PRJQUOTA) is %#x, got %#x", cmd, want, got)
		}
	}
}

func TestPlan(t *testing.T) {
	f := FirstID
	got := Plan([]uint32{0, f + 5, f + 5, 7, f})
	want := []uint32{f + 1, f + 5, f + 2, f + 3, f}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

func TestAllocator(t *testing.T) {
	a := NewAllocator()
	if !a.Reserve(FirstID) || a.Reserve(FirstID) {
		t.Fatal("expected an ID to be reserved only once")
	}
	if id := a.Next(); id != FirstID+1 {
		t.Fatalf("expected the next free ID, got %d", id)
	}
	if id := a.Next(); id != FirstID+2 {
		t.Fatalf("expected the next free ID, got %d", id)
	}
}

// TestProjectQuota needs a directory on a filesystem with project quotas
// enabled, set in NYCTO_QUOTA_TEST_DIR, and has to run as root. See
// docs/disk-usage.md for how to make one.
func TestProjectQuota(t *testing.T) {
	root := os.Getenv("NYCTO_QUOTA_TEST_DIR")
	if root == "" {
		t.Skip("NYCTO_QUOTA_TEST_DIR is not set")
	}
	dir, err := os.MkdirTemp(root, "quota-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	if !Supported(fd) {
		t.Fatal("expected project quotas to be enabled on " + root)
	}

	const id = FirstID - 1
	if err := SetProjectID(fd, id, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetLimit(fd, id, 0) })
	if got, err := ProjectID(fd); err != nil || got != id {
		t.Fatalf("expected project ID %d, got %d (%v)", id, got, err)
	}

	// A file created in the directory gets its project ID, and its size is
	// counted straight away.
	if err := os.WriteFile(filepath.Join(dir, "a"), make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Fsync(fd); err != nil {
		t.Fatal(err)
	}
	used, err := Usage(fd, id)
	if err != nil {
		t.Fatal(err)
	}
	if used < 1<<20 {
		t.Fatalf("expected at least 1 MiB to be used, got %d", used)
	}

	// With a limit, the kernel refuses to write past it.
	if err := SetLimit(fd, id, 2<<20); err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(dir, "b"), make([]byte, 4<<20), 0o600)
	if !errors.Is(err, unix.EDQUOT) {
		t.Fatalf("expected writing past the limit to fail with EDQUOT, got %v", err)
	}
}
