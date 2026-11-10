// Command lagrange runs a Lagrange server and connects agents to one.
//
//	lagrange serve                       run the server (configured by environment variables)
//	lagrange tunnel --to URL             connect a local A2A agent to a server
//	lagrange tunnel --acp "CMD"          connect a coding agent (Agent Client Protocol) to a server
//	lagrange tunnel --echo               connect a built-in test agent that repeats what it is sent
//	lagrange tunnel --address HANDLE     connect as an address, with a token minted through your login
//	lagrange login                       log in through the browser and save an API key
//	lagrange logout / whoami             forget the saved login / show who it is
//	lagrange mcp                         serve your workspace to an MCP client (stdio)
//	lagrange token --address HANDLE      mint an address token from the server's database
//	lagrange migrate                     apply database migrations and exit
//	lagrange healthcheck                 exit 0 if the local server is healthy (for Docker)
//	lagrange version                     print the version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/three-agent-problem/lagrange/client"
	"github.com/three-agent-problem/lagrange/directory"
	"github.com/three-agent-problem/lagrange/notify"
	"github.com/three-agent-problem/lagrange/pg"
	"github.com/three-agent-problem/lagrange/platform"
	"github.com/three-agent-problem/lagrange/tunnel"
	"github.com/three-agent-problem/lagrange/web"
)

const usage = `Usage: lagrange <command> [flags]

Commands:
  serve     run the server
  tunnel    connect an agent to a server
  login     log in through the browser and save an API key
  logout    forget the saved login
  whoami    show who the saved login is
  mcp       serve your workspace to an MCP client over stdio (claude mcp add lagrange -- lagrange mcp)
  send      send a message to an address: lagrange send @x "…" [--wait]
  task      show a task, or wait for it: lagrange task <id> [--wait]
  answer    answer a task that waits on you: lagrange answer <id> "…" [--approve|--reject]
  waiting   list what waits on someone [--mine]
  runs      list runs [-q text] [--status s]
  run       show a run and its tasks: lagrange run <id>
  token     mint a token for an address (needs the database)
  migrate   apply database migrations and exit
  healthcheck  check the local server (for container health checks)
  version   print the version

Run "lagrange <command> -h" for the flags of a command.
`

// Set by release builds: -ldflags "-X main.version=0.1.0 -X main.commit=... -X main.date=...".
var (
	version = "dev"
	commit  = ""
	date    = ""
)

// versionString is the version, commit and build date. Builds without ldflags (go install,
// go build) take what the Go toolchain recorded: the module version and VCS stamp.
func versionString() string {
	v, c, d := version, commit, date
	if bi, ok := debug.ReadBuildInfo(); ok && v == "dev" {
		if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			v = strings.TrimPrefix(bi.Main.Version, "v")
		}
		for _, s := range bi.Settings {
			switch {
			case s.Key == "vcs.revision" && c == "":
				c = s.Value
			case s.Key == "vcs.time" && d == "":
				d = s.Value
			}
		}
	}
	var extra []string
	if c != "" {
		extra = append(extra, c[:min(len(c), 7)])
	}
	if d != "" {
		extra = append(extra, d[:min(len(d), 10)])
	}
	if len(extra) == 0 {
		return v
	}
	return v + " (" + strings.Join(extra, ", ") + ")"
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "serve":
		err = serve(ctx, args)
	case "tunnel":
		err = runTunnel(ctx, args)
	case "login":
		err = login(ctx, args)
	case "logout":
		err = logout(args)
	case "whoami":
		err = whoami(ctx, args)
	case "mcp":
		err = runMCP(ctx, args)
	case "send":
		err = send(ctx, args)
	case "task":
		err = task(ctx, args)
	case "answer":
		err = answer(ctx, args)
	case "waiting":
		err = waiting(ctx, args)
	case "runs":
		err = runs(ctx, args)
	case "run":
		err = run(ctx, args)
	case "token":
		err = token(ctx, args)
	case "migrate":
		err = migrate(ctx, args)
	case "healthcheck":
		err = healthcheck(ctx)
	case "version", "--version", "-v":
		fmt.Println("lagrange", versionString())
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "lagrange: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.msg != "" {
			fmt.Fprintln(os.Stderr, "lagrange:", ee.msg)
		}
		os.Exit(ee.code)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "lagrange:", err)
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func databaseFlag(fs *flag.FlagSet) *string {
	return fs.String("database-url", env("DATABASE_URL", "postgres://localhost:5432/lagrange?sslmode=disable"), "Postgres URL (env DATABASE_URL).")
}

func serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", env("LAGRANGE_ADDR", ":8080"), "Listen address (env LAGRANGE_ADDR).")
	dbURL := databaseFlag(fs)
	publicURL := fs.String("public-url", env("LAGRANGE_PUBLIC_URL", "http://localhost:8080"), "URL agents and people use to reach this server (env LAGRANGE_PUBLIC_URL).")
	_ = fs.Parse(args)

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	var email notify.Email
	if u := os.Getenv("LAGRANGE_SMTP_URL"); u != "" {
		s, err := notify.ParseSMTP(u, env("LAGRANGE_SMTP_FROM", "lagrange@localhost"))
		if err != nil {
			return err
		}
		email = s
	}
	var providers []*platform.OAuthProvider
	if id := os.Getenv("LAGRANGE_GITHUB_CLIENT_ID"); id != "" {
		providers = append(providers, platform.GitHubProvider(id, os.Getenv("LAGRANGE_GITHUB_CLIENT_SECRET")))
	}
	if id := os.Getenv("LAGRANGE_GOOGLE_CLIENT_ID"); id != "" {
		providers = append(providers, platform.GoogleProvider(id, os.Getenv("LAGRANGE_GOOGLE_CLIENT_SECRET")))
	}
	signup := env("LAGRANGE_SIGNUP", platform.SignupInvite)
	if signup != platform.SignupInvite && signup != platform.SignupOpen {
		return fmt.Errorf("LAGRANGE_SIGNUP is %q; use invite or open", signup)
	}
	p, err := platform.New(ctx, platform.Config{
		DatabaseURL:      *dbURL,
		PublicURL:        *publicURL,
		AdminEmail:       os.Getenv("LAGRANGE_ADMIN_EMAIL"),
		AdminPassword:    os.Getenv("LAGRANGE_ADMIN_PASSWORD"),
		AdminName:        os.Getenv("LAGRANGE_ADMIN_NAME"),
		AdminHandle:      os.Getenv("LAGRANGE_ADMIN_HANDLE"),
		WorkerID:         os.Getenv("LAGRANGE_WORKER_ID"),
		AllowPrivatePush: os.Getenv("LAGRANGE_ALLOW_PRIVATE_PUSH") == "1",
		Dev:              os.Getenv("LAGRANGE_DEV") == "1",
		DevSeed:          os.Getenv("LAGRANGE_DEV_SEED"),
		Web:              web.FS(),
		Email:            email,
		Signup:           signup,
		OAuth:            providers,
		TrustProxy:       os.Getenv("LAGRANGE_TRUST_PROXY") == "1",
		Logger:           logger,
	})
	if err != nil {
		return err
	}
	defer p.Close()

	srv := &http.Server{Addr: *addr, Handler: p.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	logger.Info("lagrange listening", "version", versionString(), "addr", *addr, "public", *publicURL, "web", web.FS() != nil, "email", email != nil, "dev", os.Getenv("LAGRANGE_DEV") == "1")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func runTunnel(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("tunnel", flag.ExitOnError)
	af := addAPIFlags(fs)
	tok := fs.String("token", os.Getenv("LAGRANGE_TOKEN"), "Address token (env LAGRANGE_TOKEN). Not needed with --address after lagrange login.")
	address := fs.String("address", "", "Connect as this agent address, minting its token with your login (lagrange login).")
	create := fs.Bool("create", false, "With --address: create the address if it doesn't exist.")
	to := fs.String("to", "", "Base URL of the local A2A agent, e.g. http://localhost:9001.")
	acp := fs.String("acp", "", `Coding agent command speaking ACP, e.g. "claude-agent-acp" or "opencode acp".`)
	dir := fs.String("dir", ".", "Working directory of the --acp agent.")
	echo := fs.Bool("echo", false, "Serve a built-in test agent that answers with what it was sent.")
	execCmd := fs.String("exec", "", `Run this command for each message: the text on stdin, the reply (text or JSON) on stdout.`)
	httpURL := fs.String("http", "", "POST each message as JSON to this URL (an n8n, Zapier or Make webhook, any HTTP endpoint) and reply with its response.")
	headers := headerFlag{}
	fs.Var(headers, "header", `With --http: a header to send, "Name: value" (repeatable).`)
	timeout := fs.Duration("timeout", 5*time.Minute, "With --exec or --http: how long one message may take.")
	name := fs.String("name", "", "Agent name on its card (--acp, --echo, --exec, --http).")
	description := fs.String("description", "", "With --exec or --http: what the agent does, for its card and public page.")
	capacity := fs.Int("capacity", tunnel.DefaultCapacity, "Tasks this copy runs at once.")
	expose := fs.Bool("expose", false, "Let the server forward raw HTTP for /t/<handle>/... (webhooks) to the agent.")
	var acpOpts acpOptions
	acpFlags(fs, &acpOpts)
	_ = fs.Parse(args)

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	server, _ := af.resolve(cfg)
	handle := strings.TrimPrefix(strings.TrimSpace(*address), "@")
	saved := false // the token came from the saved login's cache
	if *tok == "" {
		var minted bool
		if handle == "" {
			return errors.New("tunnel: --token or --address is required (lagrange login, then --address my-agent --create)")
		}
		if *tok, minted, err = addressToken(ctx, af, cfg, handle, *create); err != nil {
			return err
		}
		saved = !minted
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	var handler http.Handler
	modes := 0
	for _, on := range []bool{*to != "", *acp != "", *echo, *execCmd != "", *httpURL != ""} {
		if on {
			modes++
		}
	}
	switch {
	case modes > 1:
		return errors.New("tunnel: use one of --to, --acp, --exec, --http and --echo")
	case *echo:
		handler = echoHandler(*name)
	case *execCmd != "":
		desc := firstOf(*description, "A command-line program connected through a Lagrange tunnel.")
		handler = wrapHandler(wrapCard(firstOf(*name, strings.Fields(*execCmd)[0]), desc, ""), execFunc(*execCmd, *timeout))
	case *httpURL != "":
		target, err := url.Parse(*httpURL)
		if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
			return fmt.Errorf("tunnel: invalid --http URL %q", *httpURL)
		}
		desc := firstOf(*description, "A workflow connected through a Lagrange tunnel.")
		handler = wrapHandler(wrapCard(firstOf(*name, "Workflow"), desc, ""), httpFunc(*httpURL, http.Header(headers), *timeout))
	case *to != "":
		target, err := url.Parse(*to)
		if err != nil || target.Host == "" {
			return fmt.Errorf("tunnel: invalid --to URL %q", *to)
		}
		handler = tunnel.Proxy(target, logger)
		if err := tunnel.Probe(ctx, target); err != nil {
			logger.Warn("nothing answers at "+*to+" yet: start your agent there (the tunnel connects anyway, and tasks fail until it answers)", "error", err)
		}
	case *acp != "":
		acpOpts.argv, acpOpts.dir, acpOpts.name = strings.Fields(*acp), *dir, *name
		h, closeFn, err := acpHandler(ctx, &acpOpts, logger)
		if err != nil {
			return err
		}
		defer closeFn()
		handler = h
	default:
		return errors.New("tunnel: one of --to, --acp, --exec, --http or --echo is required (--echo connects a built-in test agent)")
	}
	run := func(token string) error {
		logger.Info("connecting", "server", server, "capacity", *capacity)
		return tunnel.Run(ctx, tunnel.ClientConfig{
			Server: server, Token: token, Capacity: *capacity, Handler: handler, Expose: *expose,
			UserAgent: "lagrange-tunnel", Logger: logger,
			OnConnect: func() { logger.Info("tunnel connected: the agent is online") },
		})
	}
	err = run(*tok)
	if errors.Is(err, tunnel.ErrUnauthorized) && saved {
		// A saved token was revoked: mint a fresh one, once.
		delete(cfg.Tokens, tokenKey(server, handle))
		if *tok, _, err = addressToken(ctx, af, cfg, handle, false); err != nil {
			return err
		}
		err = run(*tok)
	}
	if errors.Is(err, tunnel.ErrUnauthorized) {
		return errors.New("tunnel: the server rejected the token")
	}
	return err
}

func tokenKey(server, handle string) string { return strings.TrimSuffix(server, "/") + " " + handle }

// addressToken returns the saved tunnel token for handle, or mints one with the API key
// (creating the address first with create) and saves it. minted is true for a new one.
func addressToken(ctx context.Context, af apiFlags, cfg *cliConfig, handle string, create bool) (tok string, minted bool, err error) {
	c, err := af.client(cfg)
	if err != nil {
		return "", false, fmt.Errorf("tunnel: %w", err)
	}
	k := tokenKey(c.Server, handle)
	if t := cfg.Tokens[k]; t != "" {
		return t, false, nil
	}
	host, _ := os.Hostname()
	if create {
		tok, err = c.CreateAgent(ctx, handle)
		if client.StatusOf(err) == http.StatusConflict {
			err, tok = nil, ""
		}
		if err != nil {
			return "", false, fmt.Errorf("tunnel: creating @%s: %w", handle, err)
		}
	}
	if tok == "" {
		if tok, err = c.MintToken(ctx, handle, "lagrange tunnel on "+firstOf(host, "a computer")); err != nil {
			if client.StatusOf(err) == http.StatusNotFound {
				return "", false, fmt.Errorf("tunnel: no address @%s (add --create to make it)", handle)
			}
			return "", false, fmt.Errorf("tunnel: minting a token for @%s: %w", handle, err)
		}
	}
	// Save it only when the key came from the saved login, so CI keys don't write files.
	if sameServer(c.Server, cfg.Server) && c.Key == cfg.Key {
		if cfg.Tokens == nil {
			cfg.Tokens = map[string]string{}
		}
		cfg.Tokens[k] = tok
		if err := cfg.save(); err != nil {
			return "", false, err
		}
	}
	return tok, true, nil
}

func token(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("token", flag.ExitOnError)
	dbURL := databaseFlag(fs)
	handle := fs.String("address", "", "Address handle (required).")
	create := fs.Bool("create", false, "Create the agent address if it doesn't exist.")
	label := fs.String("label", "cli", "Label shown in the web app.")
	_ = fs.Parse(args)
	if *handle == "" {
		return errors.New("token: --address is required")
	}
	db, err := pg.Open(ctx, *dbURL)
	if err != nil {
		return err
	}
	defer db.Close()
	dir := directory.New(db)
	if _, err := dir.Address(ctx, *handle); errors.Is(err, directory.ErrNotFound) && *create {
		if err := dir.CreateAgent(ctx, &directory.Address{Handle: *handle}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	t, err := dir.MintToken(ctx, *handle, *label)
	if err != nil {
		return err
	}
	fmt.Println(t)
	return nil
}

func healthcheck(ctx context.Context) error {
	addr := env("LAGRANGE_ADDR", ":8080")
	if strings.HasPrefix(addr, ":") {
		addr = "localhost" + addr
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz: %s", resp.Status)
	}
	return nil
}

func migrate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	dbURL := databaseFlag(fs)
	_ = fs.Parse(args)
	db, err := pg.Open(ctx, *dbURL)
	if err != nil {
		return err
	}
	db.Close()
	fmt.Println("migrations applied")
	return nil
}
