package main

import (
	"os"

	"github.com/qabasone/qabasone-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:]))
}
