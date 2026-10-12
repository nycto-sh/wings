// Package quota measures, and can limit, the disk space used by a directory
// using the project quotas of the filesystem it is on, so that it does not
// have to be measured by walking the directory.
//
// Project quotas are available on XFS mounted with "prjquota", and on ext4
// with the "project" and "quota" features and mounted with "prjquota". On any
// other filesystem, or without them enabled, Supported reports false and
// callers should measure the directory themselves.
//
// Every file and directory has a project ID. Giving a directory a project ID
// with the "inherit" flag makes everything created in it later get the same
// project ID, and the kernel keeps a running total of the space used by each
// project ID.
//
// @see docs/disk-usage.md.
package quota

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Values from linux/fs.h and linux/quota.h, which golang.org/x/sys/unix does
// not define. They are the same on every architecture Wings supports.
const (
	fsIocFsgetxattr    = 0x801c581f // _IOR('X', 31, struct fsxattr)
	fsIocFssetxattr    = 0x401c5820 // _IOW('X', 32, struct fsxattr)
	fsXflagProjinherit = 0x00000200

	prjQuota  = 2
	qGetinfo  = 0x800005
	qGetquota = 0x800007
	qSetquota = 0x800008

	qifBlimits = 1

	// blockSize is the unit of the block limits in struct if_dqblk.
	blockSize = 1024
)

// fsxattr is struct fsxattr from linux/fs.h.
type fsxattr struct {
	Xflags     uint32
	Extsize    uint32
	Nextents   uint32
	Projid     uint32
	Cowextsize uint32
	Pad        [8]byte
}

// dqblk is struct if_dqblk from linux/quota.h.
type dqblk struct {
	BHardlimit uint64
	BSoftlimit uint64
	CurSpace   uint64
	IHardlimit uint64
	ISoftlimit uint64
	CurInodes  uint64
	BTime      uint64
	ITime      uint64
	Valid      uint32
	_          uint32
}

// dqinfo is struct if_dqinfo from linux/quota.h.
type dqinfo struct {
	BGrace uint64
	IGrace uint64
	Flags  uint32
	Valid  uint32
}

func qcmd(cmd, typ uint32) uint32 {
	return cmd<<8 | typ&0xff
}

// quotactl calls quotactl_fd(2) for the filesystem fd is on. It needs Linux
// 5.14 or newer.
func quotactl(fd int, cmd uint32, id uint32, addr unsafe.Pointer) error {
	_, _, errno := unix.Syscall6(unix.SYS_QUOTACTL_FD, uintptr(fd), uintptr(qcmd(cmd, prjQuota)), uintptr(id), uintptr(addr), 0, 0) //nolint:gosec // audited: addr points to a struct matching the kernel's
	if errno != 0 {
		return errno
	}
	return nil
}

func getXattr(fd int) (fsxattr, error) {
	var x fsxattr
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), fsIocFsgetxattr, uintptr(unsafe.Pointer(&x))); errno != 0 { //nolint:gosec // audited: x matches struct fsxattr
		return x, errno
	}
	return x, nil
}

func setXattr(fd int, x *fsxattr) error {
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), fsIocFssetxattr, uintptr(unsafe.Pointer(x))); errno != 0 { //nolint:gosec // audited: x matches struct fsxattr
		return errno
	}
	return nil
}

// Supported reports whether project quotas are enabled on the filesystem that
// the directory fd refers to is on.
func Supported(fd int) bool {
	if _, err := getXattr(fd); err != nil {
		return false
	}
	var info dqinfo
	return quotactl(fd, qGetinfo, 0, unsafe.Pointer(&info)) == nil //nolint:gosec // audited: info matches struct if_dqinfo
}

// SupportedPath reports whether project quotas are enabled on the filesystem
// that the directory path is on.
func SupportedPath(path string) bool {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	return Supported(fd)
}

// ProjectID returns the project ID of the file or directory fd refers to.
func ProjectID(fd int) (uint32, error) {
	x, err := getXattr(fd)
	return x.Projid, err
}

// SetProjectID gives the file or directory fd refers to the project ID id. A
// directory is also marked so that everything created in it gets the same
// project ID.
func SetProjectID(fd int, id uint32, dir bool) error {
	x, err := getXattr(fd)
	if err != nil {
		return err
	}
	inherit := !dir || x.Xflags&fsXflagProjinherit != 0
	if x.Projid == id && inherit {
		return nil
	}
	x.Projid = id
	if dir {
		x.Xflags |= fsXflagProjinherit
	}
	return setXattr(fd, &x)
}

// Usage returns the number of bytes used by the project ID id, on the
// filesystem that fd is on.
func Usage(fd int, id uint32) (int64, error) {
	var d dqblk
	if err := quotactl(fd, qGetquota, id, unsafe.Pointer(&d)); err != nil { //nolint:gosec // audited: d matches struct if_dqblk
		// XFS has no record of a project ID that has never been used.
		if errors.Is(err, unix.ENOENT) {
			return 0, nil
		}
		return 0, err
	}
	return int64(d.CurSpace), nil
}

// SetLimit limits the space the project ID id may use, on the filesystem that
// fd is on, to limit bytes rounded up to a whole kilobyte. A limit of 0 or less
// removes the limit.
func SetLimit(fd int, id uint32, limit int64) error {
	d := dqblk{Valid: qifBlimits}
	if limit > 0 {
		d.BHardlimit = uint64((limit + blockSize - 1) / blockSize)
	}
	return quotactl(fd, qSetquota, id, unsafe.Pointer(&d)) //nolint:gosec // audited: d matches struct if_dqblk
}
