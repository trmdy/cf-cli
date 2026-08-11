package main

import (
	"fmt"
	"os"

	"github.com/trmdy/cf-cli/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "cf:", err)
		os.Exit(1)
	}
}
