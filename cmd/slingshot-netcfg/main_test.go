package main

import (
	"os"
	"runtime"
	"testing"
)

func TestCapGOMAXPROCS(t *testing.T) {
	cases := []struct {
		name      string
		env       string
		start     int
		wantAfter int
	}{
		{"lowers default", "", 8, 2},
		{"keeps smaller default", "", 1, 1},
		{"operator setting wins", "6", 6, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := runtime.GOMAXPROCS(tc.start)
			t.Cleanup(func() { runtime.GOMAXPROCS(orig) })
			if tc.env != "" {
				t.Setenv("GOMAXPROCS", tc.env)
			} else {
				t.Setenv("GOMAXPROCS", "")
				os.Unsetenv("GOMAXPROCS")
			}

			capGOMAXPROCS(2)

			if got := runtime.GOMAXPROCS(0); got != tc.wantAfter {
				t.Errorf("GOMAXPROCS = %d, want %d", got, tc.wantAfter)
			}
		})
	}
}
