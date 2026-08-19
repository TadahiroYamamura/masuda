// Package egressproxy implements a transparent, SNI-based TLS egress
// filter for masuda's sandbox VMs (Issue #11). It never terminates TLS: it
// only reads far enough into each connection's ClientHello to learn the
// destination hostname (the server_name extension, RFC 6066 §3), checks
// that hostname against an allowlist, and either forwards the connection
// byte-for-byte to that hostname's real address or closes it. Because the
// TLS session itself is never decrypted, the proxy needs no certificate
// and never sees anything past the ClientHello in plaintext -- it can
// filter *which* server a connection reaches without being able to read
// what's said to it.
package egressproxy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	tlsRecordTypeHandshake      = 22
	tlsHandshakeTypeClientHello = 1
	tlsExtensionServerName      = 0
	tlsServerNameTypeHostName   = 0

	// maxRecordLength is TLS's own ceiling on one record's fragment (RFC
	// 8446 §5.1): 2^14 bytes. A ClientHello legitimately spanning more
	// than one record is rare in practice (real SNI proxies like nginx's
	// stream ssl_preread and sniproxy make the same simplifying
	// assumption), so ExtractSNI treats that case as an error rather than
	// reassembling multiple records.
	maxRecordLength = 16384
)

// ErrNotTLS means the connection didn't start with a well-formed TLS
// ClientHello -- not necessarily malicious, just not something this proxy
// (which only ever forwards TLS traffic) can classify.
var ErrNotTLS = errors.New("egressproxy: not a well-formed TLS ClientHello")

// ErrNoSNI means the ClientHello parsed fine but declared no server_name
// extension, so there's no hostname to check against an allowlist.
var ErrNoSNI = errors.New("egressproxy: ClientHello has no server_name extension")

// ExtractSNI reads exactly one TLS record containing a ClientHello
// handshake message from r and returns the SNI hostname it declares. raw
// holds every byte ExtractSNI consumed from r, verbatim, regardless of
// whether it returns an error -- r is a one-shot io.Reader (typically a
// net.Conn, which can't be rewound), so a caller that goes on to forward
// the connection to its real destination must replay raw first, before
// copying any further bytes.
func ExtractSNI(r io.Reader) (hostname string, raw []byte, err error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return "", header, fmt.Errorf("%w: reading record header: %v", ErrNotTLS, err)
	}
	if header[0] != tlsRecordTypeHandshake {
		return "", header, ErrNotTLS
	}
	length := int(binary.BigEndian.Uint16(header[3:5]))
	if length <= 0 || length > maxRecordLength {
		return "", header, fmt.Errorf("%w: implausible record length %d", ErrNotTLS, length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return "", append(header, body...), fmt.Errorf("%w: reading record body: %v", ErrNotTLS, err)
	}
	raw = append(header, body...)

	hostname, err = parseClientHelloSNI(body)
	return hostname, raw, err
}

func parseClientHelloSNI(body []byte) (string, error) {
	if len(body) < 4 || body[0] != tlsHandshakeTypeClientHello {
		return "", ErrNotTLS
	}
	msgLen := int(body[1])<<16 | int(body[2])<<8 | int(body[3])
	if 4+msgLen > len(body) {
		return "", fmt.Errorf("%w: handshake message length exceeds record", ErrNotTLS)
	}
	p := body[4 : 4+msgLen]

	// legacy_version(2) + random(32)
	if len(p) < 34 {
		return "", ErrNotTLS
	}
	p = p[34:]

	var err error
	p, err = skipVector(p, 1) // legacy_session_id<0..32>
	if err != nil {
		return "", err
	}
	p, err = skipVector(p, 2) // cipher_suites<2..2^16-2>
	if err != nil {
		return "", err
	}
	p, err = skipVector(p, 1) // legacy_compression_methods<1..2^8-1>
	if err != nil {
		return "", err
	}

	if len(p) < 2 {
		return "", ErrNoSNI // no extensions block at all
	}
	extLen := int(binary.BigEndian.Uint16(p[:2]))
	p = p[2:]
	if extLen > len(p) {
		return "", fmt.Errorf("%w: extensions length exceeds message", ErrNotTLS)
	}
	extensions := p[:extLen]

	for len(extensions) >= 4 {
		extType := binary.BigEndian.Uint16(extensions[:2])
		extDataLen := int(binary.BigEndian.Uint16(extensions[2:4]))
		extensions = extensions[4:]
		if extDataLen > len(extensions) {
			return "", fmt.Errorf("%w: extension data length exceeds remaining extensions", ErrNotTLS)
		}
		extData := extensions[:extDataLen]
		extensions = extensions[extDataLen:]

		if extType == tlsExtensionServerName {
			return parseServerNameExtension(extData)
		}
	}
	return "", ErrNoSNI
}

func parseServerNameExtension(data []byte) (string, error) {
	if len(data) < 2 {
		return "", fmt.Errorf("%w: server_name extension too short", ErrNotTLS)
	}
	listLen := int(binary.BigEndian.Uint16(data[:2]))
	data = data[2:]
	if listLen > len(data) {
		return "", fmt.Errorf("%w: server_name list length exceeds extension", ErrNotTLS)
	}
	data = data[:listLen]

	for len(data) >= 3 {
		nameType := data[0]
		nameLen := int(binary.BigEndian.Uint16(data[1:3]))
		data = data[3:]
		if nameLen > len(data) {
			return "", fmt.Errorf("%w: server name length exceeds list", ErrNotTLS)
		}
		name := data[:nameLen]
		data = data[nameLen:]
		if nameType == tlsServerNameTypeHostName {
			return string(name), nil
		}
	}
	return "", ErrNoSNI
}

// skipVector consumes one TLS-style <0..N> vector -- lenBytes bytes of
// big-endian length prefix followed by that many bytes of content -- and
// returns whatever remains of p afterward.
func skipVector(p []byte, lenBytes int) ([]byte, error) {
	if len(p) < lenBytes {
		return nil, fmt.Errorf("%w: truncated length prefix", ErrNotTLS)
	}
	n := 0
	for i := range lenBytes {
		n = n<<8 | int(p[i])
	}
	p = p[lenBytes:]
	if n > len(p) {
		return nil, fmt.Errorf("%w: vector length exceeds remaining data", ErrNotTLS)
	}
	return p[n:], nil
}
