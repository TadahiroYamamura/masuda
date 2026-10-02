package serve

import (
	"net"
	"net/http"
	"strings"
)

// corsExposed はブラウザのConnect（gRPC-Webを含む）クライアントが読む応答ヘッダー。
// 既定ではCORSの応答から隠されるので、明示して見せる。
var corsExposed = strings.Join([]string{
	"Grpc-Status", "Grpc-Message", "Grpc-Status-Details-Bin",
	"Connect-Content-Encoding", "Connect-Accept-Encoding", "Content-Encoding",
}, ", ")

// loopbackHandler はループバックの待ち受け（config.jsonのlisten）用にhを包む。
//
// CORSは任意のオリジンを許す。公開APIは認証を持たず、同じホストで動くものを信頼する前提
// （docs/design/contracts.md「通信の前提」）で、GUIをどのオリジンから配っても使えるようにするため。
// 代わりにHostヘッダーがループバックの名前でない要求は断る。外部のサイトがDNSの名前を
// 127.0.0.1へ向け直して（DNS rebinding）同じオリジンとして読みに来るのを防ぐため。
func loopbackHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			http.Error(w, "masuda: only loopback host names are accepted", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			hdr := w.Header()
			hdr.Set("Access-Control-Allow-Origin", origin)
			hdr.Add("Vary", "Origin")
			hdr.Set("Access-Control-Expose-Headers", corsExposed)
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				hdr.Set("Access-Control-Allow-Methods", "GET, POST")
				// Connectのクライアントが付けるヘッダー（Connect-Protocol-Version・Connect-Timeout-Ms・
				// X-Grpc-Web等）は版で増えるので、求められたものをそのまま許す。
				if req := r.Header.Get("Access-Control-Request-Headers"); req != "" {
					hdr.Set("Access-Control-Allow-Headers", req)
				}
				hdr.Set("Access-Control-Max-Age", "7200")
				hdr.Add("Vary", "Access-Control-Request-Method")
				hdr.Add("Vary", "Access-Control-Request-Headers")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

// loopbackHost はHostヘッダーの値がループバックのIPかlocalhostか。
func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
