package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"kandev-plugin-coordinator/server/coordinator"
)

func main() {
	report, err := coordinator.RunFixturePOC(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
