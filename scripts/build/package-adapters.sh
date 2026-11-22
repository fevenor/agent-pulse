#!/usr/bin/env bash
# 构建独立适配包和未签名目标索引；发布者随后本地签名。
set -euo pipefail
project_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
repository="${1:-$project_root/dist/repository}"
sequence="${2:-1}"
mkdir -p "$repository" "$project_root/dist/adapter-build"
bash "$project_root/scripts/build/build.sh"
for adapter_id in codex antigravity qoderclicn zcode; do
  package_dir="$project_root/dist/adapter-build/$adapter_id"
  mkdir -p "$package_dir"
  cp "$project_root/bin/agent-pulse-adapter-$adapter_id" "$package_dir/adapter"
  manifest="$project_root/adapters/$adapter_id/manifest.json"
  if [[ "$adapter_id" == zcode ]]; then
    manifest="$project_root/adapters/zcode/manifest-linux.json"
  fi
  manifest_target="$project_root/dist/adapter-build/$adapter_id-manifest-target.json"
  node --input-type=module - "$manifest" "$manifest_target" "linux/$(go env GOARCH)" <<'JS'
import {readFileSync,writeFileSync} from "node:fs";
const [source,target,platform]=process.argv.slice(2);
const m=JSON.parse(readFileSync(source,"utf8"));m.platform=platform;
writeFileSync(target,JSON.stringify(m));
JS
  "$project_root/bin/agent-pulse-adapters" pack --source "$package_dir" --manifest "$manifest_target" --repository "$repository" --out "$repository/$adapter_id.json"
done
pnpm --dir "$project_root/adapters/opencode" build
# 插件仍由宿主现有运行时加载；探测/配置控制使用随平台包携带的Go程序。
for arch in amd64 arm64; do
  package_dir="$project_root/dist/adapter-build/opencode-$arch"
  mkdir -p "$package_dir"
  cp "$project_root/adapters/opencode/dist/"* "$package_dir/"
  (cd "$project_root/go" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -buildvcs=false -ldflags '-s -w -X main.adapterID=opencode' -o "$package_dir/control" ./cmd/agent-pulse-adapter)
  manifest_target="$project_root/dist/adapter-build/opencode-$arch-manifest.json"
  node --input-type=module - "$project_root/adapters/opencode/manifest.json" "$manifest_target" "linux/$arch" <<'JS'
import {readFileSync,writeFileSync} from 'node:fs';
const [source,target,platform]=process.argv.slice(2);
const m=JSON.parse(readFileSync(source,'utf8'));m.platform=platform;
writeFileSync(target,JSON.stringify(m));
JS
  "$project_root/bin/agent-pulse-adapters" pack --source "$package_dir" --manifest "$manifest_target" --repository "$repository" --out "$repository/opencode-$arch.json"
done
node --input-type=module - "$repository" "$sequence" <<'JS'
import {readFileSync,writeFileSync} from 'node:fs';
import {join} from 'node:path';
const [directory,sequence] = process.argv.slice(2);
const packages = ['codex','antigravity','qoderclicn','opencode-amd64','opencode-arm64','zcode'].map(id=>JSON.parse(readFileSync(join(directory,id+'.json'),'utf8')));
const number = Number(sequence);
if(!Number.isSafeInteger(number)||number<1) throw new Error('发布序号必须为正整数');
writeFileSync(join(directory,'index-input.json'),JSON.stringify({sequence:number,packages},null,2)+'\n');
JS
