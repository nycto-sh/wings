package quota

import "sync"

// FirstID is the first project ID Wings gives a server. It stays clear of the
// small IDs that administrators usually give their own projects in
// /etc/projid.
const FirstID uint32 = 0x6E790000

// Plan decides the project ID of each server directory, given the project ID
// each one has now, which is 0 if it has none. A directory keeps an ID that
// Wings gave it before, unless another directory has the same one, and every
// other directory is given a new one.
func Plan(existing []uint32) []uint32 {
	a := NewAllocator()
	out := make([]uint32, len(existing))
	for i, id := range existing {
		if id >= FirstID && a.Reserve(id) {
			out[i] = id
		}
	}
	for i := range out {
		if out[i] == 0 {
			out[i] = a.Next()
		}
	}
	return out
}

// Allocator hands out project IDs that are not in use.
type Allocator struct {
	mu   sync.Mutex
	used map[uint32]bool
	next uint32
}

func NewAllocator() *Allocator {
	return &Allocator{used: make(map[uint32]bool), next: FirstID}
}

// Reserve marks id as in use, and returns false if it already was.
func (a *Allocator) Reserve(id uint32) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.used[id] {
		return false
	}
	a.used[id] = true
	return true
}

// Next returns a project ID that is not in use, and marks it as in use.
func (a *Allocator) Next() uint32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	for a.used[a.next] || a.next < FirstID {
		a.next++
		if a.next == 0 {
			a.next = FirstID
		}
	}
	id := a.next
	a.used[id] = true
	a.next++
	return id
}
