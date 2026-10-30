// Command console runs the runtime console on its own: point an agent's observer push
// subscriber at http://<console>/push?agent=<agent base URL> and its tasks show up here.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/three-agent-problem/lagrange/console"
)

func main() {
	addr := flag.String("addr", ":8088", "Listen address.")
	publicURL := flag.String("public-url", "http://localhost:8088", "URL people and agents use to reach the console.")
	token := flag.String("token", os.Getenv("CONSOLE_PUSH_TOKEN"), "Push token agents must send (A2A-Notification-Token). Empty disables the check.")
	slack := flag.String("slack-webhook", os.Getenv("SLACK_WEBHOOK_URL"), "Slack incoming webhook notified when a task waits on a person.")
	flag.Parse()

	if *token == "" {
		log.Print("warning: no -token set, so the console accepts pushes from anyone who can reach it")
	}
	cfg := console.Config{PublicURL: *publicURL, Token: *token}
	if *slack != "" {
		cfg.Notifier = console.SlackWebhook{URL: *slack}
	}
	c := console.New(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Addr: *addr, Handler: c.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	log.Printf("console listening on %s (public URL %s)", *addr, *publicURL)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
