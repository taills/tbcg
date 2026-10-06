#!/usr/bin/env bash
# 用框架自带的 content-tool 生成「雾港奇谭」的开发公开包，供 Godot 编辑器「TBCG 内容」Dock 分析地图锚点绑定。
# 输出：examples/harbor/client/harbor-public-bundle.json（已被 .gitignore 忽略）。
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
stage="$(mktemp -d "${TMPDIR:-/tmp}/tbcg-harbor.XXXXXX")"
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/games"
ln -s "$root/contracts" "$stage/contracts"
ln -s "$here" "$stage/games/harbor"

out="$here/client/harbor-public-bundle.json"
(cd "$root/server" && go run ./cmd/content-tool public-bundle -root "$stage" -game harbor -out "$out")
echo "$out"
