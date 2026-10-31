package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"app-maker/internal/app"
)

// credentials are stored by fw login.
type credentials struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

func credPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "fw", "credentials.json")
}

func loadCreds() credentials {
	var c credentials
	if b, err := os.ReadFile(credPath()); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	if v := os.Getenv("FW_SERVER"); v != "" {
		c.Server = v
	}
	if v := os.Getenv("FW_TOKEN"); v != "" {
		c.Token = v
	}
	if c.Server == "" {
		c.Server = "http://localhost:8080"
	}
	return c
}

// client calls the public API with the stored token.
type client struct {
	creds credentials
	http  *http.Client
}

func newClient() *client {
	return &client{creds: loadCreds(), http: &http.Client{Timeout: 60 * time.Second}}
}

func (c *client) do(method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, strings.TrimSuffix(c.creds.Server, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.creds.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.creds.Token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		var p struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
			Errors []any  `json:"errors"`
		}
		_ = json.Unmarshal(raw, &p)
		if p.Code != "" {
			return fmt.Errorf("%s %s: %d %s: %s %v", method, path, res.StatusCode, p.Code, p.Detail, p.Errors)
		}
		return fmt.Errorf("%s %s: %d %s", method, path, res.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func loginCmd() *cobra.Command {
	var server, email, password string
	c := &cobra.Command{
		Use:   "login",
		Short: "Authenticate the CLI against a server",
		RunE: func(_ *cobra.Command, _ []string) error {
			cl := newClient()
			if server != "" {
				cl.creds.Server = server
			}
			if password == "" {
				password = os.Getenv("FW_PASSWORD")
			}
			var res struct {
				Token     string `json:"token"`
				Principal struct {
					ID string `json:"id"`
				} `json:"principal"`
			}
			if err := cl.do("POST", "/v1/auth/password", map[string]any{"login": email, "password": password, "cli": true}, &res); err != nil {
				return err
			}
			cl.creds.Token = res.Token
			if err := os.MkdirAll(filepath.Dir(credPath()), 0o700); err != nil {
				return err
			}
			b, _ := json.MarshalIndent(cl.creds, "", "  ")
			if err := os.WriteFile(credPath(), b, 0o600); err != nil {
				return err
			}
			fmt.Printf("logged in to %s as %s\n", cl.creds.Server, res.Principal.ID)
			return nil
		},
	}
	c.Flags().StringVar(&server, "server", "", "server URL")
	c.Flags().StringVar(&email, "email", "", "login")
	c.Flags().StringVar(&password, "password", "", "password (or FW_PASSWORD)")
	_ = c.MarkFlagRequired("email")
	return c
}

func tokenCmd() *cobra.Command {
	t := &cobra.Command{Use: "token", Short: "Manage API keys"}
	var principal, name string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create an API key for yourself or a service account",
		RunE: func(_ *cobra.Command, _ []string) error {
			var res struct {
				Key string `json:"key"`
			}
			if err := newClient().do("POST", "/v1/auth/tokens", map[string]any{"principal": principal, "name": name}, &res); err != nil {
				return err
			}
			fmt.Println(res.Key)
			return nil
		},
	}
	create.Flags().StringVar(&principal, "principal", "", "principal ID (default: yourself)")
	create.Flags().StringVar(&name, "name", "cli", "key name")
	t.AddCommand(create)
	return t
}

// seedCmd loads seed/<env>.json through the public API. Owners are the
// calling admin (the API cannot set owners).
func seedCmd() *cobra.Command {
	var env string
	c := &cobra.Command{
		Use:   "seed [dir]",
		Short: "Load seed data into a running server",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			b, err := os.ReadFile(filepath.Join(dirArg(args), "seed", env+".json"))
			if err != nil {
				return err
			}
			var s app.Seed
			if err := json.Unmarshal(b, &s); err != nil {
				return err
			}
			cl := newClient()
			refs := map[string]string{}
			resolve := func(r string) string {
				if id, ok := refs[r]; ok {
					return id
				}
				return r
			}
			for _, u := range s.Users {
				var res struct {
					ID string `json:"id"`
				}
				if err := cl.do("POST", "/v1/auth/users", map[string]any{"email": u.Email, "password": u.Password, "displayName": u.Name, "roles": u.Roles}, &res); err != nil {
					return err
				}
				refs[u.Ref] = res.ID
			}
			for _, f := range s.Features {
				data := map[string]any{}
				for k, v := range f.Data {
					if str, ok := v.(string); ok {
						v = resolve(str)
					}
					data[k] = v
				}
				path := "/v1/features/" + f.Type
				if f.Parent != "" {
					path += "?parent=" + resolve(f.Parent)
				}
				var res struct {
					ID string `json:"id"`
				}
				if err := cl.do("POST", path, data, &res); err != nil {
					return fmt.Errorf("feature %s: %w", f.Ref, err)
				}
				refs[f.Ref] = res.ID
			}
			for _, g := range s.Grants {
				if err := cl.do("POST", "/v1/features/grant?parent="+resolve(g.Object), map[string]any{"subject": resolve(g.Subject), "role": g.Role}, nil); err != nil {
					return err
				}
			}
			fmt.Printf("seeded %d users, %d features, %d grants\n", len(s.Users), len(s.Features), len(s.Grants))
			return nil
		},
	}
	c.Flags().StringVar(&env, "env", "dev", "seed file seed/<env>.json")
	return c
}

func runsCmd() *cobra.Command {
	r := &cobra.Command{Use: "runs", Short: "Inspect flow runs"}
	var flow, status string
	var limit int
	list := &cobra.Command{
		Use:   "list",
		Short: "List recent flow runs",
		RunE: func(_ *cobra.Command, _ []string) error {
			var terms []any
			if flow != "" {
				terms = append(terms, map[string]any{"field": "/flow", "op": "eq", "value": flow})
			}
			if status != "" {
				terms = append(terms, map[string]any{"field": "/status", "op": "eq", "value": status})
			}
			q := map[string]any{"type": "flowRun", "sort": []any{map[string]any{"field": "/startedAt", "dir": "desc"}}, "limit": limit}
			if len(terms) > 0 {
				q["where"] = map[string]any{"and": terms}
			}
			var res struct {
				Items []struct {
					ID   string         `json:"id"`
					Data map[string]any `json:"data"`
				} `json:"items"`
			}
			if err := newClient().do("POST", "/v1/query", q, &res); err != nil {
				return err
			}
			for _, it := range res.Items {
				fmt.Printf("%s  %-20v %-13v %-12v %v\n", it.ID, it.Data["flow"], it.Data["status"], it.Data["trigger"], it.Data["startedAt"])
			}
			return nil
		},
	}
	list.Flags().StringVar(&flow, "flow", "", "flow name")
	list.Flags().StringVar(&status, "status", "", "succeeded, failed, rejected, loop_stopped, dead_letter")
	list.Flags().IntVar(&limit, "limit", 50, "rows")
	r.AddCommand(list)
	return r
}

type exportLine struct {
	ID     string         `json:"id"`
	Type   string         `json:"type"`
	Parent string         `json:"parent,omitempty"`
	Data   map[string]any `json:"data"`
}

// exportCmd writes every readable feature as NDJSON (spec 14.8), parents
// before children.
func exportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "export",
		Short: "Export features as NDJSON (to stdout)",
		RunE: func(_ *cobra.Command, _ []string) error {
			cl := newClient()
			var schema struct {
				Types []struct {
					Name string `json:"name"`
					ID   string `json:"id"`
				} `json:"types"`
			}
			if err := cl.do("GET", "/v1/schema/types", nil, &schema); err != nil {
				return err
			}
			w := bufio.NewWriter(os.Stdout)
			defer func() { _ = w.Flush() }()
			// Breadth-first from the root keeps parents before children.
			queue := []string{"00000000-0000-7000-8000-000000000002"}
			for len(queue) > 0 {
				parent := queue[0]
				queue = queue[1:]
				cursor := ""
				for {
					var page struct {
						Items []exportLine `json:"items"`
						Next  string       `json:"next"`
					}
					if err := cl.do("GET", "/v1/features/"+parent+"/children?limit=500&cursor="+cursor, nil, &page); err != nil {
						return err
					}
					for _, it := range page.Items {
						if it.Type == "flowRun" {
							continue
						}
						b, _ := json.Marshal(it)
						_, _ = w.Write(append(b, '\n'))
						queue = append(queue, it.ID)
					}
					if page.Next == "" {
						break
					}
					cursor = page.Next
				}
			}
			return nil
		},
	}
}

// importCmd recreates exported features with new IDs, remapping parents
// and references.
func importCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "import <file.ndjson>",
		Short: "Import features from NDJSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			cl := newClient()
			ids := map[string]string{}
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1<<20), 1<<24)
			n := 0
			for sc.Scan() {
				var l exportLine
				if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
					return err
				}
				if l.Type == "grant" || l.Type == "user" {
					continue // principals and grants are recreated by admins
				}
				for k, v := range l.Data {
					if s, ok := v.(string); ok {
						if id, ok := ids[s]; ok {
							l.Data[k] = id
						}
					}
					if k == "owner" || k == "createdAt" || k == "updatedAt" || k == "isLate" {
						delete(l.Data, k)
					}
				}
				path := "/v1/features/" + l.Type
				if p, ok := ids[l.Parent]; ok {
					path += "?parent=" + p
				}
				var res struct {
					ID string `json:"id"`
				}
				if err := cl.do("POST", path, l.Data, &res); err != nil {
					return fmt.Errorf("%s %s: %w", l.Type, l.ID, err)
				}
				ids[l.ID] = res.ID
				n++
			}
			fmt.Printf("imported %d features\n", n)
			return sc.Err()
		},
	}
}
