package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/three-agent-problem/lagrange/client"
	"github.com/three-agent-problem/lagrange/directory"
)

// openBrowser opens a URL in the default browser. Tests replace it.
var openBrowser = func(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}

// stdin is where the paste flow reads the code. Tests replace it.
var stdin io.Reader = os.Stdin

// login gets an API key through the browser: the person signs in on the web (password,
// GitHub, Google, whatever the server offers), approves the CLI, and the browser hands a
// one-time code back to a listener on 127.0.0.1. Without a browser, the page shows the
// code to paste instead. Either way the code is useless without this process's PKCE
// verifier.
func login(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	server := fs.String("server", "", "Lagrange server URL (env LAGRANGE_SERVER; default: the last one, or "+defaultServer+").")
	noBrowser := fs.Bool("no-browser", false, "Don't open a browser: print a link and paste the code it shows.")
	name := fs.String("name", "", `Name of the key, shown in Settings (default "lagrange CLI on <host>").`)
	email := fs.String("email", "", "Log in with this email and a password instead of the browser (a fresh server, a box without one). The password comes from LAGRANGE_PASSWORD, or the first line of stdin.")
	workspace := fs.String("workspace", "", "With --email: the workspace of the key (its slug; default: your first).")
	_ = fs.Parse(args)

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	srv := strings.TrimSuffix(firstOf(*server, os.Getenv("LAGRANGE_SERVER"), cfg.Server, defaultServer), "/")
	if *name == "" {
		host, _ := os.Hostname()
		*name = "lagrange CLI on " + firstOf(host, "this computer")
	}
	var l *client.Login
	if *email != "" {
		password, err := readPassword()
		if err != nil {
			return err
		}
		if l, err = client.PasswordLogin(ctx, srv, *email, password, *workspace, *name); err != nil {
			return fmt.Errorf("login: %w", err)
		}
	} else {
		verifier := rand.Text() + rand.Text()
		q := url.Values{"challenge": {directory.PKCEChallenge(verifier)}, "name": {*name}}
		var code string
		if *noBrowser {
			code, err = pasteCode(srv, q)
		} else {
			code, err = browserCode(ctx, srv, q)
		}
		if err != nil {
			return err
		}
		if l, err = client.ClaimLogin(ctx, srv, code, verifier); err != nil {
			return fmt.Errorf("login: %w", err)
		}
	}
	if cfg.Server != "" && !sameServer(cfg.Server, srv) {
		cfg.Tokens = nil
	}
	cfg.Server, cfg.Key, cfg.Workspace, cfg.Role = srv, l.Key, l.Workspace, l.Role
	if err := cfg.save(); err != nil {
		return err
	}
	me, err := client.New(srv, l.Key).Me(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Logged in to %s as @%s, in workspace %s (%s).\n", srv, me.User.Handle, l.Workspace, l.Role)
	return nil
}

func authorizeURL(server string, q url.Values) string { return server + "/cli/authorize?" + q.Encode() }

// browserCode waits for the browser to bring the code to a loopback listener.
func browserCode(ctx context.Context, server string, q url.Values) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer ln.Close()
	q.Set("port", fmt.Sprint(ln.Addr().(*net.TCPAddr).Port))
	codes := make(chan string, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if r.URL.Path != "/callback" || code == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>Lagrange</title><body style="font:16px system-ui;margin:4rem auto;max-width:28rem;text-align:center"><h1>You're logged in</h1><p>Go back to your terminal. You can close this tab.</p>`)
		select {
		case codes <- code:
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		// Shut down gracefully: the browser still has to get the page saying it worked.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	u := authorizeURL(server, q)
	fmt.Println("Opening your browser to log in. If it doesn't open, visit:\n  " + u)
	if err := openBrowser(u); err != nil {
		fmt.Println("(Couldn't open a browser: run `lagrange login --no-browser` to paste a code instead.)")
	}
	ctx, cancel := context.WithTimeout(ctx, directory.CLILoginTTL+time.Minute)
	defer cancel()
	select {
	case code := <-codes:
		return code, nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", errors.New("login: timed out waiting for the browser")
		}
		return "", ctx.Err()
	}
}

// pasteCode prints the link and reads the code the page shows.
func pasteCode(server string, q url.Values) (string, error) {
	fmt.Println("Open this link on any device, log in, and approve:\n  " + authorizeURL(server, q))
	fmt.Print("Then paste the code here: ")
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if code := strings.TrimSpace(line); code != "" {
		return code, nil
	}
	if err != nil {
		return "", err
	}
	return "", errors.New("login: no code")
}

func logout(args []string) error {
	fs := flag.NewFlagSet("logout", flag.ExitOnError)
	_ = fs.Parse(args)
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Println("Logged out. The key still works until you revoke it in Settings → API keys.")
	return nil
}

func whoami(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("whoami", flag.ExitOnError)
	af := addAPIFlags(fs)
	_ = fs.Parse(args)
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	c, err := af.client(cfg)
	if err != nil {
		return err
	}
	me, err := c.Me(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("@%s (%s) on %s, in workspace %s (%s)\n", me.User.Handle, me.User.Email, c.Server, me.Key.Workspace, me.Key.Role)
	return nil
}

// readPassword returns LAGRANGE_PASSWORD, or the first line of stdin (asking for it
// when stdin is a terminal; it echoes, so prefer the variable or a pipe).
func readPassword() (string, error) {
	if p := os.Getenv("LAGRANGE_PASSWORD"); p != "" {
		return p, nil
	}
	if f, ok := stdin.(*os.File); ok {
		if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
			fmt.Print("Password (shown as you type; LAGRANGE_PASSWORD avoids that): ")
		}
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return "", errors.New("login: no password (set LAGRANGE_PASSWORD or pipe it in)")
	}
	return line, nil
}
