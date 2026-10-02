module github.com/TadahiroYamamura/masuda

go 1.26.3

require (
	connectrpc.com/connect v1.21.0
	github.com/TadahiroYamamura/masuda-engine v0.0.0
	golang.org/x/net v0.59.0
	google.golang.org/protobuf v1.36.12
)

require golang.org/x/text v0.42.0 // indirect

// masuda-engineは公開するまで隣のチェックアウトを使う。
// M1時点ではまだimportしていないので、go mod tidyはこのrequireを落とす。M4でRunnerを実装すると必要になる。
replace github.com/TadahiroYamamura/masuda-engine => ../masuda-engine
