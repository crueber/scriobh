package presence

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newClocked() (*Service, *clock) {
	s := New()
	c := &clock{t: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)}
	s.SetClock(c.now)
	return s, c
}

func users(s *Service, project string) map[string]bool {
	out := map[string]bool{}
	for _, e := range s.live(project) {
		out[e.user] = true
	}
	return out
}

// An entry is shown for 60 seconds after the last beat, not 61, and a refresh moves the clock.
func TestEntriesExpireSixtySecondsAfterTheLastBeat(t *testing.T) {
	s, c := newClocked()
	s.touch("p", "u1", "s1", "")
	c.add(59 * time.Second)
	if !users(s, "p")["u1"] {
		t.Fatal("gone at 59 s")
	}
	c.add(2 * time.Second) // 61 s
	if users(s, "p")["u1"] {
		t.Fatal("still shown at 61 s")
	}
	// a beat every 20 s keeps a person for as long as they beat
	s.touch("p", "u2", "s2", "")
	for i := 0; i < 10; i++ {
		c.add(20 * time.Second)
		s.touch("p", "u2", "s2", "")
	}
	if !users(s, "p")["u2"] {
		t.Fatal("a person who beats every 20 s expired")
	}
	// the sweep frees the memory of the expired
	c.add(2 * time.Minute)
	s.Sweep()
	if s.Len() != 0 {
		t.Fatalf("%d entries held after everyone expired", s.Len())
	}
}

func TestLeaveForgetAndDropProject(t *testing.T) {
	s, _ := newClocked()
	s.touch("p", "u1", "s1", "")
	s.touch("p", "u2", "s2", "")
	s.touch("q", "u1", "s3", "")
	s.Leave("p", "u1")
	if users(s, "p")["u1"] || !users(s, "q")["u1"] {
		t.Fatal("leave removed the wrong entries")
	}
	s.touch("p", "u1", "s1", "")
	s.Forget("u1")
	if len(users(s, "p")) != 1 || len(users(s, "q")) != 0 {
		t.Fatalf("forget: p=%v q=%v", users(s, "p"), users(s, "q"))
	}
	s.ForgetSession("s2")
	if s.Len() != 0 {
		t.Fatalf("forget session left %d", s.Len())
	}
	s.touch("p", "u1", "s1", "")
	s.touch("p", "u2", "s2", "")
	s.DropProject("p")
	if s.Len() != 0 {
		t.Fatal("drop project left entries")
	}
}

func TestPerProjectCapEvictsTheOldest(t *testing.T) {
	s, c := newClocked()
	for i := 0; i < MaxPerProject; i++ {
		s.touch("p", fmt.Sprintf("u%03d", i), "s", "")
		c.add(time.Millisecond)
	}
	s.touch("p", "newcomer", "s", "")
	got := users(s, "p")
	if len(got) != MaxPerProject {
		t.Fatalf("%d entries, cap is %d", len(got), MaxPerProject)
	}
	if got["u000"] || !got["newcomer"] || !got["u001"] {
		t.Fatalf("the oldest (u000) should be the one evicted: %v", got["u000"])
	}
	// another project is untouched by this one's cap
	s.touch("q", "x", "s", "")
	if len(users(s, "q")) != 1 {
		t.Fatal("a second project lost an entry")
	}
}

func TestTotalCapEvictsTheOldestOverall(t *testing.T) {
	s, c := newClocked()
	n := MaxTotal + 50
	for i := 0; i < n; i++ {
		s.touch(fmt.Sprintf("p%d", i/10), fmt.Sprintf("u%d", i), "s", "")
		c.add(time.Millisecond)
	}
	if s.Len() != MaxTotal {
		t.Fatalf("%d entries, cap is %d", s.Len(), MaxTotal)
	}
	if users(s, "p0")["u0"] {
		t.Fatal("the oldest entry survived the total cap")
	}
	if !users(s, fmt.Sprintf("p%d", (n-1)/10))[fmt.Sprintf("u%d", n-1)] {
		t.Fatal("the newest entry was evicted")
	}
}

// A refresh changes nothing and wakes nobody; a new person, another document or another session does.
func TestOnlyRealChangesWakeWatchers(t *testing.T) {
	s, c := newClocked()
	wake, stop := s.Watch("p")
	defer stop()
	woke := func() bool {
		select {
		case <-wake:
			return true
		default:
			return false
		}
	}
	s.touch("p", "u1", "s1", "d1")
	if !woke() {
		t.Fatal("a new person did not wake the watcher")
	}
	v := s.Version("p")
	c.add(20 * time.Second)
	s.touch("p", "u1", "s1", "d1")
	if woke() || s.Version("p") != v {
		t.Fatal("a plain refresh woke the watcher or changed the version")
	}
	c.add(time.Second) // closer than MinGap to the last beat, another document: still a change
	s.touch("p", "u1", "s1", "d2")
	if !woke() {
		t.Fatal("moving to another document did not wake the watcher")
	}
	// a flood from one client inside MinGap is not a change either
	for i := 0; i < 100; i++ {
		s.touch("p", "u1", "s1", "d2")
	}
	if woke() {
		t.Fatal("a flood of identical beats woke the watcher")
	}
	s.Leave("p", "u1")
	if !woke() || s.Version("p") != 0 {
		t.Fatalf("leaving: woke or version wrong (version %d, an empty project is 0)", s.Version("p"))
	}
	// a watcher that is never read does not block the sender: its buffer is one
	for i := 0; i < 1000; i++ {
		s.touch("p", fmt.Sprintf("v%d", i%50), "s", "")
	}
}

// A stalled stream holds nothing the service needs: the service never writes to a client, and a full watcher channel is
// skipped, so 10,000 changes with nobody reading finish at once.
func TestAWatcherNobodyReadsNeverBlocksTheTable(t *testing.T) {
	s, _ := newClocked()
	_, stop := s.Watch("p")
	defer stop()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10000; i++ {
			s.touch("p", fmt.Sprintf("u%d", i%40), fmt.Sprintf("s%d", i), "")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("changes blocked on a watcher nobody reads")
	}
}

func TestConcurrentUse(t *testing.T) {
	s := New() // the real clock
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			wake, stop := s.Watch(fmt.Sprintf("p%d", g%3))
			defer stop()
			for i := 0; i < 2000; i++ {
				p := fmt.Sprintf("p%d", (g+i)%3)
				u := fmt.Sprintf("u%d", i%30)
				switch i % 7 {
				case 0:
					s.Leave(p, u)
				case 1:
					s.Forget(u)
				case 2:
					s.ForgetSession("s")
				case 3:
					_ = s.live(p)
					_ = s.Version(p)
				case 4:
					s.Sweep()
				case 5:
					s.DropProject(p)
				default:
					s.touch(p, u, "s", fmt.Sprintf("d%d", i%5))
				}
				select {
				case <-wake:
				default:
				}
			}
		}()
	}
	wg.Wait()
}
