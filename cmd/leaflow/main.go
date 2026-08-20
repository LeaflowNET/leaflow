package main

import (
	"os"

	"github.com/LeaflowNET/leaflow/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:]))
}
