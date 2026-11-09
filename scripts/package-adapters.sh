#!/usr/bin/env bash
# 构建独立适配包和未签名目标索引；发布者随后本地签名。
set -euo pipefail
project_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
repository="${1:-$project_root/dist/repository}"
sequence="${2:-1}"
mkdir -p "$repository" "$project_root/dist/adapter-build"
bash "$project_root/scripts/build.sh"
for adapter_id in codex antigravity qoderclicn; do
  package_dir="$project_root/dist/adapter-build/$adapter_id"
  mkdir -p "$package_dir"
  cp "$project_root/bin/agent-pulse-adapter-$adapter_id" "$package_dir/adapter"
  "$project_root/bin/agent-pulse-adapters" pack --source "$package_dir" --manifest "$project_root/adapters/$adapter_id/manifest.json" --repository "$repository" --out "$repository/$adapter_id.json"
done
pnpm --dir "$project_root/adapters/opencode" build
"$project_root/bin/agent-pulse-adapters" pack --source "$project_root/adapters/opencode/dist" --manifest "$project_root/adapters/opencode/manifest.json" --repository "$repository" --out "$repository/opencode.json"
node --input-type=module - "$repository" "$sequence" <<'JS'
import {readFileSync,writeFileSync} from 'node:fs';
import {join} from 'node:path';
const [directory,sequence] = process.argv.slice(2);
const packages = ['codex','antigravity','qoderclicn','opencode'].map(id=>JSON.parse(readFileSync(join(directory,id+'.json'),'utf8')));
const number = Number(sequence);
if(!Number.isSafeInteger(number)||number<1) throw new Error('发布序号必须为正整数');
writeFileSync(join(directory,'index-input.json'),JSON.stringify({sequence:number,packages},null,2)+'\n');
JS
