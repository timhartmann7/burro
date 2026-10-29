// Command burro runs a project's tests and builds on the user's own server.
package main

import (
	"os"

	"github.com/timhartmann7/burro/internal/cli"
	"github.com/timhartmann7/burro/internal/output"
)

// version is set with -ldflags -X in release builds.
var version string

func main() {
	app := cli.New(os.Stdout, version)
	os.Exit(output.Report(os.Stderr, app.Run(os.Args[1:])))
}
