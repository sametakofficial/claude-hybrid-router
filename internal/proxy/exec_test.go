package proxy

import "os/exec"

// execCombined is a thin wrapper around exec.Command used only from
// end-to-end tests. Kept in its own file so unit-test files don't pick
// up a dependency on os/exec when go test builds them in isolation.
func execCombined(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}
