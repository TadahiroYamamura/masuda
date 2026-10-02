# 接続

`masuda serve`は公開APIを[Connect](https://connectrpc.com/)で提供する。CLI（`masuda run`・`masuda gate`等）もこのAPIを叩くクライアントの1つで、GUIや他のツールも同じAPIを同じ権限で使う。

## 待ち受け

| 待ち受け | 既定 | 使う側 |
|---|---|---|
| Unixドメインソケット `$XDG_RUNTIME_DIR/masuda.sock` | 常に | CLI、Node.js・Go等のローカルのプロセス |
| ループバック `127.0.0.1:<port>` | `listen`を設定したときだけ | ブラウザのGUI（ブラウザはUDSに繋げない） |

- `XDG_RUNTIME_DIR`が無い環境では`/tmp/masuda-<uid>/masuda.sock`になる。`masuda serve --socket`で変えられる
- ループバックでも待ち受けるには、サーバー全体の設定`$XDG_CONFIG_HOME/masuda/config.json`（未設定なら`~/.config/masuda/config.json`）に`listen`を書く。既定はUDSのみ

    ```json
    { "listen": "127.0.0.1:7788" }
    ```

- `listen`に書けるのはループバックのアドレス（`127.0.0.1:<port>`・`[::1]:<port>`）だけ。それ以外を書くと`masuda serve`は起動しない
- ループバックの待ち受けは、ブラウザから呼べるようにCORSの応答を返す。ローカル利用が前提なので**任意のオリジンを許す**（`Access-Control-Allow-Origin`に要求の`Origin`をそのまま返し、プリフライトでは求められたヘッダーをそのまま許す。`Grpc-Status`等の応答ヘッダーも見せる）
- 代わりに、`Host`ヘッダーがループバックの名前（`127.0.0.1`・`[::1]`・`localhost`）でない要求は403で断る。外部のサイトが自分のドメインを`127.0.0.1`へ向け直して（DNS rebinding）同じオリジンとして読みに来るのを防ぐため
- 待ち受けはどちらも平文。UDSではHTTP/1.1とHTTP/2（h2c）の両方を受ける

## 認証は無い

masuda APIは認証を持たない。同じホストアカウントのプロセスは信頼する、というのが脅威モデルの前提（[全体設計](../design/overview.md)「脅威モデル」）。

APIに届く者は、利用者がCLIでできることをすべてできる。ワークフローの開始、ゲートの承認（＝実リポジトリへのpublish）、秘密の値の登録、egressや特権コマンドの承認、stagingのコードの読み出し。秘密の値をAPIが返すことは無いが、承認を書き換えれば次の実行でゲストへ出せてしまう。

- UDSはファイルの権限で守られる（`$XDG_RUNTIME_DIR`は本人だけが入れる）
- ループバックのポートは、同じマシンの**他のユーザーのプロセスからも届く**。共用のマシンでは`listen`を設定しない
- CORSで任意のオリジンを許すので、`listen`を設定している間は、ブラウザで開いた**どのサイトのスクリプトもAPIを呼べる**（ポートを当てれば）。信頼できないサイトを開くブラウザでは使わないか、使うときだけ`listen`を設定して`masuda serve`を起動し直す
- `listen`に`0.0.0.0`やLANのアドレスを書かない

### リモートから使うとき

別のマシンのGUIから使う必要があるなら、masudaの待ち受けを外へ開かず、利用者が認証付きの経路を用意する。

- SSHのポート転送: `ssh -L 7788:127.0.0.1:7788 host`（ループバックへ）、または`ssh -L /tmp/masuda.sock:/run/user/1000/masuda.sock host`（UDSへ）
- 認証とTLSを持つリバースプロキシを前に置く。Watch等のサーバーストリーミングは長く開いたままになるので、プロキシの読み取りタイムアウトとバッファリングを切る

## 3つの呼び方

Connectのサーバーは同じURLで3つのプロトコルを受ける。どれで呼んでも中身は同じ。

| プロトコル | HTTP | 本文 | 向いている用途 |
|---|---|---|---|
| Connect | 1.1または2 | JSONまたはProtobuf | ブラウザ（`fetch`）、curl、Connectのクライアント |
| gRPC | 2（h2c）のみ | Protobuf | gRPCの既存クライアント・ツール |
| gRPC-Web | 1.1または2 | Protobuf | gRPC-Webのクライアント |

パスは`/<パッケージ>.<サービス>/<メソッド>`。パッケージは`masuda.api.v1`で、例えば`/masuda.api.v1.WorkspaceService/List`。メソッドはすべて`POST`で呼ぶ（GETで呼べる冪等なメソッドの指定はしていない）。

サーバーリフレクションは提供しない。grpcurl等を使うときは`-proto proto/masuda/api/v1/masuda.proto`で定義を渡す。

### JSONの形

ProtobufのJSONマッピングに従う。

- フィールド名は`lowerCamelCase`（`workspace_id`→`workspaceId`）。要求ではprotoの名前（`workspace_id`）も受け付ける
- 既定値（空文字列・0・false・空の配列）のフィールドは応答に出ない。`approvalRequired`が無ければfalse
- `uint64`（`seq`・`afterSeq`・`value`）は文字列（`"42"`）
- `bytes`（`Gate.subject`・`RunRequest.inputs`の値・`BlobChunk.data`）はbase64
- enumは名前の文字列（`"WORKSPACE_STATE_RUNNING"`）
- 時刻（`google.protobuf.Timestamp`）はRFC 3339（`"2026-10-02T12:34:50.502706956Z"`）
- `oneof`は、設定された1つのフィールドだけが出る（`WorkspaceEvent`なら`status`・`engine`・`guestHook`・`http`・`notice`のどれか）

## ブラウザから`fetch`で呼ぶ

クライアントライブラリを使わずに呼ぶ例。`listen`に`127.0.0.1:7788`を設定している前提。

### 1回で返るメソッド

本文はメッセージのJSONそのもの。成功は`200`、失敗は200以外のステータスと`{"code": ..., "message": ...}`。

```js
const base = "http://127.0.0.1:7788";

async function call(method, req) {
  const res = await fetch(`${base}/masuda.api.v1.${method}`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Connect-Protocol-Version": "1" },
    body: JSON.stringify(req),
  });
  const body = await res.json();
  if (!res.ok) {
    // body.code: "not_found", "failed_precondition", "invalid_argument", ...
    throw Object.assign(new Error(body.message), { code: body.code });
  }
  return body;
}

const { workspaces = [] } = await call("WorkspaceService/List", {});
```

エラーはHTTPステータスでなく`code`で見分ける（`invalid_argument`と`failed_precondition`はどちらも400）。返すコードは[エラーコード](errors.md)にある。

### サーバーストリーミング（Watch・GetBlob・BuildImage） {#streaming}

サーバーストリーミングは`Content-Type: application/connect+json`で呼び、要求も応答も**エンベロープ**に包む。エンベロープは5バイトの頭（1バイトのフラグ＋4バイトのビッグエンディアンの長さ）と、その長さのJSON。

- フラグ`0x00`: メッセージ1つ
- フラグ`0x02`: 終わり。中身は`{}`、失敗なら`{"error": {"code": ..., "message": ...}}`

ストリームのエラーは、HTTPステータスが200のまま終わりのエンベロープで届く。

```js
function envelope(obj) {
  const json = new TextEncoder().encode(JSON.stringify(obj));
  const buf = new Uint8Array(5 + json.length);
  new DataView(buf.buffer).setUint32(1, json.length); // フラグ0、長さはビッグエンディアン
  buf.set(json, 5);
  return buf;
}

async function* stream(method, req, signal) {
  const res = await fetch(`${base}/masuda.api.v1.${method}`, {
    method: "POST",
    headers: { "Content-Type": "application/connect+json", "Connect-Protocol-Version": "1" },
    body: envelope(req),
    signal,
  });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const reader = res.body.getReader();
  let buf = new Uint8Array(0);
  for (;;) {
    const { value, done } = await reader.read();
    if (done) throw new Error("stream closed without end-of-stream");
    buf = concat(buf, value);
    while (buf.length >= 5) {
      const flags = buf[0];
      const len = new DataView(buf.buffer, buf.byteOffset).getUint32(1);
      if (buf.length < 5 + len) break;
      const msg = JSON.parse(new TextDecoder().decode(buf.subarray(5, 5 + len)));
      buf = buf.subarray(5 + len);
      if (flags & 0x02) {
        if (msg.error) throw Object.assign(new Error(msg.error.message), { code: msg.error.code });
        return; // 正常な終わり
      }
      yield msg;
    }
  }
}

function concat(a, b) {
  const out = new Uint8Array(a.length + b.length);
  out.set(a);
  out.set(b, a.length);
  return out;
}

for await (const ev of stream("WorkspaceService/Watch", { id: "", afterSeq: "0" })) {
  console.log(ev.seq, ev.workspaceId, Object.keys(ev).find((k) => !["seq", "time", "workspaceId"].includes(k)));
}
```

Watchの読み方（`seq`・再接続）は[典型的な流れ](flows.md#watch)にある。

### curlで試す

UDSにはcurlの`--unix-socket`で届く（URLのホスト名は使われない）。

```sh
curl --unix-socket "$XDG_RUNTIME_DIR/masuda.sock" \
  -H 'Content-Type: application/json' -d '{}' \
  http://localhost/masuda.api.v1.WorkspaceService/List
```

## クライアントを生成する

### TypeScript・JavaScript

リポジトリの[`clients/ts/`](https://github.com/TadahiroYamamura/masuda/tree/main/clients/ts)に、`masuda.proto`からprotoc-gen-es v2で生成したパッケージ（`@masuda/api-client`）を置いてある。生成コードはコミット済みなので、bufが無くても`npm pack`したtarballを入れるだけで使える。

```ts
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { WorkspaceService } from "@masuda/api-client";

const workspaces = createClient(
  WorkspaceService,
  createConnectTransport({ baseUrl: "http://127.0.0.1:7788", httpVersion: "1.1" }),
);
const { workspaces: list } = await workspaces.list({});
```

これはNode.jsの例（`@connectrpc/connect-node`）。`nodeOptions: { socketPath: "/run/user/1000/masuda.sock" }`を足せばUDSに直接繋がる。ブラウザでは`@connectrpc/connect-web`の`createConnectTransport({ baseUrl })`を使う（HTTPの版はブラウザの`fetch`が決めるので`httpVersion`は無い）。入れ方・型の注意は`clients/ts/README.md`にある。

自分で生成するなら、bufの設定で`buf.build/bufbuild/es`（`target=ts`）を使う。

### Go

自分のモジュールの中で`masuda.proto`から生成する。masudaの`gen/`を直接importするのは勧めない（masudaはGoのパッケージとしての互換性を約束しておらず、importするとmasuda-engine等の依存もまとめて引き込む）。

```yaml
# buf.gen.yaml
version: v2
inputs:
  - git_repo: https://github.com/TadahiroYamamura/masuda.git
    branch: main
    subdir: proto
managed:
  enabled: true
  override:
    - file_option: go_package_prefix
      value: example.com/yourtool/gen
plugins:
  - remote: buf.build/protocolbuffers/go
    out: gen
    opt: paths=source_relative
  - remote: buf.build/connectrpc/go
    out: gen
    opt: paths=source_relative
```

UDSへの繋ぎ方。gRPCで呼ぶならHTTP/2（h2c）のトランスポートを、Connectで呼ぶなら普通の`http.Transport`をUDSへダイヤルさせる。

```go
sock := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "masuda.sock")
httpc := &http.Client{Transport: &http2.Transport{
	AllowHTTP: true,
	DialTLSContext: func(ctx context.Context, _, _ string, _ *tls.Config) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	},
}}
ws := apiv1connect.NewWorkspaceServiceClient(httpc, "http://masuda", connect.WithGRPC())
res, err := ws.Get(ctx, connect.NewRequest(&apiv1.GetWorkspaceRequest{Id: id}))
if connect.CodeOf(err) == connect.CodeNotFound { /* ... */ }
```

`connect.WithGRPC()`を外すとConnectプロトコルになり、`http.Transport{DialContext: ...}`（HTTP/1.1）でも動く。
