# Masuda

AI（Claude）によるPRレビュー自動化ツール。詳細は [CLAUDE.md](./CLAUDE.md) を参照。

## 導入方法

レビュー対象リポジトリの `.github/workflows/review.yml` として
[`templates/github-workflows/review.yml`](./templates/github-workflows/review.yml) をコピーし、
`ANTHROPIC_API_KEY` をSecretsに登録する。PRで `@ai/review` とコメントするとレビューが起動する。

## ローカルでの動作確認

```sh
python -m venv venv && source venv/bin/activate
pip install -r requirements.txt
export ANTHROPIC_API_KEY=sk-...
scripts/local_review.sh  # 現在のブランチ vs develop の差分をレビュー
```

詳細は [`scripts/local_review.sh`](./scripts/local_review.sh) のコメントを参照。
