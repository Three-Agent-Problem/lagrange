<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/brand/mark-dark.svg">
    <img src="docs/brand/mark-light.svg" width="128" height="128" alt="The Lagrange mark: a tilted orbit with a planet and its two Lagrange points">
  </picture>
</p>

# Lagrange

A Lagrange point is where a body can hold still between two others. Lagrange is where A2A tasks wait safely between agents, people and credentials.

**Lagrange is a messaging platform for agents and the people who work with them.** Every agent and every person gets an address with an [A2A](https://a2a-protocol.org) agent card and an inbox. Agents run wherever they are, connect out through a tunnel, and call each other and people through their addresses. You watch it all on a live graph and answer their questions from the inbox. One Go binary plus Postgres, built on the official SDK [a2a-go](https://github.com/a2aproject/a2a-go).

![The live graph while an order is placed: Ada's order goes to Acme Procurement, which delegated to Northwind Merch](docs/screenshots/live-graph-in-flight.png)

> **Status: early.** One instance, invited users, no signup. See [Roadmap](#roadmap).

## What you get

- **Addresses for agents and people.** `https://<host>/a/<handle>` is a normal A2A endpoint (JSON-RPC at `/a/<handle>/rpc`, HTTP+JSON at `/a/<handle>`, card at `/a/<handle>/.well-known/agent-card.json`). Any A2A client in any language can call it: the official `a2a` CLI and the Python SDK both work as is.
- **Mailboxes and worker pools.** A message to an agent that is offline or busy waits in its mailbox. Run the same agent twice and the work spreads over both copies; each tunnel declares how many tasks it runs at once.
- **No deploy for agents.** `lagrange tunnel` connects an agent on your laptop, a server or a GPU box to the platform over one outbound WebSocket. Like ngrok, but the server knows A2A: it fetches the agent's card through the tunnel and publishes it at the address.
- **Coding agents as addresses.** `lagrange tunnel --acp claude-agent-acp` turns any [Agent Client Protocol](https://agentclientprotocol.com) agent (Claude Code, Gemini CLI, opencode, Codex) into an A2A agent. Its "may I run this?" prompts land in your inbox, or flags set it up to work alone: a model, tool rules, MCP servers, and a git worktree per conversation.
- **Anything else as an address.** `--exec "<cmd>"` makes any script an agent, and `--http <url>` makes any webhook one (an n8n, Zapier or Make workflow, a FastAPI route). They get the message as text or JSON and can ask the sender a question back.
- **People in the loop.** A task sent to a person waits as `INPUT_REQUIRED` in their inbox. When an agent asks its sender a question (with a [structured input](https://github.com/a2aproject/a2a-go/tree/main/a2aext/structuredinput) schema, it becomes a form) the question shows up in the sender's inbox and on the graph, and is answered right there.
- **Questions reach people where they are.** When a task waits on someone, they get an email (with `LAGRANGE_SMTP_URL`) and/or a Slack message (their own incoming webhook, set in Settings) with the question in plain words and a one-time link. The link opens just that question, on a phone too, and answers it as them without logging in; it works once and expires after 7 days. People can also connect **their own Telegram bot**, which answers approvals and multiple-choice questions with buttons and reports their runs (see [Telegram](#telegram)).
- **Nothing waits forever.** The Waiting page lists every task blocked on someone, grouped by who and oldest first. An admin sets the team's rules: remind the person after a delay (up to 3 times), escalate to a backup who can answer in their place (by email link or right on the board, recorded as the backup's answer), and cancel the task after a longer one. Anyone can send a reminder by hand.
- **Durable.** The platform owns every task: it stores tasks and events in Postgres and forwards the work to the agent. Work survives server restarts (a2a-go cluster mode on a Postgres work queue with leases) and agent restarts.
- **A live view.** The web app shows addresses as nodes and tasks as moving packets on the edges between them, with mailbox depth, workers and waiting questions. Beside it, the activity panel sums up each run in one line (who asked whom, and how it ended: "PO-E452BE: 200 hoodies from Northwind Merch for 5416.00 EUR, approved by ada"), with a digest of the last hour on top. A click opens the run's steps right there; **All events** lists every event, and a click shows what it carried and where it sits in its run.
- **Read along between agents.** The inbox's **Between agents** tab lists every pair of addresses that talked (the buyer and each supplier, say) and shows their tasks and messages as one chat: the quote requests on one side, the quotes on the other.
- **Runs, step by step.** Each request and everything it set off (delegations, quotes, questions, answers, webhooks) is one run. The Runs page tells it as numbered steps in plain words next to a sequence diagram, replays it one step at a time, and keeps the raw A2A events and every HTTP request and response behind each step. Find runs by status, participant or text.

| | |
|---|---|
| ![A run, step by step](docs/screenshots/runs.png) | ![Replaying a run](docs/screenshots/runs-replay.png) |
| A run: the order as numbered steps, with the quotes' actual prices, next to the sequence diagram. | Replay one run step by step (←/→, space); what hasn't happened yet is dimmed. |
| ![The HTTP behind a step](docs/screenshots/runs-http.png) | ![All traffic of a run](docs/screenshots/runs-traffic.png) |
| Every step opens to the HTTP requests and responses behind it. | All traffic: every call in, call to an agent and webhook, linked to its step. |
| ![Approving from the graph](docs/screenshots/live-graph-approval.png) | ![Inbox](docs/screenshots/inbox.png) |
| The buyer asks Ada to approve; she answers on the graph. | The inbox: a thread per contact, with the question as a form. |
| ![Agents](docs/screenshots/agents.png) | ![Directory](docs/screenshots/directory.png) |
| Agents: addresses, tunnels, workers, capacity, tokens. | The directory: people and agents, each with a card. |
| ![Run summaries on the live graph](docs/screenshots/live-summary.png) | ![One event, in place](docs/screenshots/live-event.png) |
| Each run summed up in one line; a click opens its steps in place. | All events: a click shows what the event carried and its run, with that step marked. |
| ![Between agents](docs/screenshots/inbox-between.png) | ![A run's report card](docs/screenshots/runs-report.png) |
| The buyer and Northwind's conversation: quote requests and quotes. | A run's report: what it built and its stats, copied as Markdown with one click. |
| ![Telegram in Settings](docs/screenshots/settings-telegram.png) | |
| Settings → Telegram: your own bot, answering with buttons only. | |
| ![The waiting board](docs/screenshots/waiting.png) | ![Answering an escalation](docs/screenshots/waiting-escalated.png) |
| Waiting: an approval Ada hasn't answered, reminded and escalated to Bob by the team's rules. | Bob answers in Ada's place, right on the board. |
| ![Dark live graph](docs/screenshots/live-graph-dark.png) | ![Dark runs](docs/screenshots/runs-dark.png) |

## Run it

You need Postgres (developed against 16; the compose file uses 17).

```sh
cd web && npm ci && npm run build && cd ..        # the web app, embedded in the binary
go build -o lagrange ./cmd/lagrange

DATABASE_URL=postgres://localhost:5432/lagrange?sslmode=disable \
LAGRANGE_PUBLIC_URL=http://localhost:8080 \
LAGRANGE_ADMIN_EMAIL=you@example.com LAGRANGE_ADMIN_PASSWORD=... LAGRANGE_ADMIN_HANDLE=you \
./lagrange serve                                  # http://localhost:8080
```

| Variable | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | `postgres://localhost:5432/lagrange` | Migrations run on start. |
| `LAGRANGE_ADDR` | `:8080` | Listen address. |
| `LAGRANGE_PUBLIC_URL` | `http://localhost:8080` | How agents and people reach the server. Agent cards and tunnel commands use it. |
| `LAGRANGE_ADMIN_EMAIL`, `_PASSWORD`, `_NAME`, `_HANDLE` | | The first account, created if it doesn't exist. |
| `LAGRANGE_WORKER_ID` | hostname | Names this replica in the work queue's leases. |
| `LAGRANGE_ALLOW_PRIVATE_PUSH` | off | `1` lets A2A push notifications go to private networks (local demos only). |
| `LAGRANGE_DEV` | off | `1` adds a **Reset** menu to the sidebar for admins. **Empty** deletes every agent and all traffic (people and logins stay). Development only. |
| `LAGRANGE_DEV_SEED` | none | With `LAGRANGE_DEV=1`: a seed command (no shell), run from start to stop, e.g. the procurement demo's `platform` binary. **Reset to demo seed** empties the server and runs it again with `LAGRANGE_SEED=1`; a server with no agents seeds at start. |
| `LAGRANGE_SMTP_URL` | off | `smtp://user:pass@host:587` (STARTTLS) or `smtps://...:465`. Turns on email notifications: when a task waits on someone, they get an email with a one-time link that answers it without logging in. |
| `LAGRANGE_SMTP_FROM` | `lagrange@localhost` | The sender address of those emails. |
| `LAGRANGE_SIGNUP` | `invite` | `invite`: people join through an invitation into a workspace, with a role (Directory or Settings → Invite, which emails a one-time link or shows it to copy). `open`: anyone can create an account. New accounts confirm their email when SMTP is set. |
| `LAGRANGE_GITHUB_CLIENT_ID`, `_SECRET` | off | "Continue with GitHub". Create an OAuth app with the callback `<public URL>/api/auth/github/callback`. |
| `LAGRANGE_GOOGLE_CLIENT_ID`, `_SECRET` | off | "Continue with Google". Create an OAuth client with the redirect URI `<public URL>/api/auth/google/callback`. |
| `LAGRANGE_TRUST_PROXY` | off | `1` behind a reverse proxy (Coolify, Traefik, nginx): login rate limits then use the client address from `X-Forwarded-For`. |

### Deploy on Coolify (or any Docker host)

The repo has a [`Dockerfile`](Dockerfile) (web build, Go build, distroless image with a health check) and a [`docker-compose.yml`](docker-compose.yml) with Postgres and Lagrange. Only Lagrange exposes a port.

1. In Coolify, add a resource from this Git repository and choose **Docker Compose**.
2. Set the variables from [`.env.example`](.env.example): `POSTGRES_PASSWORD`, `LAGRANGE_PUBLIC_URL` (your domain, with `https://`), and the `LAGRANGE_ADMIN_*` account.
3. Point your domain at the `lagrange` service, port **8080**. The proxy must pass WebSockets (`/tunnel`) and must not buffer server-sent events (`/api/events`). If the live graph only updates on reload, SSE is being buffered.
4. Deploy, then log in with the admin account.

Without Coolify: `cp .env.example .env`, edit it, then `docker compose up -d`.

## Connect an agent

In the web app, open **Agents → New agent**. You get an address and the tunnel command with its token filled in (shown once); the page turns green when the tunnel connects and lets you send a test message. Get the binary with `go install github.com/three-agent-problem/lagrange/cmd/lagrange@latest`, then, next to your agent:

```sh
# no agent yet? a built-in test agent that repeats what it is sent
lagrange tunnel --server https://lagrange.example.com --token <token> --echo

# any A2A agent (any language) listening locally
lagrange tunnel --server https://lagrange.example.com --token <token> --to http://localhost:9001

# a coding agent that speaks ACP; its permission prompts come to your inbox
lagrange tunnel --server https://lagrange.example.com --token <token> --acp claude-agent-acp --dir ~/code/project

# any script: the message text on stdin, the reply on stdout
lagrange tunnel --server https://lagrange.example.com --token <token> --exec "python summarize.py" --name Summarizer

# any webhook (n8n, Zapier, Make, your own endpoint): each message is POSTed as JSON
lagrange tunnel --server https://lagrange.example.com --token <token> --http https://n8n.example.com/webhook/orders --header "X-Key: …"
```

### Coding agents: `--acp`

By default an `--acp` agent asks before every tool call, and the question lands in the sender's inbox. These flags set it up for unattended work:

```sh
lagrange tunnel --address coder --create --acp claude-agent-acp --dir ~/code/app \
  --model sonnet --allow '*' --deny delete \
  --per-context worktree \
  --mcp docs=https://docs.example.com/mcp
```

| Flag | Does |
|---|---|
| `--model M` | The model of every session (`haiku`, `sonnet` or `opus` for `claude-agent-acp`). |
| `--set id=value` | Any other session setting the agent reports, e.g. `--set mode=plan` (repeatable). |
| `--caller-settings model` | Lets a sender change these settings per message, with `{"acpSettings": {"model": "opus"}}` in the message metadata. Anything else fails the task. |
| `--allow kinds`, `--deny kinds` | Approve or reject tool calls by ACP kind without asking: `read`, `edit`, `delete`, `move`, `search`, `execute`, `think`, `fetch`, `other`, or `*`. `--deny` wins. |
| `--per-context dir\|worktree` | Gives each conversation (A2A context) its own working directory: an empty folder, or a git worktree of `--dir` on the branch `lagrange/<context>`. They live in your cache directory, under `lagrange/contexts/`. |
| `--mcp name=url` | An MCP server for every session: `name=https://…` for HTTP, `name=command args` for stdio, or the ACP `McpServer` JSON (repeatable). |
| `--thoughts` | Streams the agent's thinking into a `thoughts` artifact, next to its reply. |

The agent's card says how it's set up ("It runs tools without asking. Model: sonnet. Each conversation gets its own git worktree and branch."), so a sender can tell before it calls.

### Agents that don't speak A2A: `--exec` and `--http`

Both get the same input and reply the same way, so a script can stand in for a workflow and the other way round.

- **Input.** `--http` POSTs `{"text": "...", "data": {...}, "taskId": "...", "contextId": "...", "answer": false}`. `data` is the message's first JSON part. `--exec` gets the text on stdin and the rest in `LAGRANGE_TASK_ID`, `LAGRANGE_CONTEXT_ID`, `LAGRANGE_DATA` and `LAGRANGE_ANSWER`.
- **Reply.** Plain text completes the task with that text. A JSON object completes it with the object as data, using its `text`, `reply`, `output` or `answer` string as the text. In n8n, *Respond to Webhook* with the AI Agent's `{"output": …}` works as is.
- **Asking back.** `{"ask": "Approve 200 hoodies?", "schema": {JSON Schema}}` puts the task in `INPUT_REQUIRED`. With a schema, the question is a form in the sender's inbox. The answer arrives as a new call with the same `taskId` and `"answer": true`.
- **Failures.** A non-2xx response, a non-zero exit or `--timeout` (default 5m) fails the task with the reason. For a script, the reason is the end of its stderr.
- `--description` sets what the card and the public page say the agent does.

- Run the same command more than once (on one machine or several) for more workers. `--capacity` sets how many tasks each copy runs at once (default 4).
- `--expose` also forwards plain HTTP from `https://<host>/t/<handle>/...` to the agent, for webhooks such as A2A push notifications from other agents.
- An agent with its own public URL can skip the tunnel: give its endpoint when you create the address.
- No web app at hand? `lagrange token --address <handle> --create` mints a token straight from the database.

Or skip copying tokens: log in once, and the tunnel makes its own.

```sh
lagrange login --server https://lagrange.example.com   # opens the browser; password, GitHub or Google
lagrange tunnel --address my-agent --create --echo     # creates @my-agent, mints and saves its token
```

`lagrange login` opens the server's approval page, where you pick the workspace and the role. The browser hands a one-time code back to the CLI on `127.0.0.1`, and the CLI trades it (with a PKCE verifier only it knows) for an API key, saved in your config directory (`lagrange whoami`, `lagrange logout`). Over SSH, `lagrange login --no-browser` prints a link to open anywhere and asks you to paste the code the page shows.

No browser anywhere (a fresh server, CI, a container)? Two ways:

```sh
LAGRANGE_PASSWORD=… lagrange login --server https://lagrange.example.com --email you@example.com   # password accounts
docker compose exec lagrange lagrange token --api-key --email you@example.com                     # on the server: straight from the database
```

`token --api-key` prints a key with your role in your first workspace (`--workspace`, `--role` to pick).

## API keys

Scripts, CI and MCP clients use `/api` with a personal API key: `Authorization: Bearer lgk_…`. Create one in **Settings → API keys**, or get one with `lagrange login`. A key acts as you in one workspace, with at most the role picked when it was made (and never more than your current role there). Keys can't create keys, change passwords or manage workspaces; revoke them in Settings. The stable part of the API is described at `/api/openapi.yaml`.

```sh
curl -H "Authorization: Bearer $LAGRANGE_KEY" https://lagrange.example.com/api/runs?q=PO-123
curl -H "Authorization: Bearer $LAGRANGE_KEY" -H 'Content-Type: application/json' \
  -d '{"to":"acme-buyer","text":"Order 200 hoodies"}' https://lagrange.example.com/api/send
```

`/api/send` also takes `files` (`[{"name": "shot.png", "data": "<base64>"}]` or `[{"url": "https://…"}]`, which become A2A file parts, so a coding agent gets a screenshot as an image), `tags` (recorded with the task, see below), `metadata` (the message's), and `taskId`/`contextId` to continue a task or a conversation. Each run in `/api/runs` carries its root task's `contextId`, so sending into it continues the run's conversation. `lagrange send @x "what's wrong here?" --file shot.png --tag ticket=42` does the same from the shell.

Agents calling other addresses should say who they are, and which task the call is part of, so the graph and the task tree show it. In Go, the [`delegate`](delegate/delegate.go) package does both:

```go
client, _ := a2aclient.NewFromCard(ctx, card,
	a2aclient.WithCallInterceptors(delegate.Interceptor(token))) // Authorization + Lagrange-Task
handler := a2asrv.NewHandler(delegate.Executor(myExecutor))       // puts the incoming task on ctx
```

Other languages send `Authorization: Bearer <token>` and `Lagrange-Task: <id of the task you're working on>` on their calls.

The procurement example in [`three-agent-problem/examples`](https://github.com/three-agent-problem/examples) runs a buyer and two suppliers on a Lagrange server: `go run ./procurement/cmd/platform -server http://localhost:8080 -email ... -password ...`, then message `@acme-buyer` from the inbox.

### What the activity log keeps from agents

Every A2A event becomes an activity entry (`GET /api/events`, the live graph, the Runs page). A few conventions make agents' work visible to every client, not only to the machine that runs them:

- **Tags.** A sender puts `{"https://threeagents.dev/ext/tags/v1": {"subtask": "S3"}}` in a message's metadata, and the entry that creates the task carries them as `data.tags`, so clients can group and label tasks (up to 16 short strings).
- **Tool calls.** A status message with a `{"toolCall": {"id", "title", "kind", "status", "input", "output"}}` data part is recorded with that data, capped at 4 KB each for input and output. Coding agents report a call when it starts (`in_progress`) and when it ends, and the Runs page shows the pair as one step with its duration.
- **Usage.** A status message whose only part is `{"usage": {"used", "size", "cost": {"amount", "currency"}}}` (cost cumulative over the session) becomes a `usage` entry with `data.costDelta`, what the session spent since its previous report. Each run adds those up as `cost`, shown in the runs list.
- **Thoughts.** An agent can stream its thinking into a `thoughts` artifact, next to its `response`.
- **Reports.** An artifact named `report` (ID `report`) with a `{"https://threeagents.dev/ext/report": {"headline", "status", "progress": {"done", "total"}, "built": [{"title", "detail"}], "stats": [{"label", "value"}], "outcome", "notice"}}` data part is the run's situation report; each one replaces the last. It is recorded as `data.report`, every run carries its latest one as `report`, and the Runs page shows it with a "Copy" button (Markdown). Set `outcome` on the final one. A report with a `notice` is forwarded to whoever started the run (Telegram); a final one sends them the finished-run card. In Go: [`report.Report{...}.Event(ec)`](report/report.go).
- **Commits.** An artifact named `commit` with a `{"commit": {"sha", "branch", "subject", "body", "author", "at", "files": [{"path", "insertions", "deletions"}], "insertions", "deletions"}}` data part, plus the patch as a `text/x-diff` text part, is recorded as "commit 3f2a1c9 fix the tests (+4 −1)" with `data.commit`; the patch (up to 256 KB) stays in the task, for `GET /api/tasks/{id}`. The Runs page shows it as "@coder committed …" with its files.

Together these are an agent's transcript: what it was asked, the tools it ran (with their output), what it thought, what it said and what it committed, readable from any client after the fact.

`tunnel --acp` does all of this for coding agents (`--thoughts` adds the thinking; with `--per-context worktree` it publishes each commit the agent makes on its branch, just before the task ends or asks something). For an a2aacp executor of your own, use [`acp.Converter`](acp/acp.go), and [`code`](code/code.go) for commits:

```go
exec, _ := a2aacp.NewExecutor(ctx, proc, &a2aacp.Config{Converter: acp.Converter(nil, acp.Options{Thoughts: true})})

// Publish each commit the agent makes in its context's checkout, before the task ends:
served := code.Publish(exec, func(ec *a2asrv.ExecutorContext) code.Workspace {
	return code.Workspace{Dir: worktreeOf(ec.ContextID), Branch: branchOf(ec.ContextID)}
}, logger)
```

## From the command line and CI

After `lagrange login` (or with `LAGRANGE_SERVER` and `LAGRANGE_KEY` set, as in CI), you can drive the workspace with these commands:

```sh
lagrange send @echo "hello" --wait              # prints the answer
lagrange send @cfo "Deploy v1.4 to prod?"        # prints the task ID
lagrange task <id> --wait                        # waits for it, then prints the answer
lagrange waiting --mine                          # what waits on you
lagrange answer <id> --approve "Ship it."        # or --reject, or --data '{"qty":200}'
lagrange runs -q PO-123                          # search runs; lagrange run <id> shows one
lagrange run <id> --overlap                      # per-agent tool calls and cost, and the tool calls agents repeated
```

`send --wait` waits through questions and approvals addressed to other people, so a deploy job can wait for `@cfo`. The answer goes to stdout and the status to stderr, and `--json` prints the whole result. The exit code tells a script how it went:

| Exit code | Meaning |
|---|---|
| 0 | completed |
| 3 | failed, rejected or canceled |
| 4 | `--timeout` ran out (the task goes on) |
| 5 | the task asks you |

```yaml
# GitHub Actions: deploy only after @cfo approves
- run: lagrange send @cfo "Deploy ${{ github.sha }} to prod?" --wait --timeout 4h
  env: { LAGRANGE_SERVER: https://lagrange.example.com, LAGRANGE_KEY: ${{ secrets.LAGRANGE_KEY }} }
```

## Telegram

Each person can connect their own Telegram bot in **Settings → Telegram** (or from [Halo](https://github.com/three-agent-problem/halo) with `halo connections`). It's one bot per person, made in two minutes with @BotFather, and it long-polls, so the server needs no public URL.

1. In @BotFather, `/newbot`, then paste the token in Settings. The server checks it with Telegram and never shows it again.
2. Tap **Open @your_bot** and **Start**. The link carries a one-time pairing code (10 minutes, five wrong guesses kill it), which binds the bot to your private chat and your Telegram account.

The settings API (`/api/settings/telegram`) also takes an API key, so tools like Halo can connect it; a viewer key can only read it.

From then on, what waits on you arrives there too, next to email and Slack, with reminders and escalations:

| The task asks | In Telegram |
|---|---|
| to approve (an `ask.ApprovalKey` message, or a structured form whose only required field is a boolean) | **Approve** (it asks again: "Yes, approve") or **Reject** |
| multiple-choice questions ([`ask`](ask/ask.go)) | the options as buttons (✓ toggles for multi-select), then **Send answers** |
| anything else (free text, a larger form, an account to connect) | the question, and a pointer to the inbox |

When the task is answered anywhere else, the message says so and its buttons go away. With the level at *milestones* (the default), the bot also sends report notices and a card when a run you started finishes: its report, cost and time. `/status`, `/waiting`, `/runs`, `/mute 2h` and `/unmute` read things back.

What it can't do is the point. The bot listens to one private chat and one Telegram user. It answers only tasks that wait on you, and only with buttons; it never starts, cancels or messages anything, and typed text goes nowhere (it gets a fixed reply). Buttons carry random tokens that name a row on the server, never a task. An answer goes through the same checks as the inbox, and only the press that closes the message counts, so a double tap answers once. Everything the bot can reach is [`telegram.Deps`](telegram/service.go): one answer function and three reads. Messages hold questions, run titles and reports, never diffs or tool output. The token only goes to `api.telegram.org` and is kept out of errors and logs. Like Slack webhooks, it is stored in the database as is.

## Webhooks

**Settings → Webhooks** (admins) sends a workspace's events to your URL, so Zapier, n8n, Make or your own service can react without polling:

| Event | When |
|---|---|
| `task.waiting` | A task stops on someone: a question or an approval (`waitingOn`, `asker`, `question`). |
| `task.answered` | Someone answered a waiting task (`answeredBy`, `answer`). |
| `run.completed` / `run.failed` | A run (a root task and everything it caused) finished, or failed, was rejected or canceled. |
| `address.online` / `address.offline` | An agent's tunnel came up, or its last one dropped. |

Each delivery is a POST of `{id, type, createdAt, workspace, data}`, with these headers:
- `Lagrange-Event`: the event type.
- `Lagrange-Delivery`: the delivery ID.
- `Lagrange-Signature: t=<unix>,v1=<hex>`: an HMAC-SHA256 of `<t>.<body>` made with the webhook's signing secret (`whsec_…`, shown once).

In Go, `webhook.Verify(secret, header, body, 0)` checks the signature and rejects anything older than five minutes. Answer 2xx within 15 s. Other answers are retried with backoff (10 s, 30 s, 90 s, … up to 8 tries, about a day). Settings shows the last 50 deliveries, with their payloads, test events and redelivery. Events are read from the activity log, so none are lost while your endpoint is down, and replicas don't send them twice. Order isn't guaranteed, so use `createdAt`. Webhook URLs on private networks are refused unless `LAGRANGE_ALLOW_PRIVATE_PUSH=1`.

## Use it from Claude Code, Cursor or any MCP client

`lagrange mcp` serves your workspace to an MCP client over stdio, as you (it uses your `lagrange login`, or `LAGRANGE_KEY`):

```sh
lagrange login --server https://lagrange.example.com
claude mcp add lagrange -- lagrange mcp
```

Cursor, Claude Desktop and others take the same command in their MCP config: `{"command": "lagrange", "args": ["mcp"]}`.

| Tool | |
|---|---|
| `list_agents`, `get_agent_card` | Who the workspace can reach, and what they do. |
| `send_task` | Give an agent work and wait for its answer (or for it to ask something). |
| `ask_human` | Ask a person in their inbox (with email/Slack, reminders and escalation) and wait for the answer. If they take longer, it returns the task ID for `get_task`. |
| `get_task` | Check on a task later. |
| `list_waiting`, `answer` | What waits on you, and answering it (approve/reject). |
| `search_runs`, `get_run` | "What happened with PO-123?" |

So "ask @northwind-supplier for a quote and get @ada to approve it" works from your editor, and every step shows up in the runs and the live graph. A viewer key gives a read-only MCP server.

## How it works

```mermaid
flowchart LR
    subgraph server["lagrange serve"]
        A2A["A2A handler (a2a-go, cluster mode)<br/>one tenant per address"] --> R["relay executor"]
        R --> MB["mailbox: tunnel sessions,<br/>free slots, waiting tasks"]
        API["web app + API + SSE"]
    end
    PG[("Postgres: tasks, events, work queue,<br/>addresses, links, activity")]
    server <--> PG
    C["any A2A client"] -- "/a/&lt;handle&gt;" --> A2A
    MB -- "yamux over WebSocket" --> T["lagrange tunnel → your agent"]
```

1. A call to `/a/<handle>` becomes a task in Postgres with the address as its A2A tenant and the caller (token or session) as its sender. The work goes on the work queue.
2. A worker takes it and runs the **relay**. For a person, the task waits as `INPUT_REQUIRED` with `awaiting` set. For an agent, the mailbox picks a tunnel session with a free slot, or the task waits (SUBMITTED, "queued") until one connects or frees up.
3. The relay calls the agent with a normal A2A client over the tunnel, and restamps the agent's events with the platform's task ID. The link between the two task IDs is stored, so answers, cancels and redeliveries reach the same task on the agent.
4. Every step is written to the activity log; `LISTEN/NOTIFY` and server-sent events carry it to the web app, which folds it into the graph with the same reducer that powers replay.
5. Every HTTP exchange is recorded too: calls in at `/a/<handle>`, the relay's calls to agents, and webhooks through `/t/<handle>`. Bodies are kept up to 64 KB, and only a short allowlist of headers (never `Authorization`, cookies or tokens). `GET /api/runs/{id}` returns a run (the task nobody delegated, everything delegated from it, and the messages sent from those) with its entries and exchanges.

### Workspaces

A workspace is a team: its agents and its members. People belong to any number of workspaces with a role (`owner` manages members, `admin` manages agents, invitations and waiting rules, `member` sends, answers and adds agents, `viewer` only looks). Each agent belongs to one workspace; handles stay global, so `/a/<handle>` works across workspaces.

A workspace sees the traffic of its own addresses: a task is visible when its sender or recipient is one of them. A call from one workspace to another shows in both, and each side's internal delegations stay its own (the other side's tasks are counted as `hidden` on the run). API requests name the workspace with the `Lagrange-Workspace: <slug>` header (`?workspace=` for `/api/events`), and default to the caller's oldest one. Open signup gives each new person a workspace of their own. Invitations carry a workspace and a role; someone who already has an account logs in and joins. Data from before workspaces lands in `default`.

### Public address pages

Every address URL is also a page. A browser opening `https://<host>/a/<handle>` gets the address's public page; an A2A client asking for the same URL (no `text/html` in `Accept`) gets the card as JSON, as before, and `/.well-known/agent-card.json` is always JSON. The page shows the name, description, skills with their examples, whether the agent is online, who can reach it, and snippets that send it a message from the `a2a` CLI, curl, Python (`a2a-sdk`) and Go (with `$LAGRANGE_TOKEN` when the address isn't public). It also grades the card as served with the [registry's linter](https://github.com/three-agent-problem/registry). Signed in, the page lets you try the address (or ask a person) from a workspace that may reach it, or ask for access for one of yours. The page's title and description are in the HTML, so shared links preview properly. Data: `GET /api/public/addresses/{handle}` (no session needed; with one, it adds how each of your workspaces stands with the address). Share it from the Agents page or the Directory.

An address's own name and description (set when you create it) win over the ones in the agent's card.

### Who can reach an address

Each address says who may start a task with it (`access`): `public` (anyone, even without a token), `workspace` (agents and people of its workspace) or `connections` (its workspace, plus workspaces whose request to connect it accepted). New agents take `connections`, new people `workspace`; addresses from before stay `public`. Agents' managers change it on the Agents page, and people change their own in Settings.

Another workspace asks in Settings → Connections (`POST /api/connections {to, note}`); the agent's workspace admins, or the person asked, accept, decline or block (`PUT /api/connections/{id} {status, ratePerMin}`), and get an email when SMTP is set. An accepted connection lets the asking workspace's agents (with their own token) and people start tasks, up to `ratePerMin` new tasks a minute (default 60, per replica). Refusals are A2A errors: `UNAUTHENTICATED` (401) without credentials, `UNAUTHORIZED` (403) otherwise, with a message that says what to do. Follow-up calls on a task stay with its sender. The card of a non-public address declares a bearer `securitySchemes` entry.

| Package | What it does |
|---|---|
| [`cmd/lagrange`](cmd/lagrange/main.go) | `serve`, `tunnel`, `token`, `migrate`, `healthcheck`. |
| [`platform`](platform/) | Wires it together: routes, auth (sessions and address tokens), the API, the web app. |
| [`relay`](relay/) | The executor behind every address: forwards to agents, parks tasks for people, records links and activity. |
| [`directory`](directory/) | Addresses (agents and people) and their access, tokens, users, sessions, workspaces and members, connections, stored cards. |
| [`mailbox`](mailbox/) | Tunnel sessions per address, capacity, waiting deliveries. |
| [`tunnel`](tunnel/) | The tunnel server and client (WebSocket + yamux, reconnects with backoff). |
| [`activity`](activity/) | The activity log and its live stream, and the recorder of every HTTP exchange. |
| [`pg`](pg/) | Postgres: migrations, `taskstore.Store`, push config store, the work queue and event queue for a2a-go cluster mode. |
| [`delegate`](delegate/delegate.go) | For agents: send your token and the current task with outgoing calls. |
| [`web`](web/) | The React app (Vite, React Flow, Tailwind), embedded with `go:embed`. |

## Toolkit (for agents you host yourself)

These packages came first and work without the platform. The `console` is the old single-process web console; the platform's web app replaces it.

| Package | What it does |
|---|---|
| [`observer`](observer/observer.go) | A `push.ConfigStore` decorator. Every task an a2a-go agent runs also pushes its events to an operator endpoint, and the executor doesn't change. |
| [`console`](console/) | *Legacy.* An in-memory web console with a task tree, timelines and an inbox. |
| [`credentials`](credentials/credentials.go) | A `Broker` interface for `AUTH_REQUIRED`: look up a user's connection, start a connect flow, wait for it, then call the provider API through the broker. |
| [`credentials/nango`](credentials/nango/) | A broker built on [Nango](https://nango.dev): Connect sessions, connection lookup by end-user tag, the proxy, and signed auth webhooks. |
| [`credentials/demo`](credentials/demo/) | An in-memory broker with a fake consent page and a fake calendar, for demos and tests. |
| [`links`](links/links.go) | A metadata convention that records "this task delegated to that task on that agent". |
| [`cmd/console`](cmd/console/main.go) | The legacy console as a standalone server. |

### Watch an agent you run yourself

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

### The console inbox

A task shows up in the inbox when it is:

- **`INPUT_REQUIRED`** with a [structured input](https://github.com/a2aproject/a2a-go/tree/main/a2aext/structuredinput) request. The JSON Schema is rendered as a form: a lone boolean becomes Approve/Reject buttons, enums become selects, and numbers and strings become inputs. Submitting the form sends `structuredinput.NewResponse(...)` back to the agent with `returnImmediately`, and records who answered in the message metadata under `console.AnsweredByKey`.
- **`AUTH_REQUIRED`** with a credential challenge (`credentials.NewChallengeParts`). The inbox shows a *Connect* button that opens the broker's link.

With `-slack-webhook`, a Slack incoming webhook gets a link to the inbox whenever a task starts waiting on a person.

> The console has **no login yet**. Anyone who can reach it can answer, and "answered by" is whatever they type. Keep it on localhost or behind an authenticating proxy.

### AUTH_REQUIRED with a broker

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

#### Nango setup

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
| `https://threeagents.dev/ext/answered-by/v1` | message metadata | who answered from the inbox (the logged-in person on the platform; self-reported in the legacy console) |
| `https://threeagents.dev/ext/awaiting/v1` | status and status-message metadata | the handle of the person a waiting task waits for |
| `https://threeagents.dev/ext/questions` | message metadata | multiple-choice questions for a person ([`ask`](ask/ask.go)); the answer is a text message, one `Header: picks` line per question |
| `https://threeagents.dev/ext/approval` | message metadata | `true`: the message asks to approve or reject (answer with the decision `approve` or `reject`) |
| `https://threeagents.dev/ext/report` | data part key, in a `report` artifact | a run's situation report ([`report`](report/report.go)) |

And two HTTP headers (gRPC metadata) that agents on the platform send with their calls:

| Header | Meaning |
|---|---|
| `Authorization: Bearer <address token>` | who is calling: the edge in the graph starts at this address |
| `Lagrange-Task: <platform task id>` | the task this call is part of, so the new task nests under it in the task tree. The platform only accepts it when that task was addressed to the caller. |

## Caveats

- `observer.Store` adds its subscriber (ID `operator-observer`) to every task's push config list. Clients that list a task's push configs will see it.
- `HTTPSenderConfig.AllowPrivateNetworks` turns off a2a-go's SSRF guard for pushes (spec §13). Use it only when the console is on a private network you control.
- Pushes are fire-and-forget in a2a-go. If the console is down, it misses events.
- On the platform, a task whose agent disconnects mid-task waits in the mailbox and is redelivered (followed if the agent still has it, sent again if not), at most 5 times per execution. Agents should treat a repeated message ID as the same request.
- a2a-go's cluster-mode cancel can report a waiting task as not cancelable while the cancel is still running; the platform waits for the cancel to settle instead.
- Recorded HTTP exchanges are kept forever for now (no pruning yet), up to 64 KB per body. They hold message contents, so treat the database as sensitive.
- A refused new task is recorded as an HTTP exchange (the 401/403), but it creates no task, so it doesn't show in Runs. A rate-limited call is a 403 `UNAUTHORIZED` too, because A2A has no rate-limit error; its message says so.
- A run groups calls by the `Lagrange-Task` header. An agent that doesn't send it on its own calls still gets its tasks recorded, but as separate runs.

## Roadmap

1. Coding agents for real: run `lagrange tunnel --acp claude-agent-acp` end to end, and a Claude-powered demo agent.
2. Binary releases (Homebrew, a GHCR image) and a Slack app with buttons.
3. OpenTelemetry with `traceparent` across hops.
4. Several Lagrange replicas behind one load balancer (cluster mode and `LISTEN/NOTIFY` are built for it; this release tests one replica).
5. A remote MCP endpoint for claude.ai and ChatGPT connectors (OAuth); `lagrange tunnel --mcp` to put an MCP server behind an address; pruning of recorded exchanges.

## Development

```sh
gofmt -l . && go vet ./... && go test -race ./...
cd web && npm ci && npm run typecheck && npm run lint && npm test && npm run build
```

The Postgres tests start a throwaway server with the `postgres` and `initdb` binaries on `PATH` (or in `LAGRANGE_PG_BIN`), or use `LAGRANGE_TEST_DATABASE_URL`. Without either they skip.

For the web app, run `lagrange serve` on :8090 and `npm run dev` in `web/`: Vite proxies the API to it (`LAGRANGE_DEV_SERVER` overrides the address). `go build` embeds whatever is in `web/dist`, so run `npm run build` first for a binary with the app in it.

To develop against a local a2a-go checkout, use a `go.work` file (one is generated by the workspace bootstrap script). This module currently requires a2a-go at commit `2692308` (after v2.7.0), because `a2aext/structuredinput` isn't in a tagged release yet.

## License

Apache-2.0
