# IronLark

[English](README.md) | [French](README.fr.md) | [Spanish](README.es.md) | [日本語](README.ja.md) | [中文文档](README.zh-CN.md)

IronLark は SSH 向けに設計された AI オペレーターです。リモートマシンに SSH で入ったあと、問題の調査、修正、監視、報告をターミナルの中だけで進めたいときに使うことを想定しています。

このページは日本語の概要です。最も完全で最新の情報は `README.md` の英語版です。

## IronLark を使う理由

IronLark は、SSH セッションの中でそのまま動くオペレーターのように使えます。

- サーバー、ログ、設定、プロセス、ポート、リポジトリを調査できる
- 単発コマンドと `lk agent` のあいだでコンテキストを保持できる
- サービス復旧をバックグラウンドで継続し、あとで結果を確認できる
- サービスを継続監視し、証拠を残しながら明らかな再起動系インシデントに対応できる
- watcher、recovery、incident の運用メモリをローカルに保持できる
- `lk ps` で動作中の IronLark プロセスを確認し、停止や強制終了ができる

## IronLark の動き方

IronLark は実運用のターミナル作業で手間を減らすように作られています。

- まずマシンやリポジトリのローカルな状況を見て、次の一歩を決める
- 単純で安全な調査は毎回止まらずにそのまま実行する
- 危険なコマンドやファイル変更では明確な承認ポイントで止まる
- すでに見つけたことを覚えるので、次の質問が無状態になりにくい
- バックグラウンド作業、incident、recovery の履歴をマシン上に残す

目的は、ターミナルの中で汎用チャットボットになることではありません。目的は「このマシンで何が起きているのか」を素早く把握し、「何が変わったか」「次に何をすべきか」へ進みやすくすることです。

## IronLark が向いている場面

特に向いているのは次のような場面です。

- SSH 越しに本番サーバーを調査するとき
- サービス復旧を進めて、あとから状態を確認したいとき
- 複数の端末セッションにまたがって incident を追いたいとき
- 設定ファイルを慎重に直接編集したいとき

より広い IDE 中心の開発ワークフローについては、英語版 `README.md` の説明が最も詳しいです。

## クイックスタート

### ローカルマシン

```bash
curl -fsSL https://raw.githubusercontent.com/richardsondx/IronLark/main/install.sh | sh
mkdir -p ~/.config/lark
cat > ~/.config/lark/.env <<'EOF'
OPENAI_API_KEY=your_key_here
EOF
lk init
lk version
lk model
lk config test
lk "hello"
```

### SSH 経由のリモートサーバー

```bash
ssh root@your-server-ip
curl -fsSL https://raw.githubusercontent.com/richardsondx/IronLark/main/install.sh | sh
lk init
lk "what can you help me do on this server?"
lk agent
```

## オペレーターワークフロー

### サービスを復旧する

```bash
lk recover "restore openclaw and keep going until it is stable"
```

### サービスを監視する

```bash
lk watch openclaw
```

### バックグラウンド作業を確認する

```bash
lk ps
lk watch list
lk recover list
```

## よく使うコマンド

- `lk "task"`: execute-first の単発実行
- `lk --plan "task"`: 実行前に見えるプランを表示
- `lk agent`: SSH 向けの対話セッション
- `lk edit <path> [instruction]`: diff を確認しながらファイル編集
- `lk run "<command>"`: ガード付きでシェルコマンド実行
- `lk context`: 現在の永続コンテキストを表示
- `lk policy list`: マシンのルールを表示
- `lk ps`: 実行中の IronLark プロセスを表示

## Open Source

- ライセンス: GNU Affero General Public License v3.0 (AGPL-3.0)
- コマンド名: `lark` と `lk`
- プロジェクト名: IronLark
