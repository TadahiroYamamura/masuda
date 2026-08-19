package egressproxy

import (
	"bytes"
	"crypto/tls"
	"net"
	"testing"
)

// pipeClientHello runs a real crypto/tls client handshake attempt over an
// in-memory net.Pipe and returns the server-side connection, so the
// ClientHello ExtractSNI reads is a genuine one produced by Go's own TLS
// stack, not a hand-crafted byte string. The handshake itself is expected
// to fail (the "server" side never speaks TLS back) -- only the
// ClientHello it sends matters here.
func pipeClientHello(t *testing.T, serverName string) net.Conn {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { clientConn.Close() })
	t.Cleanup(func() { serverConn.Close() })

	go func() {
		tlsConn := tls.Client(clientConn, &tls.Config{ServerName: serverName, InsecureSkipVerify: true})
		_ = tlsConn.Handshake() // expected to fail/hang past the ClientHello; ignored
	}()
	return serverConn
}

func TestExtractSNI(t *testing.T) {
	conn := pipeClientHello(t, "example.com")
	hostname, raw, err := ExtractSNI(conn)
	if err != nil {
		t.Fatalf("ExtractSNI() error = %v", err)
	}
	if hostname != "example.com" {
		t.Errorf("hostname = %q, want %q", hostname, "example.com")
	}
	if len(raw) == 0 {
		t.Error("raw is empty")
	}
	// raw must actually be a handshake record, so a caller replaying it to
	// the real destination sends something that server will accept.
	if raw[0] != tlsRecordTypeHandshake {
		t.Errorf("raw[0] = %#x, want handshake record type %#x", raw[0], tlsRecordTypeHandshake)
	}
}

func TestExtractSNINoServerName(t *testing.T) {
	conn := pipeClientHello(t, "") // empty ServerName -> Go's tls.Client omits the SNI extension
	_, _, err := ExtractSNI(conn)
	if err == nil {
		t.Fatal("ExtractSNI() error = nil, want ErrNoSNI")
	}
}

func TestExtractSNINotTLS(t *testing.T) {
	conn := bytes.NewReader([]byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"))
	_, _, err := ExtractSNI(conn)
	if err == nil {
		t.Fatal("ExtractSNI() error = nil, want ErrNotTLS")
	}
}

func TestExtractSNITruncated(t *testing.T) {
	// A handshake record header claiming more body than actually follows.
	conn := bytes.NewReader([]byte{tlsRecordTypeHandshake, 0x03, 0x01, 0x01, 0x00, 0x01, 0x02})
	_, _, err := ExtractSNI(conn)
	if err == nil {
		t.Fatal("ExtractSNI() error = nil, want an error for truncated input")
	}
}

func TestExtractSNIEmpty(t *testing.T) {
	conn := bytes.NewReader(nil)
	_, _, err := ExtractSNI(conn)
	if err == nil {
		t.Fatal("ExtractSNI() error = nil, want an error for empty input")
	}
}
