// Package sandboxcontract は、masudaがどのsandbox.protoから生成されたかを表す。
// sandbox serviceのGetServerInfoが返すcontract_sha256と比べ、同じ契約の文面を
// 共有しているかを確かめるのに使う。
package sandboxcontract

// sha.goはsandbox.protoの文面から作る。gen/のsandboxクライアントと同じ文面から
// 作り直すため、buf generateと一緒に走らせる。
//go:generate go run gen.go
