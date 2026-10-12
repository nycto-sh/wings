package server

import (
	"github.com/apex/log"

	"github.com/nycto-sh/wings/config"
	"github.com/nycto-sh/wings/internal/quota"
)

// projectQuotas hands out project IDs to servers while their disk usage is
// measured with project quotas.
//
// @see docs/disk-usage.md.
type projectQuotas struct {
	alloc   *quota.Allocator
	enforce bool
}

// EnableProjectQuotas measures the disk usage of every server with a project
// quota when the filesystem of the data directory has them enabled, instead of
// by counting the files. It does nothing otherwise, or when turned off in the
// configuration. Call it once, after the servers have been loaded.
func (m *Manager) EnableProjectQuotas() {
	cfg := config.Get().System
	if cfg.DiskQuota.Mode == "off" || cfg.DiskCheckInterval == 0 {
		log.WithField("subsystem", "disk_quota").Debug("project quotas are turned off in the configuration")
		return
	}
	if !quota.SupportedPath(cfg.Data) {
		log.WithField("subsystem", "disk_quota").WithField("path", cfg.Data).
			Info("project quotas are not enabled on the filesystem of the data directory, counting files to measure disk usage")
		return
	}

	servers := m.All()
	existing := make([]uint32, len(servers))
	for i, s := range servers {
		existing[i], _ = s.Filesystem().ProjectID()
	}
	ids := quota.Plan(existing)

	pq := &projectQuotas{alloc: quota.NewAllocator(), enforce: cfg.DiskQuota.Enforce}
	for _, id := range ids {
		pq.alloc.Reserve(id)
	}
	m.mu.Lock()
	m.quotas = pq
	m.mu.Unlock()

	log.WithField("subsystem", "disk_quota").WithField("servers", len(servers)).WithField("enforce", pq.enforce).
		Info("using project quotas to measure disk usage")

	// Giving the files of a server its project ID visits every one of them once,
	// so the servers are set up one at a time, in the background.
	go func() {
		for i, s := range servers {
			s.useProjectQuota(ids[i], pq.enforce)
		}
	}()
}

// setUpProjectQuota gives a server added after start-up its own project quota,
// if project quotas are in use.
func (m *Manager) setUpProjectQuota(s *Server) {
	m.mu.RLock()
	pq := m.quotas
	m.mu.RUnlock()
	if pq == nil {
		return
	}
	go s.useProjectQuota(pq.alloc.Next(), pq.enforce)
}

func (s *Server) useProjectQuota(id uint32, enforce bool) {
	if err := s.Filesystem().UseProjectQuota(id, enforce); err != nil {
		s.Log().WithField("error", err).Warn("could not measure disk usage with a project quota, counting files instead")
		return
	}
	s.Log().WithField("project_id", id).Debug("measuring disk usage with a project quota")
}
