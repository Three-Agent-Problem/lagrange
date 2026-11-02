package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/three-agent-problem/lagrange/client"
)

// cliConfig is what `lagrange login` remembers: the server, the API key, and the tunnel
// tokens `lagrange tunnel --address` minted with it.
type cliConfig struct {
	Server    string `json:"server"`
	Key       string `json:"key"`
	Workspace string `json:"workspace"`
	Role      string `json:"role"`
	// Tokens maps "server handle" to an address token.
	Tokens map[string]string `json:"tokens,omitempty"`
}

const defaultServer = "http://localhost:8080"

// configPath is $LAGRANGE_CONFIG, or lagrange/config.json in the user's config directory.
func configPath() (string, error) {
	if p := os.Getenv("LAGRANGE_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lagrange", "config.json"), nil
}

// loadConfig returns the saved config, or an empty one.
func loadConfig() (*cliConfig, error) {
	c := &cliConfig{}
	p, err := configPath()
	if err != nil {
		return c, err
	}
	raw, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(raw, c)
}

// save writes the config readable by the user only: it holds secrets.
func (c *cliConfig) save() error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(c, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// apiFlags are --server and --key, for commands that call the API.
type apiFlags struct{ server, key *string }

func addAPIFlags(fs *flag.FlagSet) apiFlags {
	return apiFlags{
		server: fs.String("server", "", "Lagrange server URL (env LAGRANGE_SERVER; default: the one you logged in to)."),
		key:    fs.String("key", "", "API key (env LAGRANGE_KEY; default: the one from lagrange login)."),
	}
}

// resolve picks the server and key: flags, then environment, then the saved login.
func (f apiFlags) resolve(cfg *cliConfig) (server, key string) {
	server = firstOf(*f.server, os.Getenv("LAGRANGE_SERVER"))
	key = firstOf(*f.key, os.Getenv("LAGRANGE_KEY"))
	// The saved key belongs to the saved server: use it only there.
	if server == "" || sameServer(server, cfg.Server) {
		server = firstOf(server, cfg.Server)
		key = firstOf(key, cfg.Key)
	}
	return firstOf(server, defaultServer), key
}

// client returns an API client, or an error that says how to log in.
func (f apiFlags) client(cfg *cliConfig) (*client.Client, error) {
	server, key := f.resolve(cfg)
	if key == "" {
		return nil, errors.New("not logged in: run `lagrange login --server " + server + "` (or set LAGRANGE_KEY)")
	}
	return client.New(server, key), nil
}

func sameServer(a, b string) bool { return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/") }

func firstOf(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
