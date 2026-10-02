# @masuda/api-client

masudaの公開API（[`proto/masuda/api/v1/masuda.proto`](../../proto/masuda/api/v1/masuda.proto)）を、TypeScript・JavaScriptから呼ぶためのパッケージ。中身は[protoc-gen-es](https://github.com/bufbuild/protobuf-es) v2が生成したメッセージ型とサービス記述子（`WorkspaceService`など）だけで、通信は[Connect](https://connectrpc.com/docs/web/getting-started)のクライアントが行う。

- 生成コード（`gen/`）はコミットしてある。使う側にbufやprotocは要らない
- 出力は`.js`と`.d.ts`。TypeScriptのコンパイルを挟まずに素のJavaScriptからも使える
- npmには公開していない。`npm pack`で作ったtarballか、リポジトリのパスを指定して入れる

APIの意味（各RPCの前後関係・エラーコード・典型的な流れ）は[統合開発者向けドキュメント](../../docs/api/index.md)にある。

## 入れる

```sh
# masudaのチェックアウトからtarballを作る
cd clients/ts && npm pack            # → masuda-api-client-0.1.0.tgz

# 使う側
npm install /path/to/masuda-api-client-0.1.0.tgz @bufbuild/protobuf@^2 @connectrpc/connect@^2
npm install @connectrpc/connect-web@^2    # ブラウザから
npm install @connectrpc/connect-node@^2   # Node.jsから
```

`npm install /path/to/masuda/clients/ts`のようにディレクトリを直接指定してもよい。

## 使う

### ブラウザから

ブラウザはUnixドメインソケットに繋げないので、`masuda serve`にループバックでも待ち受けさせる（`$XDG_CONFIG_HOME/masuda/config.json`の`listen`、例: `"127.0.0.1:7788"`）。

```ts
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { WorkspaceService, WorkspaceState } from "@masuda/api-client";

const transport = createConnectTransport({ baseUrl: "http://127.0.0.1:7788" });
const workspaces = createClient(WorkspaceService, transport);

const { workspaces: list } = await workspaces.list({});
for (const w of list) console.log(w.id, w.branch, WorkspaceState[w.state]);
```

### Node.jsから

Node.jsならUDSへ直接繋げる。`httpVersion: "1.1"`で、ソケットのパスは`nodeOptions.socketPath`に渡す（`baseUrl`のホスト名は使われない）。

```ts
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { WorkspaceService } from "@masuda/api-client";

const transport = createConnectTransport({
  baseUrl: "http://masuda",
  httpVersion: "1.1",
  nodeOptions: { socketPath: `${process.env.XDG_RUNTIME_DIR}/masuda.sock` },
});
const workspaces = createClient(WorkspaceService, transport);
```

ループバックに繋ぐなら`nodeOptions`を外し、`baseUrl`を`http://127.0.0.1:<port>`にする。

### サーバーストリーミング（Watch）

`watch`・`getBlob`・`buildImage`は`for await`で読む非同期イテレータを返す。

```ts
const ac = new AbortController();
for await (const ev of workspaces.watch({ id: "", afterSeq: 0n }, { signal: ac.signal })) {
  switch (ev.event.case) {
    case "status": console.log(ev.workspaceId, ev.event.value.state); break;
    case "engine": console.log(ev.event.value.kind, ev.event.value.detail); break;
  }
}
```

### エラー

失敗した呼び出しは`ConnectError`を投げる。`code`（`Code.NotFound`・`Code.FailedPrecondition`等）で分岐する。RPCごとにどのコードを返すかは[エラーコード](../../docs/api/errors.md)にある。

```ts
import { Code, ConnectError } from "@connectrpc/connect";

try {
  await workspaces.get({ id });
} catch (e) {
  const err = ConnectError.from(e);
  if (err.code === Code.NotFound) { /* 消されたワークスペース */ }
}
```

### 型の注意

- `uint64`（`seq`・`afterSeq`・`ServeNotice.value`）は`bigint`
- `bytes`（`Gate.subject`・`RunRequest.inputs`の値・`BlobChunk.data`）は`Uint8Array`。文字列は`new TextEncoder().encode(...)`／`new TextDecoder().decode(...)`で変換する
- enumは接頭辞を落とした名前の数値enum（`WorkspaceState.RUNNING`、`ActivityKind.WAITING_INPUT`）。JSONで見える`"WORKSPACE_STATE_RUNNING"`とは名前が違う
- `oneof`は`{ case, value }`の判別共用体（`WorkspaceEvent.event`）
- 時刻は`google.protobuf.Timestamp`。`timestampDate(ts)`（`@bufbuild/protobuf/wkt`）で`Date`にする

## 作り直す（masudaの開発者向け）

`masuda.proto`を変えたら、このディレクトリで生成し直して生成物もコミットする。

```sh
npm ci
npm run generate   # buf generate（buf.gen.yaml）
npm run check      # 生成コードとexamples/の型検査
npm pack           # tarballが作れることの確認
```

`examples/`はnpmのパッケージに含めない。`node examples/node-uds.ts <ソケット>`で、動いている`masuda serve`に対して試せる（Node.js 22.18以上の型除去で動く）。
