package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"organon/internal/app"
)

func codegenCmd() *cobra.Command {
	cg := &cobra.Command{Use: "codegen", Short: "Generate client code from the manifest"}
	var out, env, clientImport string
	ts := &cobra.Command{
		Use:   "ts [dir]",
		Short: "Generate TypeScript types and a typed client (spec 13.1)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := app.Load(dirArg(args), env)
			if err != nil {
				return printProblems(err)
			}
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
			path := filepath.Join(out, "index.ts")
			if err := os.WriteFile(path, []byte(app.GenerateTS(c, clientImport)), 0o644); err != nil {
				return err
			}
			fmt.Println("wrote", path)
			return nil
		},
	}
	ts.Flags().StringVar(&out, "out", "./src/gen", "output directory")
	ts.Flags().StringVar(&env, "env", "dev", "environment")
	ts.Flags().StringVar(&clientImport, "client-import", "@organon/client", "import path of the client runtime")
	cg.AddCommand(ts)
	return cg
}
