package main

import (
	"os"

	"imgm/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
