// Package delegate is for Go agents on Lagrange that call other addresses. Calls made
// with Interceptor carry the agent's token, so Lagrange knows who is calling, and the
// platform task the agent is working on, so the call shows up nested under that task in
// the task tree and the live graph.
//
//	exec = delegate.Executor(exec) // remember the platform task of each execution
//	client, _ := a2aclient.NewFromCard(ctx, card, a2aclient.WithCallInterceptors(delegate.Interceptor(token)))
//	client.SendMessage(ctx, req) // ctx from Execute
package delegate

import (
	"context"
	"iter"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// HeaderTask carries the platform task an agent is working on: the relay sets it on
// requests to agents, and Interceptor sends it back on the agent's own calls.
const HeaderTask = "Lagrange-Task"

type taskKey struct{}

// Executor wraps exec so that the platform task of each execution (the Lagrange-Task
// header the relay sends) is in the context Execute gets.
func Executor(exec a2asrv.AgentExecutor) a2asrv.AgentExecutor {
	return executor{exec}
}

type executor struct{ a2asrv.AgentExecutor }

func (e executor) Execute(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	if values, ok := ec.ServiceParams.Get(HeaderTask); ok && len(values) > 0 && values[0] != "" {
		ctx = context.WithValue(ctx, taskKey{}, values[0])
	}
	return e.AgentExecutor.Execute(ctx, ec)
}

// Task returns the platform task ctx belongs to, if any.
func Task(ctx context.Context) string {
	s, _ := ctx.Value(taskKey{}).(string)
	return s
}

// Interceptor authenticates calls with token (empty: anonymous) and passes on the
// platform task from ctx.
func Interceptor(token string) a2aclient.CallInterceptor {
	return interceptor{token: token}
}

type interceptor struct {
	a2aclient.PassthroughInterceptor
	token string
}

func (i interceptor) Before(ctx context.Context, req *a2aclient.Request) (context.Context, any, error) {
	if i.token != "" {
		req.ServiceParams["Authorization"] = []string{"Bearer " + i.token}
	}
	if t := Task(ctx); t != "" {
		req.ServiceParams[HeaderTask] = []string{t}
	}
	return ctx, nil, nil
}
