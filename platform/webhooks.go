package platform

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/three-agent-problem/lagrange/directory"
	"github.com/three-agent-problem/lagrange/webhook"
)

// Webhook is an endpoint of a workspace, without its secret.
type Webhook struct {
	ID          int64     `json:"id"`
	URL         string    `json:"url"`
	Description string    `json:"description"`
	Events      []string  `json:"events"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"createdAt"`
	// Delivered and Failed count the last day's deliveries.
	Delivered int `json:"delivered"`
	Failed    int `json:"failed"`
	Pending   int `json:"pending"`
}

// Delivery is one event sent (or being sent) to a webhook.
type Delivery struct {
	ID          int64           `json:"id"`
	EventID     string          `json:"eventId"`
	Type        string          `json:"type"`
	Status      string          `json:"status"`
	Attempts    int             `json:"attempts"`
	LastStatus  *int            `json:"lastStatus,omitempty"`
	LastError   string          `json:"lastError,omitempty"`
	NextAt      *time.Time      `json:"nextAt,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
	DeliveredAt *time.Time      `json:"deliveredAt,omitempty"`
	Payload     json.RawMessage `json:"payload"`
}

func (p *Platform) webhookRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/webhooks/events", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": webhook.Types})
	})
	mux.Handle("GET /api/webhooks", p.in(directory.RoleAdmin, p.handleWebhooks))
	mux.Handle("POST /api/webhooks", p.in(directory.RoleAdmin, p.handleCreateWebhook))
	mux.Handle("PUT /api/webhooks/{id}", p.in(directory.RoleAdmin, p.handleUpdateWebhook))
	mux.Handle("DELETE /api/webhooks/{id}", p.in(directory.RoleAdmin, p.handleDeleteWebhook))
	mux.Handle("GET /api/webhooks/{id}/deliveries", p.in(directory.RoleAdmin, p.handleDeliveries))
	mux.Handle("POST /api/webhooks/{id}/test", p.in(directory.RoleAdmin, p.handleTestWebhook))
	mux.Handle("POST /api/webhooks/{id}/deliveries/{delivery}/retry", p.in(directory.RoleAdmin, p.handleRetryDelivery))
}

type webhookBody struct {
	URL         string   `json:"url"`
	Description string   `json:"description"`
	Events      []string `json:"events"`
	Active      *bool    `json:"active"`
}

func validEvents(events []string) ([]string, error) {
	out := []string{}
	for _, e := range events {
		if !webhook.ValidType(e) {
			return nil, errors.New("unknown event " + e + "; events are " + strings.Join(webhook.Types, ", "))
		}
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("pick at least one event")
	}
	return out, nil
}

func validURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", errors.New("the URL must be http:// or https://")
	}
	return u.String(), nil
}

// hookID reads {id} and checks the webhook belongs to the workspace.
func (p *Platform) hookID(w http.ResponseWriter, r *http.Request, s *scope) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err == nil {
		var ok bool
		err = p.DB.Pool.QueryRow(r.Context(), `SELECT true FROM webhooks WHERE id = $1 AND workspace_id = $2`, id, s.ID).Scan(&ok)
	}
	if err != nil {
		apiError(w, http.StatusNotFound, "no such webhook")
		return 0, false
	}
	return id, true
}

func (p *Platform) handleWebhooks(w http.ResponseWriter, r *http.Request, _ *directory.User, s *scope) {
	rows, err := p.DB.Pool.Query(r.Context(), `SELECT h.id, h.url, h.description, h.events, h.active, h.created_at,
			count(*) FILTER (WHERE d.status = 'delivered' AND d.created_at > now() - interval '1 day'),
			count(*) FILTER (WHERE d.status = 'failed' AND d.created_at > now() - interval '1 day'),
			count(*) FILTER (WHERE d.status = 'pending')
		FROM webhooks h LEFT JOIN webhook_deliveries d ON d.webhook_id = h.id
		WHERE h.workspace_id = $1 GROUP BY h.id ORDER BY h.created_at, h.id`, s.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	hooks, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*Webhook, error) {
		var h Webhook
		return &h, row.Scan(&h.ID, &h.URL, &h.Description, &h.Events, &h.Active, &h.CreatedAt, &h.Delivered, &h.Failed, &h.Pending)
	})
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"webhooks": hooks, "events": webhook.Types})
}

// handleCreateWebhook adds an endpoint and shows its signing secret once.
func (p *Platform) handleCreateWebhook(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	var body webhookBody
	if err := decode(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "send {url, events, description}")
		return
	}
	u, err := validURL(body.URL)
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	events, err := validEvents(body.Events)
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	secret := "whsec_" + hex.EncodeToString(b)
	h := &Webhook{URL: u, Description: strings.TrimSpace(body.Description), Events: events, Active: true}
	err = p.DB.Pool.QueryRow(r.Context(), `INSERT INTO webhooks (workspace_id, url, description, events, secret, created_by) VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at`, s.ID, h.URL, h.Description, h.Events, secret, me.ID).Scan(&h.ID, &h.CreatedAt)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"webhook": h, "secret": secret})
}

func (p *Platform) handleUpdateWebhook(w http.ResponseWriter, r *http.Request, _ *directory.User, s *scope) {
	id, ok := p.hookID(w, r, s)
	if !ok {
		return
	}
	var body webhookBody
	if err := decode(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "send {url, events, description, active}")
		return
	}
	ctx := r.Context()
	if body.URL != "" {
		u, err := validURL(body.URL)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, err := p.DB.Pool.Exec(ctx, `UPDATE webhooks SET url = $2 WHERE id = $1`, id, u); err != nil {
			apiError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if body.Events != nil {
		events, err := validEvents(body.Events)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, err := p.DB.Pool.Exec(ctx, `UPDATE webhooks SET events = $2 WHERE id = $1`, id, events); err != nil {
			apiError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if body.Active != nil {
		if _, err := p.DB.Pool.Exec(ctx, `UPDATE webhooks SET active = $2 WHERE id = $1`, id, *body.Active); err != nil {
			apiError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if _, err := p.DB.Pool.Exec(ctx, `UPDATE webhooks SET description = $2 WHERE id = $1 AND $3`, id, strings.TrimSpace(body.Description), body.Description != ""); err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *Platform) handleDeleteWebhook(w http.ResponseWriter, r *http.Request, _ *directory.User, s *scope) {
	id, ok := p.hookID(w, r, s)
	if !ok {
		return
	}
	if _, err := p.DB.Pool.Exec(r.Context(), `DELETE FROM webhooks WHERE id = $1`, id); err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeliveries lists a webhook's last 50 deliveries, newest first.
func (p *Platform) handleDeliveries(w http.ResponseWriter, r *http.Request, _ *directory.User, s *scope) {
	id, ok := p.hookID(w, r, s)
	if !ok {
		return
	}
	rows, err := p.DB.Pool.Query(r.Context(), `SELECT id, event_id, event_type, status, attempts, last_status, last_error,
			CASE WHEN status = 'pending' THEN next_at END, created_at, delivered_at, payload
		FROM webhook_deliveries WHERE webhook_id = $1 ORDER BY id DESC LIMIT 50`, id)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*Delivery, error) {
		var d Delivery
		return &d, row.Scan(&d.ID, &d.EventID, &d.Type, &d.Status, &d.Attempts, &d.LastStatus, &d.LastError, &d.NextAt, &d.CreatedAt, &d.DeliveredAt, &d.Payload)
	})
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": out})
}

func (p *Platform) handleTestWebhook(w http.ResponseWriter, r *http.Request, _ *directory.User, s *scope) {
	id, ok := p.hookID(w, r, s)
	if !ok {
		return
	}
	if err := p.Webhooks.Ping(r.Context(), id, s.Slug); err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleRetryDelivery sends a delivery again now, whatever its status.
func (p *Platform) handleRetryDelivery(w http.ResponseWriter, r *http.Request, _ *directory.User, s *scope) {
	id, ok := p.hookID(w, r, s)
	if !ok {
		return
	}
	tag, err := p.DB.Pool.Exec(r.Context(), `UPDATE webhook_deliveries SET status = 'pending', next_at = now(), attempts = 0, delivered_at = NULL
		WHERE id = $1 AND webhook_id = $2`, r.PathValue("delivery"), id)
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		apiError(w, http.StatusNotFound, "no such delivery")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
