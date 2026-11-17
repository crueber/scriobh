package passkit

import (
	"strings"
	"testing"
	"time"
)

func TestHashAndVerify(t *testing.T) {
	h, err := Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("the hash is %.40q", h)
	}
	if strings.Contains(h, "correct horse") {
		t.Error("the hash holds the passphrase")
	}
	if !Verify(h, "correct horse battery") || Verify(h, "correct horse batter") || Verify(h, "") {
		t.Error("verify")
	}
	h2, _ := Hash("correct horse battery")
	if h == h2 {
		t.Error("two hashes of one passphrase are equal: the salt is not random")
	}
}

// A row someone else wrote must not make a check expensive or crash it.
func TestVerifyRefusesHostileHashes(t *testing.T) {
	for _, bad := range []string{
		"", "plain", "$argon2id$v=19$m=19456,t=2,p=1$AAAA", "$argon2i$v=19$m=19456,t=2,p=1$AAAA$AAAA",
		"$argon2id$v=19$m=999999999,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=19456,t=999,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=19456,t=2,p=99$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=x,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAA", "$argon2id$v=19$m=19456,t=2,p=1$!!!$!!!",
		"$argon2id$v=19$m=0,t=0,p=0$AAAA$AAAA", "$argon2id$v=19$m=19456,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$",
	} {
		done := make(chan bool, 1)
		go func() { done <- Verify(bad, "x") }()
		select {
		case ok := <-done:
			if ok {
				t.Errorf("%q verified", bad)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("%q took too long to refuse", bad)
		}
	}
}

func TestDelayIsTheSharedSchedule(t *testing.T) {
	want := map[int]time.Duration{0: 0, 1: 0, 3: 0, 4: 30 * time.Second, 5: time.Minute, 6: 2 * time.Minute, 7: 4 * time.Minute, 12: time.Hour, 50: time.Hour}
	for n, d := range want {
		if got := Delay(n); got != d {
			t.Errorf("Delay(%d) = %v, want %v", n, got, d)
		}
	}
}

func TestPoolSaysBusyInsteadOfQueueing(t *testing.T) {
	p := NewPool(2)
	r1, ok1 := p.TryAcquire()
	r2, ok2 := p.TryAcquire()
	if !ok1 || !ok2 {
		t.Fatal("two slots")
	}
	if _, ok := p.TryAcquire(); ok {
		t.Error("a third slot")
	}
	r1()
	if r3, ok := p.TryAcquire(); !ok {
		t.Error("a freed slot")
	} else {
		r3()
	}
	r2()
}
