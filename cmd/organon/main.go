// Command organon is the framework CLI (spec 12.3): it scaffolds, validates,
// tests, and runs app manifests locally, and talks to a running server
// through the public API for everything else.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:           "organon",
		Short:         "Schema-driven application framework CLI",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(initCmd(), validateCmd(), uiCmd(), testCmd(), devCmd(), loginCmd(), tokenCmd(), seedCmd(), runsCmd(),
		exportCmd(), importCmd(), planCmd(), applyCmd(), codegenCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "organon:", err)
		os.Exit(1)
	}
}

func dirArg(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return "."
}
