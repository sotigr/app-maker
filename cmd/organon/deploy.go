package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"organon/internal/app"
	manifestfs "organon/internal/manifest/infrastructure/fs"
)

func readBundle(dir string) (map[string]string, error) {
	files, err := manifestfs.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(files))
	for k, v := range files {
		out[k] = string(v)
	}
	return out, nil
}

func printPlan(p app.Plan) {
	fmt.Printf("plan %s (%s -> %s)\n", p.Hash, short(p.From), short(p.To))
	for _, t := range p.Types {
		fmt.Printf("  type %-40s %s\n", t.Type, t.Status)
		for _, c := range t.Changes {
			mark := " "
			if c.Breaking() {
				mark = "!"
			}
			fmt.Printf("   %s %-20s %-28s storage=%s api=%s %s\n", mark, c.Path, c.Kind, c.Storage, c.API, c.Detail)
		}
	}
	for _, x := range p.Policies {
		fmt.Printf("  policy %-38s %s\n", x.Name, x.Status)
	}
	for _, x := range p.Flows {
		priv := ""
		if x.Privileged {
			priv = " (privileged: review)"
		}
		fmt.Printf("  flow %-40s %s%s\n", x.Name, x.Status, priv)
	}
	for _, e := range p.Exposures {
		fmt.Printf("  ! new public field (data exposure, review): %s\n", e)
	}
	for _, i := range p.Indexes {
		fmt.Printf("  index build: %s\n", i)
	}
	if p.Breaking {
		fmt.Println("  BREAKING: apply needs a migration or --allow-breaking")
	}
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func planCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "plan [dir]",
		Short: "Diff the manifest against the running server (spec 12.4)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			files, err := readBundle(dirArg(args))
			if err != nil {
				return err
			}
			var p app.Plan
			if err := newClient().do("POST", "/v1/deploy/plan", map[string]any{"files": files}, &p); err != nil {
				return err
			}
			if asJSON {
				b, _ := json.MarshalIndent(p, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			printPlan(p)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the plan as JSON")
	return c
}

func applyCmd() *cobra.Command {
	var planHash string
	var autoApprove, allowBreaking bool
	c := &cobra.Command{
		Use:   "apply [dir]",
		Short: "Apply the manifest (requires the plan hash, or --auto-approve)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			files, err := readBundle(dirArg(args))
			if err != nil {
				return err
			}
			cl := newClient()
			if planHash == "" {
				if !autoApprove {
					return fmt.Errorf("pass --plan <hash> from organon plan, or --auto-approve")
				}
				var p app.Plan
				if err := cl.do("POST", "/v1/deploy/plan", map[string]any{"files": files}, &p); err != nil {
					return err
				}
				printPlan(p)
				planHash = p.Hash
			}
			var res struct {
				Activated string `json:"activated"`
			}
			if err := cl.do("POST", "/v1/deploy/apply", map[string]any{"files": files, "planHash": planHash, "allowBreaking": allowBreaking}, &res); err != nil {
				return err
			}
			fmt.Printf("activated manifest %s\n", short(res.Activated))
			return nil
		},
	}
	c.Flags().StringVar(&planHash, "plan", "", "plan hash from organon plan")
	c.Flags().BoolVar(&autoApprove, "auto-approve", false, "plan and apply in one step")
	c.Flags().BoolVar(&allowBreaking, "allow-breaking", false, "apply breaking changes without a migration")
	return c
}
