package main

import (
	"context"
	"fmt"
	"os"

	"github.com/corca-ai/craken-cli/internal/craken"
)

var version = "dev"

func main() {
	if err := craken.Run(context.Background(), version, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
