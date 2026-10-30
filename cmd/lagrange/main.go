// Command lagrange runs a Lagrange server and connects agents to one.
//
//	lagrange serve                       run the server (configured by environment variables)
//	lagrange tunnel --to URL             connect a local A2A agent to a server
//	lagrange tunnel --acp "CMD"          connect a coding agent (Agent Client Protocol) to a server
//	lagrange token --address HANDLE      mint an address token from the server's database
//	lagrange migrate                     apply database migrations and exit
//	lagrange healthcheck                 exit 0 if the local server is healthy (for Docker)
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
	"strings"
	"syscall"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aext/structuredinput"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/integrations/executor/a2aacp"

	"github.com/three-agent-problem/lagrange/directory"
	"github.com/three-agent-problem/lagrange/pg"
	"github.com/three-agent-problem/lagrange/platform"
	"github.com/three-agent-problem/lagrange/tunnel"
	"github.com/three-agent-problem/lagrange/web"
)

const usage = `Usage: lagrange <command> [flags]

Commands:
  serve     run the server
  tunnel    connect an agent to a server
  token     mint a token for an address (needs the database)
  migrate   apply database migrations and exit
  healthcheck  check the local server (for container health checks)

Run "lagrange <command> -h" for the flags of a command.
`

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
	case "token":
		err = token(ctx, args)
	case "migrate":
		err = migrate(ctx, args)
	case "healthcheck":
		err = healthcheck(ctx)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "lagrange: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
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
	p, err := platform.New(ctx, platform.Config{
		DatabaseURL:      *dbURL,
		PublicURL:        *publicURL,
		AdminEmail:       os.Getenv("LAGRANGE_ADMIN_EMAIL"),
		AdminPassword:    os.Getenv("LAGRANGE_ADMIN_PASSWORD"),
		AdminName:        os.Getenv("LAGRANGE_ADMIN_NAME"),
		AdminHandle:      os.Getenv("LAGRANGE_ADMIN_HANDLE"),
		WorkerID:         os.Getenv("LAGRANGE_WORKER_ID"),
		AllowPrivatePush: os.Getenv("LAGRANGE_ALLOW_PRIVATE_PUSH") == "1",
		Web:              web.FS(),
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
	logger.Info("lagrange listening", "addr", *addr, "public", *publicURL, "web", web.FS() != nil)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func runTunnel(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("tunnel", flag.ExitOnError)
	server := fs.String("server", env("LAGRANGE_SERVER", "http://localhost:8080"), "Lagrange server URL (env LAGRANGE_SERVER).")
	tok := fs.String("token", os.Getenv("LAGRANGE_TOKEN"), "Address token (env LAGRANGE_TOKEN).")
	to := fs.String("to", "", "Base URL of the local A2A agent, e.g. http://localhost:9001.")
	acp := fs.String("acp", "", `Coding agent command speaking ACP, e.g. "claude-agent-acp" or "opencode acp".`)
	dir := fs.String("dir", ".", "Working directory of the --acp agent.")
	name := fs.String("name", "", "Agent name on its card (--acp only).")
	capacity := fs.Int("capacity", tunnel.DefaultCapacity, "Tasks this copy runs at once.")
	expose := fs.Bool("expose", false, "Let the server forward raw HTTP for /t/<handle>/... (webhooks) to the agent.")
	_ = fs.Parse(args)

	if *tok == "" {
		return errors.New("tunnel: --token is required (create an address in the web app to get one)")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	var handler http.Handler
	switch {
	case *to != "" && *acp != "":
		return errors.New("tunnel: use --to or --acp, not both")
	case *to != "":
		target, err := url.Parse(*to)
		if err != nil || target.Host == "" {
			return fmt.Errorf("tunnel: invalid --to URL %q", *to)
		}
		handler = tunnel.Proxy(target)
	case *acp != "":
		h, closeFn, err := acpHandler(ctx, strings.Fields(*acp), *dir, *name, logger)
		if err != nil {
			return err
		}
		defer closeFn()
		handler = h
	default:
		return errors.New("tunnel: --to or --acp is required")
	}
	logger.Info("connecting", "server", *server, "capacity", *capacity)
	err := tunnel.Run(ctx, tunnel.ClientConfig{
		Server: *server, Token: *tok, Capacity: *capacity, Handler: handler, Expose: *expose,
		UserAgent: "lagrange-tunnel", Logger: logger,
		OnConnect: func() { logger.Info("tunnel connected: the agent is online") },
	})
	if errors.Is(err, tunnel.ErrUnauthorized) {
		return errors.New("tunnel: the server rejected the token")
	}
	return err
}

// acpHandler serves a coding agent as an A2A agent, for the tunnel. Its permission
// prompts become structured input requests, which land in the sender's inbox.
func acpHandler(ctx context.Context, argv []string, dir, name string, logger *slog.Logger) (http.Handler, func(), error) {
	exec, err := a2aacp.NewExecutor(ctx, a2aacp.Process{Argv: argv, Dir: dir, Stderr: os.Stderr}, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("starting %s: %w", argv[0], err)
	}
	if name == "" {
		name = argv[0]
	}
	card := &a2a.AgentCard{
		Name:        name,
		Description: "A coding agent (" + strings.Join(argv, " ") + ") connected through a Lagrange tunnel. It asks before running tools.",
		Version:     "1.0.0",
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface("http://agent/rpc", a2a.TransportProtocolJSONRPC),
		},
		Capabilities: a2a.AgentCapabilities{
			Streaming:  true,
			Extensions: []a2a.AgentExtension{structuredinput.NewExtension("")},
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []a2a.AgentSkill{{
			ID: "code", Name: "Code", Description: "Reads, writes and runs code in its workspace.",
			Tags: []string{"coding", "acp"},
		}},
	}
	h := a2asrv.NewHandler(exec)
	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/rpc", a2asrv.NewJSONRPCHandler(h))
	return mux, func() {
		if err := exec.Close(); err != nil {
			logger.Warn("stopping the agent", "error", err)
		}
	}, nil
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
