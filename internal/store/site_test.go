package store

import "testing"

func TestValidDesign(t *testing.T) {
	for d, want := range map[string]bool{"classic": true, "surface": true, "": false, "Surface": false, "dark": false} {
		if got := ValidDesign(d); got != want {
			t.Errorf("ValidDesign(%q) = %v, want %v", d, got, want)
		}
	}
}
