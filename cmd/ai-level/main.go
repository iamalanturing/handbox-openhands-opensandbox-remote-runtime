// Command ai-level reads and writes handbox's network-level state file.
package main

import (
	"fmt"
	"os"

	"github.com/iamalanturing/handbox-openhands-opensandbox-remote-runtime/internal/config"
	"github.com/iamalanturing/handbox-openhands-opensandbox-remote-runtime/internal/levels"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: ai-level <offline|github|research|full>")
		os.Exit(1)
	}

	level, err := levels.ParseLevel(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	statePath := os.Getenv(config.EnvStateFilePath)
	if statePath == "" {
		statePath = config.DefaultStateFile
	}

	// The singleUse argument only affects State.Consume(), which ai-level
	// never calls - Set() ignores it. Its value here is irrelevant.
	state := levels.New(statePath, true)
	if err := state.Set(level); err != nil {
		fmt.Fprintln(os.Stderr, "failed to set level:", err)
		os.Exit(1)
	}
	fmt.Printf("Network level set to %s.\n", level)
}
