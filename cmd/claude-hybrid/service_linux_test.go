package main

import "testing"

func TestSystemdQuote(t *testing.T) {
	if got := systemdQuote(`/home/a b/100%/x"y`); got != `"/home/a b/100%%/x\"y"` {
		t.Fatal(got)
	}
}
