package filesystem

import (
	"errors"
	"time"

	"github.com/apex/log"
	"golang.org/x/sys/unix"

	"github.com/nycto-sh/wings/internal/quota"
	"github.com/nycto-sh/wings/internal/ufs"
)

// projectQuota is set on a filesystem once every file of the server has its
// project ID, after which its disk usage is read from the project quota.
type projectQuota struct {
	id      uint32
	enforce bool
}

// openRoot opens the server's root directory, without following a symbolic
// link.
func (fs *Filesystem) openRoot() (int, error) {
	return unix.Open(fs.Path(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

// ProjectQuotaSupported reports whether the server's directory is on a
// filesystem with project quotas enabled.
func (fs *Filesystem) ProjectQuotaSupported() bool {
	fd, err := fs.openRoot()
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	return quota.Supported(fd)
}

// ProjectID returns the project ID of the server's directory, which is 0 if it
// has never been given one.
func (fs *Filesystem) ProjectID() (uint32, error) {
	fd, err := fs.openRoot()
	if err != nil {
		return 0, err
	}
	defer unix.Close(fd)
	return quota.ProjectID(fd)
}

// UsesProjectQuota reports whether the disk usage is read from a project quota.
func (fs *Filesystem) UsesProjectQuota() bool {
	return fs.project.Load() != nil
}

// UseProjectQuota gives every file and directory of the server the project ID
// id, after which the disk usage is read from the project quota instead of by
// counting the files. Directories are marked so that everything created in
// them later gets the same project ID. With enforce, the kernel also refuses
// writes that would take the server past its disk limit.
//
// Giving the files their project ID visits every one of them once, so it can
// take as long as counting them. Until it has finished, the files are counted
// as before. If it fails, they keep being counted.
func (fs *Filesystem) UseProjectQuota(id uint32, enforce bool) error {
	if !fs.ProjectQuotaSupported() {
		return errors.New("project quotas are not enabled on the filesystem")
	}

	dirfd, name, closeFd, err := fs.unixFS.SafePath("/")
	defer closeFd()
	if err != nil {
		return err
	}
	err = fs.unixFS.WalkDirat(dirfd, name, func(dirfd int, name, _ string, d ufs.DirEntry, err error) error {
		// Files can be removed while the walk is running, which is not an error.
		if errors.Is(err, ufs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		// Only regular files and directories hold data. Symbolic links and
		// special files are never opened.
		if !d.IsDir() && !d.Type().IsRegular() {
			return nil
		}
		return setProjectID(dirfd, name, id, d.IsDir())
	})
	if err != nil {
		return err
	}

	q := &projectQuota{id: id, enforce: enforce}
	if err := fs.applyProjectLimit(q); err != nil {
		return err
	}
	fs.project.Store(q)
	// Read the usage from the project quota from now on.
	fs.lastLookupTime.Set(time.Time{})
	return nil
}

// setProjectID gives the file or directory name in dirfd the project ID id.
// The directory is marked before its contents are read, so that anything
// created in it while the walk is running gets the project ID as well.
func setProjectID(dirfd int, name string, id uint32, dir bool) error {
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_NOCTTY | unix.O_CLOEXEC
	if dir {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(dirfd, name, flags, 0)
	if err != nil {
		// The entry was removed, or replaced with a symbolic link, since it was
		// read from its directory.
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) {
			return nil
		}
		return err
	}
	defer unix.Close(fd)

	// Check what was opened, in case the entry was replaced since it was read.
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return quota.SetProjectID(fd, id, true)
	case unix.S_IFREG:
		return quota.SetProjectID(fd, id, false)
	}
	return nil
}

// applyProjectLimit sets the limit of the project quota to the server's disk
// limit when it is enforced, and removes any limit otherwise.
func (fs *Filesystem) applyProjectLimit(q *projectQuota) error {
	fd, err := fs.openRoot()
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var limit int64
	if q.enforce {
		limit = fs.MaxDisk()
	}
	return quota.SetLimit(fd, q.id, limit)
}

// projectUsage reads the disk usage from the project quota.
func (fs *Filesystem) projectUsage(q *projectQuota) (int64, error) {
	fd, err := fs.openRoot()
	if err != nil {
		return 0, err
	}
	defer unix.Close(fd)
	return quota.Usage(fd, q.id)
}

// updateProjectLimit applies a new disk limit to an enforced project quota.
func (fs *Filesystem) updateProjectLimit() {
	q := fs.project.Load()
	if q == nil || !q.enforce {
		return
	}
	if err := fs.applyProjectLimit(q); err != nil {
		log.WithField("root", fs.Path()).WithField("error", err).Warn("failed to update the project quota limit")
	}
}
