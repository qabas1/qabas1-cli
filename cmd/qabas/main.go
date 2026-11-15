package main

import (
	"os"

	"github.com/qabas1/qabas1-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:]))
}
