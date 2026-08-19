package egressproxy

import (
	"io"
	"net"
	"time"
)

// AllowlistFunc reports whether clientIP may reach hostname. clientIP is
// the connecting VM's own address (each workspace's VM gets a distinct
// DHCP-leased IP), which is how a single shared Proxy process resolves
// "which workspace is this" and applies that workspace's own allowlist --
// see internal/sandbox's resolver for how clientIP maps back to a
// workspace. Matching semantics for hostname (exact match, wildcard,
// case-folding, ...) are entirely up to the implementation the caller
// supplies -- this package has no opinion.
type AllowlistFunc func(clientIP net.IP, hostname string) bool

// Proxy is a shared, host-wide SNI-based egress filter (Issue #11): it
// accepts TCP connections (arriving via an iptables REDIRECT rule, so
// every VM's outbound 443 traffic reaches it regardless of which
// workspace it's from), extracts the destination hostname from each
// connection's TLS ClientHello (see ExtractSNI), and either forwards the
// connection verbatim to that hostname's real address or closes it.
//
// One process for the whole host, not one per workspace: an earlier design
// ran one proxy per workspace with per-workspace DNAT rules, but that
// requires netfilter rules to be added/removed dynamically in lockstep
// with VM lifecycle -- doable, but only via a low-level nftables netlink
// library (exec'ing `iptables` doesn't work: confirmed live that a
// setcap'd CAP_NET_ADMIN binary's capability doesn't propagate to an
// exec'd child, the same limitation that ruled out shelling out to `ip` for
// TAP management). That code would only be exercisable end-to-end against
// live kernel netfilter state, with no way to verify it outside a
// privileged real-machine test -- unacceptably untestable for something
// that ships. A single static iptables rule, set up once by
// scripts/setup-vm-host.sh, resolves "which workspace" per-connection
// here instead of per-rule at the network layer.
//
// Deliberately default-deny, with no fallback path: any connection that
// isn't a well-formed TLS ClientHello with an SNI extension, or whose
// hostname Allowed rejects, is closed rather than forwarded. There is no
// mode that lets non-TLS or SNI-less traffic through unfiltered.
//
// The real destination is resolved from the SNI hostname itself, not from
// whatever address the client originally dialed -- this is what keeps a
// client from "lying" about its destination: the hostname in the
// ClientHello is the only thing that determines where the connection
// actually goes.
type Proxy struct {
	// Allowed decides whether a given client may reach a given SNI
	// hostname. Called once per connection, must be safe for concurrent
	// use.
	Allowed AllowlistFunc
	// DestPort is the port dialed on the resolved destination, e.g.
	// "443". Defaults to "443" if empty.
	DestPort string
	// DialTimeout bounds connecting to the real destination. Defaults to
	// 10s if zero.
	DialTimeout time.Duration
	// Logf, if set, receives one line per accepted/denied/errored
	// connection. Optional -- nil means silent.
	Logf func(format string, args ...any)
}

func (p *Proxy) log(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
	}
}

func (p *Proxy) destPort() string {
	if p.DestPort == "" {
		return "443"
	}
	return p.DestPort
}

func (p *Proxy) dialTimeout() time.Duration {
	if p.DialTimeout == 0 {
		return 10 * time.Second
	}
	return p.DialTimeout
}

// Serve accepts connections on ln until Accept returns an error (e.g. ln
// was closed), handling each in its own goroutine. Serve itself blocks
// until that happens, then returns the error.
func (p *Proxy) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go p.handle(conn)
	}
}

func (p *Proxy) handle(client net.Conn) {
	defer client.Close()

	hostname, raw, err := ExtractSNI(client)
	if err != nil {
		p.log("egressproxy: closing %s: %v", client.RemoteAddr(), err)
		return
	}

	clientIP := remoteIP(client)
	if p.Allowed == nil || clientIP == nil || !p.Allowed(clientIP, hostname) {
		p.log("egressproxy: denying %s -> %q (not allowed)", client.RemoteAddr(), hostname)
		return
	}

	dest, err := net.DialTimeout("tcp", net.JoinHostPort(hostname, p.destPort()), p.dialTimeout())
	if err != nil {
		p.log("egressproxy: dialing %q for %s: %v", hostname, client.RemoteAddr(), err)
		return
	}
	defer dest.Close()

	// The ClientHello bytes ExtractSNI already consumed from client were
	// never forwarded -- replay them first so the real destination sees
	// the exact same handshake the client sent.
	if _, err := dest.Write(raw); err != nil {
		p.log("egressproxy: replaying ClientHello to %q: %v", hostname, err)
		return
	}

	p.log("egressproxy: allowing %s -> %s", client.RemoteAddr(), hostname)
	relay(client, dest)
}

// remoteIP extracts conn's remote address as a net.IP, or nil if it
// somehow isn't a *net.TCPAddr (Proxy is only ever used over TCP, so this
// shouldn't happen in practice; treated as "deny" via the nil check in
// handle rather than panicking).
func remoteIP(conn net.Conn) net.IP {
	tcpAddr, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return nil
	}
	return tcpAddr.IP
}

// relay copies bytes bidirectionally between a and b until one side
// closes or errors, then closes both so the other direction's io.Copy
// unblocks too, and waits for both goroutines to finish before returning.
func relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(a, b)
		a.Close()
		done <- struct{}{}
	}()
	go func() {
		io.Copy(b, a)
		b.Close()
		done <- struct{}{}
	}()
	<-done
	<-done
}
