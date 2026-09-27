package powerguard

import "sync"

// Guard holds an operating-system power assertion for the lifetime of an
// active NIBIA workload. Release is idempotent.
type Guard struct {
	backend string
	release func() error
	once    sync.Once
	err     error
}

// Acquire prevents idle/system sleep while an active NIBIA workload is using
// this node. Platform implementations use native OS facilities or standard OS
// utilities; callers may choose whether an unavailable guard is fatal.
func Acquire(reason string) (*Guard, error) {
	release, backend, err := acquirePlatform(reason)
	if err != nil {
		return nil, err
	}
	return &Guard{backend: backend, release: release}, nil
}

func (g *Guard) Backend() string {
	if g == nil {
		return ""
	}
	return g.backend
}

func (g *Guard) Release() error {
	if g == nil || g.release == nil {
		return nil
	}
	g.once.Do(func() { g.err = g.release() })
	return g.err
}
