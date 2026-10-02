# インストール

## 前提

| | Linux x86_64（WSL2含む） | macOS arm64 |
|---|---|---|
| 仮想化 | QEMU + KVM（`/dev/kvm`） | QEMU + HVF |
| パッケージ | `qemu-system-x86`、`qemu-utils` | `brew install qemu` |
| その他 | Node 22.19以上、Docker（ゲストイメージのビルド）、git | Node 22.19以上、Docker Desktop等、git |

sudoは要らない。Claude Codeはホストではなくゲストイメージの中に入る。

## 導入

masudaは3つのリポジトリからなる（[README](../README.md)）。現時点ではリリース物が無いので、同じディレクトリに並べてチェックアウトしてビルドする。

```sh
git clone https://github.com/TadahiroYamamura/masuda.git
git clone https://github.com/TadahiroYamamura/masuda-engine.git
git clone https://github.com/TadahiroYamamura/masuda-sandbox.git

(cd masuda && go build -o ~/.local/bin/masuda ./cmd/masuda)
```

`masuda-sandbox`の導入は、そのリポジトリのREADMEに従う。

## 起動

```sh
masuda-sandbox serve &
masuda serve &
```

`masuda serve`は`$XDG_RUNTIME_DIR/masuda.sock`で公開APIを待ち受け、状態を`$XDG_DATA_HOME/masuda/`（既定`~/.local/share/masuda/`）に置く。`masuda serve --help`で待ち受け先等を変えられる。

## 実行

```sh
masuda run workflows/develop --branch feat/x --input instructions=@todo.md
```

`masuda run`は実装中（[work-orders.md](work-orders.md)のM5）。
