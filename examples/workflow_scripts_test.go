package examples

import (
	"os"
	"os/exec"
	"testing"
)

// Keep the merge policy's failure-path tests in the normal CI command.
func TestRiskDeliveryPolicy(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	command := exec.Command(python, "-m", "unittest", "discover", "-s", "workflows/risk_delivery", "-p", "test_gate.py", "-v")
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("risk delivery tests: %v: %s", err, output)
	}
}
