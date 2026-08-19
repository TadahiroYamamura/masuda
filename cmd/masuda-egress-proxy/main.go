// Command masuda-egress-proxy runs masuda's shared, host-wide SNI-based
// TLS egress filter (Issue #11): one process for every VM on the host,
// not one per workspace (see internal/egressproxy.Proxy's doc comment for
// why).
//
// Connections arrive via an iptables REDIRECT rule (scripts/setup-vm-host.sh),
// which rewrites the destination to this process's own listening address
// before the kernel's normal routing decision ever runs -- so the
// listener is an ordinary bound socket, no special capability required.
// An earlier revision used TPROXY (needing IP_TRANSPARENT/CAP_NET_ADMIN,
// hence a separate setcap'd binary), but the fwmark + policy-routing trick
// TPROXY depends on to make the kernel treat a non-local-address packet as
// local turned out not to work on this project's WSL2 development host --
// confirmed reproducible independent of the VM (a plain veth pinned to the
// bridge reproduced it), with no interfering nftables/rp_filter/FORWARD
// rule found. REDIRECT sidesteps that entirely by rewriting the
// destination directly instead of relying on routing-table trickery.
//
// Still its own small binary rather than a masuda subcommand, mainly to
// keep matching cmd/masuda-net-helper's shape (one thing running
// independently of masuda's own process lifecycle); not required for
// capability isolation anymore.
//
// Not part of the public CLI surface -- masuda's Go code invokes it (see
// internal/sandbox.EnsureEgressProxy), a human isn't meant to type it
// directly.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/TadahiroYamamura/masuda/internal/egressproxy"
	"github.com/TadahiroYamamura/masuda/internal/sandbox"
)

func main() {
	bind := flag.String("bind", "127.0.0.1", "address to listen on")
	port := flag.Int("port", 0, "TCP port to listen on (required, non-zero)")
	destPort := flag.String("dest-port", "443", "port to dial on the resolved destination")
	flag.Parse()

	if *port == 0 {
		fmt.Fprintln(os.Stderr, "masuda-egress-proxy: --port is required")
		os.Exit(2)
	}

	if err := run(*bind, *port, *destPort); err != nil {
		fmt.Fprintln(os.Stderr, "masuda-egress-proxy:", err)
		os.Exit(1)
	}
}

// run listens and serves until a signal closes the listener. Which
// workspace a given connection belongs to -- and therefore which
// allowlist applies -- is resolved per connection from the client's IP
// (sandbox.NewEgressAllowlistFunc), not from anything passed on this
// command line: an arbitrary number of workspaces can be running at
// once, and this process has no per-workspace state of its own to
// configure at startup.
func run(bind string, port int, destPort string) error {
	ln, err := net.Listen("tcp", bind+":"+strconv.Itoa(port))
	if err != nil {
		return fmt.Errorf("listening: %w", err)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		ln.Close()
	}()

	p := &egressproxy.Proxy{
		Allowed:  sandbox.NewEgressAllowlistFunc(),
		DestPort: destPort,
		Logf:     func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	}
	err = p.Serve(ln)
	if isUseOfClosedConn(err) {
		return nil // ln.Close() from the signal handler above, not a real failure
	}
	return err
}

func isUseOfClosedConn(err error) bool {
	return err != nil && strings.Contains(err.Error(), "use of closed network connection")
}
