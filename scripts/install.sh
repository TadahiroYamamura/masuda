#!/bin/sh
# リリースのworkflowが、この1か所だけをタグの版に置き換える。
default_version=__MASUDA_VERSION__

fail() {
  echo "install: $1" >&2
  exit 1
}

usage() {
  cat >&2 <<'EOF'
install: 入れるバージョンが決まっていない。
Releaseに添付されたmasuda_installer.shを使うか、MASUDA_VERSION=X.Y.Z（先頭のvは付けない）を指定して実行する。
  curl -fsSL https://github.com/TadahiroYamamura/masuda/releases/download/vX.Y.Z/masuda_installer.sh | sh
EOF
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 が見つからない。$2"
}

main() {
  version=${MASUDA_VERSION:-$default_version}
  # 置き換え前の目印（先頭が数字でない）かどうかは、文字列の比較ではなく先頭の文字で判定する。
  # 置き換えは目印の文字列をファイル全体で書き換えるので、比較に目印を書くと比較側も変わって判定できなくなる。
  case "$version" in
    [0-9]*) ;;
    *) usage ;;
  esac

  case "$(uname -s)/$(uname -m)" in
    Linux/x86_64) target=linux_amd64 ;;
    Darwin/arm64) target=darwin_arm64 ;;
    *) fail "このプラットフォーム向けの配布物は無い（$(uname -s)/$(uname -m)）" ;;
  esac

  need curl "curlを入れてから、もう一度実行する。"
  need tar "tarを入れてから、もう一度実行する。"
  need npm "Node（22.19以上。npmを含む）を入れてから、もう一度実行する。"
  if command -v sha256sum >/dev/null 2>&1; then
    verify="sha256sum -c -"
  elif command -v shasum >/dev/null 2>&1; then
    verify="shasum -a 256 -c -"
  else
    fail "sha256sum も shasum も見つからない。どちらかを入れてから、もう一度実行する。"
  fi

  tmp=$(mktemp -d) || fail "一時ディレクトリを作れない"
  trap 'rm -rf "$tmp"' EXIT
  trap 'exit 1' HUP INT TERM
  cd "$tmp" || fail "一時ディレクトリに移れない"

  masuda_base=https://github.com/TadahiroYamamura/masuda/releases/download/v$version
  sandbox_base=https://github.com/TadahiroYamamura/masuda-sandbox/releases/download/v$version
  masuda_file=masuda_${version}_${target}.tar.gz
  sandbox_file=masuda-sandbox-$version.tgz

  mkdir masuda sandbox
  echo "install: masuda $version と masuda-sandbox $version を取得する"
  (cd masuda && curl -fsSLO "$masuda_base/$masuda_file" && curl -fsSLO "$masuda_base/SHA256SUMS") ||
    fail "masudaの配布物を取得できない（$masuda_base）"
  (cd sandbox && curl -fsSLO "$sandbox_base/$sandbox_file" && curl -fsSLO "$sandbox_base/SHA256SUMS") ||
    fail "masuda-sandboxの配布物を取得できない（$sandbox_base）"

  # SHA256SUMSにはそのリリースの全部の添付物が載っているので、自分のファイルの行だけを検証する。
  (cd masuda && grep " $masuda_file\$" SHA256SUMS | $verify >/dev/null) ||
    fail "$masuda_file のチェックサムが合わない"
  (cd sandbox && grep " $sandbox_file\$" SHA256SUMS | $verify >/dev/null) ||
    fail "$sandbox_file のチェックサムが合わない"

  tar -xzf "masuda/$masuda_file" -C masuda || fail "$masuda_file を展開できない"
  bindir=$HOME/.local/bin
  mkdir -p "$bindir" || fail "$bindir を作れない"
  install -m 0755 "masuda/masuda_${version}_${target}/masuda" "$bindir/masuda" ||
    fail "$bindir/masuda に置けない"

  if ! npm install -g "$tmp/sandbox/$sandbox_file"; then
    echo "install: npm install -g が失敗した。権限が原因なら、npmのグローバルの置き場所を自分のディレクトリにする（npm config set prefix ~/.local。~/.local/bin をPATHに入れる）。sudoは使わない。" >&2
    exit 1
  fi

  echo
  if command -v masuda >/dev/null 2>&1; then
    masuda version
  else
    "$bindir/masuda" version
  fi
  if command -v masuda-sandbox >/dev/null 2>&1; then
    masuda-sandbox --version
  else
    echo "install: masuda-sandbox がPATHに無い。npmのグローバルのbinディレクトリをPATHに入れる。" >&2
  fi

  case ":$PATH:" in
    *":$bindir:"*) ;;
    *) echo "install: $bindir がPATHに入っていない。シェルの設定に足す。" ;;
  esac
  echo "次は masuda doctor。起動は docs/user/install.md の「起動」。"
}

main "$@"
