package config

import "testing"

func TestCompileConcurrency(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil || c.CompileConcurrency != 2 {
		t.Fatalf("default: %v %v", c, err)
	}
	if c, err = Load(env(map[string]string{"SCRIOBH_COMPILE_CONCURRENCY": "5"})); err != nil || c.CompileConcurrency != 5 {
		t.Errorf("5: %v", err)
	}
	for _, bad := range []string{"0", "33", "-1", "many"} {
		if _, err := Load(env(map[string]string{"SCRIOBH_COMPILE_CONCURRENCY": bad})); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
