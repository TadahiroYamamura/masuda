module github.com/TadahiroYamamura/masuda

go 1.26.3

require (
	connectrpc.com/connect v1.21.0
	github.com/TadahiroYamamura/masuda-engine v0.0.0
	github.com/modelcontextprotocol/go-sdk v1.7.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2
	golang.org/x/net v0.59.0
	golang.org/x/text v0.42.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

// masuda-engineは公開するまで隣のチェックアウトを使う。
replace github.com/TadahiroYamamura/masuda-engine => ../masuda-engine
