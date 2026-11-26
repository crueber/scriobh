package imports

import "time"

// RawCached says something is held in memory for a staged file's hash (what the PDF child or Pandoc read, or a Word file's blocks).
func RawCached(sum string) bool {
	rawMu.Lock()
	defer rawMu.Unlock()
	for k := range raw {
		if len(k) > len(sum) && k[len(k)-len(sum):] == sum || containsSum(k, sum) {
			return true
		}
	}
	return false
}

func containsSum(k, sum string) bool {
	for i := 0; i+len(sum) <= len(k); i++ {
		if k[i:i+len(sum)] == sum {
			return true
		}
	}
	return false
}

// RawPut and RawSweepAt let a test fill the cache and run its timer's sweep at a time of its choosing.
func RawPut(key string, v any) { rawPut(key, v) }
func RawSweepAt(now time.Time) { rawSweep(now) }
func RawTTL() time.Duration    { return rawTTL }
