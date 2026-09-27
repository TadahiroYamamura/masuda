package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

// guestPlaceholderToken is the only credential a sandbox guest ever holds:
// runtime/entrypoint.sh and runtime/start_claude.sh export it as
// CLAUDE_CODE_OAUTH_TOKEN, and the API gateway swaps it for the real token
// on the way out. Must match those two scripts. It is not a secret -- its
// whole point is that reading it gets an agent nothing.
const guestPlaceholderToken = "masuda-sandbox-placeholder-token"

const anthropicAPIUpstream = "https://api.anthropic.com"

// newInternalAPIGatewayCommand builds `masuda internal api-gateway`, which
// runs on the host for one workspace's VM, next to its mcp-relay. The guest's
// Claude Code reaches the Anthropic API only through it (ANTHROPIC_BASE_URL),
// carrying a placeholder token; this process puts the real `claude
// setup-token` token in its place and forwards over TLS. The real token
// never enters the guest, so nothing running there can read or exfiltrate it.
//
// A plain HTTP hop the guest is pointed at, rather than intercepting the
// guest's TLS to api.anthropic.com: the latter needs a CA installed in the
// guest and TLS termination in the egress proxy, which otherwise never looks
// past the SNI. Setting only ANTHROPIC_BASE_URL keeps the session on the
// subscription -- a gateway credential or apiKeyHelper would switch it to
// per-token API billing (code.claude.com/docs/en/llm-gateway, "Subscriptions
// and gateways") -- and the OAuth capability Claude Code sends in
// anthropic-beta is forwarded untouched, which is what that page requires.
func newInternalAPIGatewayCommand() *cobra.Command {
	var bind, tokenFile, allowMAC, leaseFile string
	var port int
	cmd := &cobra.Command{
		Use:    "api-gateway",
		Hidden: true,
		Short:  "Forward a sandbox guest's Anthropic API calls, replacing its placeholder token with the real one",
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if tokenFile == "" || allowMAC == "" || leaseFile == "" {
				return fmt.Errorf("--token-file, --allow-mac and --lease-file are required")
			}
			upstream, err := url.Parse(anthropicAPIUpstream)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			allow := func(remote net.Addr) bool { return leaseMatches(remote, allowMAC, leaseFile) }
			return runAPIGateway(ctx, net.JoinHostPort(bind, strconv.Itoa(port)), newAPIGatewayHandler(tokenFile, upstream), allow)
		},
	}
	cmd.Flags().StringVar(&bind, "bind", "127.0.0.1", "address to listen on")
	cmd.Flags().IntVar(&port, "port", 0, "TCP port to listen on (required, non-zero)")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "file holding the real OAuth token, re-read on every request")
	cmd.Flags().StringVar(&allowMAC, "allow-mac", "", "serve only the client whose DHCP lease has this MAC")
	cmd.Flags().StringVar(&leaseFile, "lease-file", "", "dnsmasq lease file --allow-mac is checked against")
	return cmd
}

// newAPIGatewayHandler forwards to upstream, replacing the placeholder bearer
// token with the one in tokenFile. Anything carrying some other credential,
// or none, is refused instead of forwarded: the gateway is not a way for the
// guest to reach the API with a credential of its own choosing.
//
// The token file is read per request so a token registered or rotated with
// `masuda claude set-token` takes effect without restarting the VM.
func newAPIGatewayHandler(tokenFile string, upstream *url.URL) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
		},
	}
	proxy.ErrorLog = log.New(os.Stderr, "api-gateway: ", log.LstdFlags)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+guestPlaceholderToken {
			http.Error(w, "masuda api-gateway: only the sandbox placeholder credential is accepted", http.StatusUnauthorized)
			return
		}
		raw, err := os.ReadFile(tokenFile)
		token := strings.TrimSpace(string(raw))
		if err != nil || token == "" {
			http.Error(w, "masuda api-gateway: no Claude token registered on the host -- run `masuda claude set-token`", http.StatusServiceUnavailable)
			return
		}
		r.Header.Set("Authorization", "Bearer "+token)
		proxy.ServeHTTP(w, r)
	})
}

// runAPIGateway serves handler on addr, closing connections allow rejects
// before a single byte is read from them. Blocks until ctx is cancelled.
func runAPIGateway(ctx context.Context, addr string, handler http.Handler, allow func(net.Addr) bool) error {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: handler}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	err = srv.Serve(&filteredListener{Listener: l, allow: allow})
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// filteredListener drops connections allow rejects at Accept, so the HTTP
// server never sees them.
type filteredListener struct {
	net.Listener
	allow func(net.Addr) bool
}

func (f *filteredListener) Accept() (net.Conn, error) {
	for {
		c, err := f.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if f.allow == nil || f.allow(c.RemoteAddr()) {
			return c, nil
		}
		c.Close()
	}
}
