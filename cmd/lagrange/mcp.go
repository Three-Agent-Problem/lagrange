package main

import (
	"context"
	"flag"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/three-agent-problem/lagrange/mcpserver"
)

// runMCP serves the logged-in workspace to an MCP client over stdin/stdout:
//
//	claude mcp add lagrange -- lagrange mcp
func runMCP(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
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
	return mcpserver.New(c, versionString(), mcpserver.Options{}).Run(ctx, &mcp.StdioTransport{})
}
