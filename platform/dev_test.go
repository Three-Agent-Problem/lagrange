package platform_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/three-agent-problem/lagrange/directory"
	"github.com/three-agent-problem/lagrange/platform"
)

// The dev reset clears agents and traffic and keeps people; without Config.Dev it does not exist.
func TestDevReset(t *testing.T) {
	newEnv(t, "").login(t, adminEmail, adminPassword).do(http.MethodPost, "/api/dev/reset", struct{}{}, http.StatusNotFound, nil)

	e := newEnv(t, "", func(c *platform.Config) { c.Dev = true })
	tok := e.agent(t, "pinger")
	e.connect(t, "pinger", tok, agentServer("pinger", replier()), 1)
	if _, err := e.client(t, "pinger", "", "").SendMessage(t.Context(), text("ping")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.p.Directory.CreateUser(t.Context(), "bob@example.com", "Bob", "bob", "hunter2hunter2", false); err != nil {
		t.Fatal(err)
	}
	e.login(t, "bob@example.com", "hunter2hunter2").do(http.MethodPost, "/api/dev/reset", struct{}{}, http.StatusForbidden, nil)

	ada := e.login(t, adminEmail, adminPassword)
	var me struct{ Dev bool }
	ada.do(http.MethodGet, "/api/me", nil, http.StatusOK, &me)
	if !me.Dev {
		t.Fatal("/api/me does not report dev")
	}
	ada.do(http.MethodPost, "/api/dev/reset", struct{}{}, http.StatusNoContent, nil)

	var runs runList
	ada.do(http.MethodGet, "/api/runs", nil, http.StatusOK, &runs)
	if len(runs.Runs) != 0 {
		t.Errorf("runs after reset = %+v", runs.Runs)
	}
	var addrs struct{ Addresses []directory.Address }
	ada.do(http.MethodGet, "/api/addresses", nil, http.StatusOK, &addrs)
	kinds := map[string]string{}
	for _, a := range addrs.Addresses {
		kinds[a.Handle] = a.Kind
	}
	if _, ok := kinds["pinger"]; ok || kinds["ada"] != "human" || kinds["bob"] != "human" {
		t.Errorf("addresses after reset = %v", kinds)
	}
	waitFor(t, "the tunnel to drop", func() bool { return e.p.Mailbox.Stats("pinger").Sessions == 0 })
}

// Reset to seed needs a seed command; with one, it restarts the command after emptying.
func TestDevResetSeed(t *testing.T) {
	newEnv(t, "", func(c *platform.Config) { c.Dev = true }).login(t, adminEmail, adminPassword).
		do(http.MethodPost, "/api/dev/reset", map[string]bool{"seed": true}, http.StatusBadRequest, nil)

	marker := filepath.Join(t.TempDir(), "seeded")
	script := filepath.Join(t.TempDir(), "seed.sh")
	// The script records whether it was asked for the scripted requests, then stays up like the agents do.
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$LAGRANGE_SEED $LAGRANGE_ADMIN_EMAIL\" >> "+marker+"\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, "", func(c *platform.Config) { c.Dev, c.DevSeed = true, script })
	ada := e.login(t, adminEmail, adminPassword)
	var me struct{ DevSeed bool }
	ada.do(http.MethodGet, "/api/me", nil, http.StatusOK, &me)
	if !me.DevSeed {
		t.Fatal("/api/me does not report devSeed")
	}
	ada.do(http.MethodPost, "/api/dev/reset", map[string]bool{"seed": true}, http.StatusNoContent, nil)
	want := "1 " + adminEmail + "\n1 " + adminEmail + "\n" // at start (no agents yet), then on reset
	waitFor(t, "the seed command to run twice", func() bool {
		raw, _ := os.ReadFile(marker)
		return string(raw) == want
	})
}
