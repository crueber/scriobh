//go:build race

package presence_test

// The race detector makes the code several times slower, so the time budgets scale by timingFactor and the
// load test uses fewer projects (the heap and the per-beat figures are the same shape at 200 projects).
const (
	timingFactor = 10
	loadProjects = 200
)
