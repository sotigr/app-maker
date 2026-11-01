package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"organon/internal/app"
)

func uiCmd() *cobra.Command {
	c := &cobra.Command{Use: "ui", Short: "End-user UI: component catalog and file schemas"}
	var asJSON bool
	catalog := &cobra.Command{
		Use:   "catalog",
		Short: "List UI components, their props, and steps",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cat := app.UICatalog()
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(cat)
			}
			fmt.Println("Props: text = literal text with {{ expr }}; expr = expression ('quoted' literals); path = \"/field\".")
			fmt.Printf("Every component also takes: %s\n", propList(cat.Common))
			for _, fam := range cat.Families {
				fmt.Printf("\n%s\n", strings.ToUpper(fam))
				for _, comp := range cat.Components {
					if comp.Family != fam {
						continue
					}
					ev := ""
					if len(comp.Events) > 0 {
						ev = "  on: " + strings.Join(comp.Events, ", ")
					}
					fmt.Printf("  %-12s %s%s\n", comp.Name, comp.Doc, ev)
					if len(comp.Props) > 0 {
						fmt.Printf("  %-12s %s\n", "", propList(comp.Props))
					}
				}
			}
			fmt.Println("\nSTEPS (\"do\")")
			for _, st := range cat.Steps {
				fmt.Printf("  %-12s %s\n", st.Name, st.Doc)
				if len(st.Props) > 0 {
					fmt.Printf("  %-12s %s\n", "", propList(st.Props))
				}
			}
			return nil
		},
	}
	catalog.Flags().BoolVar(&asJSON, "json", false, "print the catalog as JSON")
	var out string
	schema := &cobra.Command{
		Use:   "schema",
		Short: "Write JSON Schemas of ui/app.json and ui/pages/*.json for editor completion",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
			schemas := app.UISchemas()
			names := make([]string, 0, len(schemas))
			for n := range schemas {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				b, _ := json.MarshalIndent(schemas[n], "", "  ")
				p := filepath.Join(out, n)
				if err := os.WriteFile(p, append(b, '\n'), 0o644); err != nil {
					return err
				}
				fmt.Println("wrote", p)
			}
			return nil
		},
	}
	schema.Flags().StringVar(&out, "out", ".organon/schema", "output directory")
	c.AddCommand(catalog, schema)
	return c
}

func propList(props []app.UIProp) string {
	parts := make([]string, len(props))
	for i, p := range props {
		s := p.Name + ":" + string(p.Kind)
		if p.Kind == "enum" {
			s = p.Name + ":" + strings.Join(p.Enum, "|")
		}
		if p.Kind == "objects" {
			s = p.Name + ":[{" + propList(p.Item) + "}]"
		}
		if p.Required {
			s += "*"
		}
		parts[i] = s
	}
	return strings.Join(parts, "  ")
}
