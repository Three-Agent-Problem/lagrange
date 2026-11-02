// Package webhook sends a workspace's events to its HTTP endpoints: a task waiting on
// someone, an answer, a run that finished or failed, an agent going online or offline.
//
// Events come from the activity log, so nothing is missed while a receiver is down and
// several replicas don't send twice: a cursor (locked while it moves) turns new log
// entries into delivery rows, and senders claim due rows with SKIP LOCKED. Each request
// is signed (see [Sign] and [Verify]) and retried with backoff.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Event types.
const (
	TaskWaiting    = "task.waiting"
	TaskAnswered   = "task.answered"
	RunCompleted   = "run.completed"
	RunFailed      = "run.failed"
	AddressOnline  = "address.online"
	AddressOffline = "address.offline"
	// Ping is sent by "Send test"; endpoints always get it.
	Ping = "ping"
)

// Types lists the event types an endpoint can subscribe to.
var Types = []string{TaskWaiting, TaskAnswered, RunCompleted, RunFailed, AddressOnline, AddressOffline}

// ValidType reports whether t is a type one can subscribe to.
func ValidType(t string) bool { return slices.Contains(Types, t) }

// Headers on every delivery.
const (
	SignatureHeader = "Lagrange-Signature"
	EventHeader     = "Lagrange-Event"
	DeliveryHeader  = "Lagrange-Delivery"
)

// Event is the JSON body of a delivery.
type Event struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	CreatedAt time.Time      `json:"createdAt"`
	Workspace string         `json:"workspace"`
	Data      map[string]any `json:"data"`
}

// Sign returns the Lagrange-Signature header for body at time t:
// "t=<unix seconds>,v1=<hex HMAC-SHA256 of "<t>.<body>" with secret>".
func Sign(secret string, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	return "t=" + ts + ",v1=" + mac(secret, ts, body)
}

func mac(secret, ts string, body []byte) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(ts))
	h.Write([]byte("."))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// ErrBadSignature means a delivery's signature doesn't match, or is too old.
var ErrBadSignature = errors.New("webhook: bad signature")

// Verify checks a Lagrange-Signature header against body, and that it was made within
// tolerance of now (zero: five minutes), which stops replays.
func Verify(secret, header string, body []byte, tolerance time.Duration) error {
	if tolerance <= 0 {
		tolerance = 5 * time.Minute
	}
	var ts string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(sigs) == 0 {
		return ErrBadSignature
	}
	if d := time.Since(time.Unix(sec, 0)); d > tolerance || d < -tolerance {
		return fmt.Errorf("%w: timestamp outside the tolerance", ErrBadSignature)
	}
	want := mac(secret, ts, body)
	for _, s := range sigs {
		if hmac.Equal([]byte(s), []byte(want)) {
			return nil
		}
	}
	return ErrBadSignature
}
