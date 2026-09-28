# ADR-0084: Claudeのトークンはゲストに渡さず、ワークスペースごとのホスト側APIゲートウェイがプレースホルダーを差し替える

## Status

Accepted (2026-09-27)

## Context

サンドボックスVMのClaude Codeは、ホストで`claude setup-token`により発行した長期（1年）OAuthトークンで動く。これまでvmStartはそのトークンを専用のvirtiofs共有（`/masuda-secrets/token`）でゲストに渡し、`runtime/entrypoint.sh`が`CLAUDE_CODE_OAUTH_TOKEN`としてexportしていた。ゲスト内のエージェントは`--dangerously-skip-permissions`で動くため、このファイルも環境変数も読める。エージェントには秘密情報を読ませず、ユーザーが管理するホスト側だけが使えるようにするという方針に、トークンの面では沿えていなかった。

既存のローカルmicroVMツール（[[0083-keep-own-microvm-layer-split-into-masuda-agnostic-package]]の調査）のうち、matchlockとagent-vmは、ゲストに偽の値だけを渡し、ホスト側のプロキシが本物に差し替える方式を採っている。ただし、どちらも443番をTLS終端してゲストにCAを入れる方式で、masudaのegress-proxyがTLSを終端せずSNIだけを見る方針（[[0045-redirect-over-tproxy-for-egress-interception]]）とは相容れない。

2026-09-27、Claude Codeの公式ドキュメント（ネットワーク要件、LLM gateway）を確認し、実機でスパイクを行った（Claude Code 2.1.260）。

## Decision

本物のトークンはホストにだけ置く。ゲストのClaude Codeには、固定のプレースホルダー（`masuda-sandbox-placeholder-token`）を`CLAUDE_CODE_OAUTH_TOKEN`として、ワークスペースごとのAPIゲートウェイの平文HTTPアドレスを`ANTHROPIC_BASE_URL`として渡す。

- **ゲートウェイ**（`masuda internal api-gateway`、`cmd/masuda/apigateway.go`）: vmStartがmcp-relayと同じくワークスペースごとにホスト側で起動する。ブリッジのゲートウェイIPで待ち受け、ポートはmcp-relayと同じ予約範囲から選ぶ。`Authorization: Bearer <プレースホルダー>`のリクエストだけを、トークンファイルの値に差し替えて`https://api.anthropic.com`へTLSで転送する。ほかのヘッダーは`anthropic-beta`も含めてそのまま転送する。プレースホルダー以外の認証情報（または無し）は401で断り、上流へ送らない。トークンファイルはリクエストごとに読むので、登録し直したトークンはVMの再起動なしに効く。接続元はmcp-relayと同じ`--allow-mac`で持ち主のVMに限る
- **ゲスト**: `/masuda-secrets`の共有をやめた。`runtime/entrypoint.sh`・`start_claude.sh`は、カーネル引数`masuda.api_gateway=`からアドレスを得て、`ANTHROPIC_BASE_URL`・プレースホルダー・`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`をexportする
- **ゲートウェイ用の認証情報も`apiKeyHelper`も使わない**: 公式ドキュメント（LLM gatewayの「Subscriptions and gateways」）によれば、これらを設定するとサブスクリプションが使われず、トークン単位の従量課金に切り替わる。一方、`ANTHROPIC_BASE_URL`だけを設定すれば、サブスクリプションのログインが有効な認証情報のまま、リクエストはゲートウェイを通る。そのときゲートウェイは`anthropic-beta`のOAuthの指定を転送しなければならない

スパイクで確認したこと:
- プレースホルダーを渡したClaude Codeは起動時に止まらない。ゲートウェイで差し替えた`/v1/messages`は200を返し、ツールの使用も動いた
- 応答に`Anthropic-Ratelimit-Unified-5h/7d-*`ヘッダーが付いており、サブスクリプションの枠で処理されている
- `ANTHROPIC_BASE_URL`を通らず`api.anthropic.com`へ直接行く通信（bootstrap、MCPレジストリ、1Pテレメトリ、GrowthBookの機能フラグ）は、egress-proxyで拒否されても動作に支障がない。機能フラグは既定値で動く。`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`でエラーは0件になった

## Alternatives Considered

- **egress-proxyで`api.anthropic.com`宛てだけTLSを終端し、プレースホルダーを差し替える（matchlock・agent-vmの方式）**: `ANTHROPIC_BASE_URL`を通らない直接の通信まで差し替えられる利点はある。しかし、masuda用のCAをゲストの信頼ストアに入れる必要があり、TLSを終端しないegress-proxyの方針に例外を作る。egress-proxyはホスト全体で1プロセスなので、そこにHTTPの解釈とトークンの保持を持ち込むことにもなる。直接の通信は止めても支障がないとスパイクで分かったため、この利点は要らなかった
- **`apiKeyHelper`や`ANTHROPIC_AUTH_TOKEN`にプレースホルダーを返させ、ゲートウェイで差し替える**: Claude Codeが正式にサポートするゲートウェイの認証方式だが、サブスクリプションではなく従量課金になる（上記）。masudaはサブスクリプションで動かす前提のため不採用
- **トークンをゲストに渡し続ける（現状維持）**: エージェントがトークンを読める。1年有効のサブスクリプションのトークンで、漏れるとそのアカウントで任意に使われる
- **ゲートウェイをホスト全体で1つにする**: プロセスは減るが、egress-proxyと同じく止めるタイミングが無く、接続元のワークスペースを別の仕組みで判定する必要がある。ワークスペースごとにすれば、mcp-relayと同じ起動・停止と接続元チェックをそのまま使える

## Consequences

- ゲストのエージェントは本物のトークンを読めない。実機テスト`TestManualGuestReachesAPIWithoutHoldingToken`が、ゲストのユーザーが読める範囲にトークンが無いこと（見つかる文字列での対照つき）と、ゲートウェイ経由で応答が返ることを確かめる
- ただし、ゲストは自分のゲートウェイを通して、サブスクリプションの枠でAPIを使える。トークンを持ち出されることは無くなったが、VMが動いている間の利用は防げない。これは元々エージェントに与えている権限の範囲内である
- `ANTHROPIC_BASE_URL`を通らない直接の通信は止めている。そのため、機能フラグは既定値になり、Anthropicへのテレメトリは送られない。WebFetchの事前の安全確認も`api.anthropic.com`へ直接行く通信であり、今後WebFetchを使うときに影響が出うる
- Claude Codeの今後の版で、`ANTHROPIC_BASE_URL`を通らない必須の通信が増えたり、プレースホルダーを起動時に検証するようになったりすると、この方式が壊れる。実機テストで検出する
- ゲスト側のスクリプトが変わったため、`masuda-loop`イメージの再ビルドが必要になった
- プレースホルダーの値は`cmd/masuda/apigateway.go`と`runtime/entrypoint.sh`・`start_claude.sh`の3か所で一致させる必要がある
