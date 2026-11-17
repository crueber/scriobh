package mirror

// WorkerBusy reports whether the project is queued for the worker or being written.
// For tests that drive a fake clock: after advancing it they wait for the work the
// clock started, instead of guessing how long a write takes in real time.
func (m *Mirror) WorkerBusy(projectID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.ps[projectID]
	return ok && (p.queued || p.flushing)
}
