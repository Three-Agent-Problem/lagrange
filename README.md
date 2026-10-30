# Lagrange

A Lagrange point is where a body can hold still between two others. Lagrange is where A2A tasks wait safely between agents, people and credentials.

Operator-side building blocks for running [A2A](https://a2a-protocol.org) agents, built on the official Go SDK, [a2a-go](https://github.com/a2aproject/a2a-go). With them you can see what your agents are doing across hops, answer the tasks that wait on a person, and hand agents credentials without passing tokens through the A2A chain.

> **Status: early.** Everything is in memory and single-process. Durable storage comes next (see [Roadmap](#roadmap)).

| Package | What it does |
|---|---|
| [`observer`](observer/observer.go) | A `push.ConfigStore` decorator. Every task an a2a-go agent runs also pushes its events to an operator endpoint, and the executor doesn't change. |
| [`console`](console/) | A web console with a task tree across agents, a per-task timeline, time in state (working / waiting on a human / waiting on a credential), and an **inbox** that renders `structuredinput` schemas as forms and resumes the task. |
| [`credentials`](credentials/credentials.go) | A `Broker` interface for `AUTH_REQUIRED`: look up a user's connection, start a connect flow, wait for it, then call the provider API through the broker. |
| [`credentials/nango`](credentials/nango/) | A broker built on [Nango](https://nango.dev): Connect sessions, connection lookup by end-user tag, the proxy, and signed auth webhooks. |
| [`credentials/demo`](credentials/demo/) | An in-memory broker with a fake consent page and a fake calendar, for demos and tests. |
| [`links`](links/links.go) | A metadata convention that records "this task delegated to that task on that agent", so the console can draw the tree. |
| [`cmd/console`](cmd/console/main.go) | The console as a standalone server. |

## Quick start: watch an agent

Run the console:

```sh
go run ./cmd/console -token s3cret            # http://localhost:8088
```

Point an a2a-go agent at it. Its executor stays the same, and only the push config store is wrapped:

```go
import (
	"net/url"

	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/push"
	"github.com/three-agent-problem/lagrange/observer"
)

pushURL := "http://localhost:8088/push?agent=" + url.QueryEscape(agentBaseURL)
store := observer.Wrap(push.NewInMemoryStore(), pushURL, "s3cret")
sender := push.NewHTTPPushSender(&push.HTTPSenderConfig{
	AllowPrivateNetworks: true, // only because the console is on localhost
})
handler := a2asrv.NewHandler(executor, a2asrv.WithPushNotifications(store, sender))
```

The console resolves the agent's card from `agent=` to name it, and talks to the agent with an A2A client when you answer from the inbox.

## The inbox

A task shows up in the inbox when it is:

- **`INPUT_REQUIRED`** with a [structured input](https://github.com/a2aproject/a2a-go/tree/main/a2aext/structuredinput) request. The JSON Schema is rendered as a form: a lone boolean becomes Approve/Reject buttons, enums become selects, and numbers and strings become inputs. Submitting the form sends `structuredinput.NewResponse(...)` back to the agent with `returnImmediately`, and records who answered in the message metadata under `console.AnsweredByKey`.
- **`AUTH_REQUIRED`** with a credential challenge (`credentials.NewChallengeParts`). The inbox shows a *Connect* button that opens the broker's link.

With `-slack-webhook`, a Slack incoming webhook gets a link to the inbox whenever a task starts waiting on a person.

> The console has **no login yet**. Anyone who can reach it can answer, and "answered by" is whatever they type. Keep it on localhost or behind an authenticating proxy.

## AUTH_REQUIRED with a broker

a2a-go keeps consuming an executor's events after `AUTH_REQUIRED`, while non-streaming callers get their response at that point. So an executor can ask for a credential, wait, and carry on by itself (A2A spec §7.6.1):

```go
grant, err := broker.Lookup(ctx, user, "google-calendar")
if errors.Is(err, credentials.ErrNoGrant) {
	challenge, err := broker.Begin(ctx, user, "google-calendar")
	if err != nil { /* ... */ }
	msg := a2a.NewMessageForTask(a2a.MessageRoleAgent, execCtx,
		credentials.NewChallengeParts(challenge, "I need access to your calendar.")...)
	if !yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateAuthRequired, msg), nil) {
		return
	}
	grant, err = broker.Await(ctx, challenge) // the user connects in the browser
	if err != nil { /* ErrDenied, ErrExpired, ctx.Err() */ }
	yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateWorking, nil), nil)
}
err = grant.Call(ctx, http.MethodPost, "/calendar/v3/calendars/primary/events", event, &created)
```

The provider token never enters the agent's A2A messages. `Grant.Call` goes through the broker, which for Nango means the [proxy](https://nango.dev/docs/reference/backend/http-api/proxy/post).

### Nango setup

1. Create a [Nango](https://app.nango.dev) environment, either Cloud or [self-hosted](https://nango.dev/docs/guides/platform/free-self-hosting). Self-hosting's free tier covers auth, the proxy and Connect UI.
2. Add an integration, for example Google Calendar with the `https://www.googleapis.com/auth/calendar.events` scope. Its integration ID (e.g. `google-calendar`) is what you pass to `Lookup`/`Begin`.
3. Create the broker:

   ```go
   client, err := nango.NewClient(os.Getenv("NANGO_SECRET_KEY"), nango.DefaultBaseURL, nil)
   broker := nango.NewBroker(client, nango.BrokerConfig{
   	WebhookSigningKey: os.Getenv("NANGO_WEBHOOK_SIGNING_KEY"), // optional
   })
   ```

4. Webhooks are optional. `Await` polls Nango every 2 s, which works locally. If Nango can reach your server, mount `broker.WebhookHandler()` and set the webhook URL and signing key in the Nango environment settings. A signed `auth`/`creation` webhook makes `Await` check at once. The grant always comes from Nango's connection list for that user, never from the webhook body. Without a signing key the handler refuses every webhook.

Connections are tagged `end_user_id=<user>` (`nango.EndUserTag`), and `Lookup` returns the newest connection for the integration that has no errors.

## Conventions on the wire

These are metadata keys under this project's namespace. Agents that don't know them can ignore them.

| Key | On | Meaning |
|---|---|---|
| `https://threeagents.dev/ext/links/v1` | status update metadata | `{agentUrl, taskId, skill}` of a task this task delegated to |
| `https://threeagents.dev/ext/credential-challenge/v1` | data part metadata | marks the part `{id, integration, url, expiresAt}` of an `AUTH_REQUIRED` message |
| `https://threeagents.dev/ext/answered-by/v1` | message metadata | who answered from the inbox (self-reported for now) |

## Caveats

- `observer.Store` adds its subscriber (ID `operator-observer`) to every task's push config list. Clients that list a task's push configs will see it.
- `HTTPSenderConfig.AllowPrivateNetworks` turns off a2a-go's SSRF guard for pushes (spec §13). Use it only when the console is on a private network you control.
- Pushes are fire-and-forget in a2a-go. If the console is down, it misses events.

## Roadmap

1. Postgres task store, push config store and a durable push queue. A `front` mode that proxies any A2A agent.
2. Slack interactive approvals, email magic links, SLA timers (remind, escalate, cancel), and OpenTelemetry with `traceparent` propagated across hops.
3. Signed-card verification and per-tenant consoles.

## Development

```sh
go vet ./... && go test -race ./...
```

To develop against a local a2a-go checkout, use a `go.work` file (one is generated by the workspace bootstrap script). This module currently requires a2a-go at commit `2692308` (after v2.7.0), because `a2aext/structuredinput` isn't in a tagged release yet.

## License

Apache-2.0
