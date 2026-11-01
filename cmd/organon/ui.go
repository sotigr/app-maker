package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	c.AddCommand(catalog, schema, i18nCmd())
	return c
}

func i18nCmd() *cobra.Command {
	var lang, env string
	var prune bool
	c := &cobra.Command{
		Use:   "i18n [dir] --lang <language>",
		Short: "Add every untranslated text and label to ui/i18n/<language>.json (empty, for a translator)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			dir := dirArg(args)
			c, err := app.Load(dir, env)
			if err != nil {
				return printProblems(err)
			}
			if c.UI == nil || !slices.Contains(c.UI.Languages, lang) {
				return fmt.Errorf("add %q to \"languages\" in ui/app.json first", lang)
			}
			p := filepath.Join(dir, "ui", "i18n", lang+".json")
			var head struct {
				Schema string `json:"$schema"`
			}
			if old, err := os.ReadFile(p); err == nil {
				_ = json.Unmarshal(old, &head)
			}
			tr := c.UI.Scaffold(c.Types, lang, prune)
			doc := struct {
				Schema string `json:"$schema,omitempty"`
				*app.UITranslations
			}{head.Schema, tr}
			var b strings.Builder
			enc := json.NewEncoder(&b)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			if err := enc.Encode(doc); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
				return err
			}
			empty := strings.Count(b.String(), `": ""`)
			fmt.Printf("wrote %s: %d entries to translate\n", p, empty)
			return nil
		},
	}
	c.Flags().StringVar(&lang, "lang", "", "language tag listed in ui/app.json languages")
	c.Flags().BoolVar(&prune, "prune", false, "drop entries no page uses")
	c.Flags().StringVar(&env, "env", "dev", "environment file env/<env>.json")
	_ = c.MarkFlagRequired("lang")
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
