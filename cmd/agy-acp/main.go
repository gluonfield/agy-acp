package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gluonfield/acp-transport/jsonrpc"
	"github.com/gluonfield/acp-transport/stdio"
	"github.com/gluonfield/agy-acp/internal/server"
	agy "github.com/gluonfield/agy-go"
)

func main() {
	var authMode string
	var agyBin string
	var model string
	var timeout time.Duration
	var allowAll bool
	flag.StringVar(&authMode, "auth", "auto", "auto or oauth")
	flag.StringVar(&agyBin, "agy", "agy", "Antigravity CLI executable")
	flag.StringVar(&model, "model", "", "default model")
	flag.DurationVar(&timeout, "timeout", 5*time.Minute, "turn timeout")
	flag.BoolVar(&allowAll, "dangerously-skip-permissions", false, "let the selected Antigravity backend run with broad workspace permissions")
	flag.Parse()

	store, err := agy.DefaultStore()
	if err != nil {
		exit(err)
	}
	agent, backend, err := selectAgent(context.Background(), authMode, agyBin, store)
	if err != nil {
		exit(err)
	}
	srv := server.New(agent, server.Options{
		Backend:                    backend,
		DefaultModel:               model,
		DefaultTimeout:             timeout,
		DangerouslySkipPermissions: allowAll,
	})
	conn := stdio.New(os.Stdin, os.Stdout)
	peer := jsonrpc.NewPeer(conn, srv)
	srv.SetPeer(peer)
	if err := peer.Serve(context.Background()); err != nil && err != jsonrpc.ErrClosed {
		exit(err)
	}
}

func selectAgent(ctx context.Context, mode, agyBin string, store *agy.Store) (agy.Agent, string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "auto"
	}
	cli := agy.NewCLIClient(agyBin, store)
	switch mode {
	case "oauth":
		return cli, "oauth", nil
	case "auto":
		if status, err := cli.AuthStatus(ctx); err == nil && status.Authenticated {
			return cli, "oauth", nil
		}
		return cli, "oauth", nil
	default:
		return nil, "", fmt.Errorf("unknown auth mode %q", mode)
	}
}

func exit(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
