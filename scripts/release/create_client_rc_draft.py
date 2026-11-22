"""仅创建Bridge与Tray候选草稿；复用已公开Hub/Relay和适配包，不构建或发布镜像。"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import tomllib
import zipfile

import create_rc_draft as common

ROOT=Path(__file__).resolve().parents[2]

def validate_version(info,tag,commit,digest,platform):
    expected={"version":tag,"commit":commit,"sha256":digest,"platform":platform,"go_version":"go1.27.4"}
    if any(info.get(key)!=value for key,value in expected.items()):
        raise ValueError("Bridge运行版本、提交、摘要、平台或编译器不符")

def validate_reuse(release,manifest,tag):
    if release.get("isDraft") or release.get("tagName")!=tag or manifest.get("tag")!=tag or manifest.get("commit")!=release.get("targetCommitish"):
        raise ValueError("复用来源必须是同一提交绑定的公开Release")
    for component in ("hub","relay"):
        if not str(manifest.get(component,"")).endswith("-"+component+":"+tag):
            raise ValueError("复用镜像与公开Release不符")
    packages=manifest.get("packages",[])
    actual={(p["id"],p["platform"]) for p in packages}
    # 客户端单独发布仍可原样复用已公开的七包仓库；不能要求旧附件追补新Linux来源。
    published_scope=(common.EXPECTED_SCOPE-{("zcode","linux/amd64"),("opencode","linux/amd64"),("opencode","linux/arm64")})|{("opencode","any")}
    previous_scope=published_scope|{("zcode","linux/amd64")}
    if len(packages)!=len(actual) or actual not in (published_scope,previous_scope,common.EXPECTED_SCOPE):
        raise ValueError("复用适配仓库必须保持已公开的七包或含Linux ZCode的完整八包或两架构OpenCode控制入口的九包集合")

def validate_client_changes(files):
    blocked=("adapters/","go/internal/event/","go/internal/processmode/","go/internal/hub/","go/internal/relay/","go/cmd/agent-pulse-hub/","go/cmd/agent-pulse-relay/","web/hub/","protocol/")
    if len(files)>=300:
        raise ValueError("来源差异过大，不能完整核验客户端发布边界")
    for item in files:
        for name in (item.get("filename",""),item.get("previous_filename","")):
            if name.startswith(blocked) or name in ("go/go.mod","go/go.sum"):
                raise ValueError("客户端候选包含Hub/Relay、协议或适配包变更，需要调整发布范围")

def validate_client_assets(paths):
    expected={"agent-pulse-bridge-linux-amd64","agent-pulse-bridge-linux-arm64","windows-amd64.zip","SHA256SUMS.txt"}
    if {p.name for p in paths}!=expected or len(paths)!=len(expected):
        raise ValueError("客户端候选只能包含Bridge、Tray和对应说明/校验文件")
def read_native_version(path):
    # Actions归档不保留执行位；只对前一步已核验的候选二进制恢复执行位，再运行只读查询。
    path.chmod(0o700)
    return json.loads(common.command(path,"version","--json").stdout)
def validate_build_run(run,commit):
    jobs={j.get("name"):j for j in run.get("jobs",[])}
    if run.get("headSha")!=commit or any(jobs.get(name,{}).get("conclusion")!="success" or jobs.get(name,{}).get("status")!="completed" for name in ("linux","windows / windows")):
        raise ValueError("必须复用同一提交且Linux/Windows构建均成功的产物")

def validate_cross_compiled_version(raw,tag,commit):
    # trimpath会省略构建信息中的ldflags；非本机架构核对注入字符串，运行验证由amd64承担。
    if tag.encode() not in raw or commit.encode() not in raw:
        raise ValueError("交叉构建Bridge缺少候选版本或提交")
def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository",required=True)
    parser.add_argument("--run-id",required=True)
    parser.add_argument("--tag",required=True)
    parser.add_argument("--commit",required=True)
    parser.add_argument("--reuse-release",required=True)
    parser.add_argument("--output",required=True,type=Path)
    args=parser.parse_args()
    version=tomllib.loads((ROOT/"tray/Cargo.toml").read_text(encoding="utf-8"))["package"]["version"]
    common.validate_tag(args.tag,version)
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+",args.repository) or not re.fullmatch(r"[0-9a-f]{40}",args.commit) or not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+(?:-rc\.[1-9][0-9]*)?",args.reuse_release) or args.reuse_release==args.tag:
        raise ValueError("仓库、提交或复用版本无效")
    if not re.fullmatch(r"[0-9]+",args.run_id): raise ValueError("构建工作流编号无效")
    reference=json.loads(common.command("gh","api",f"repos/{args.repository}/git/ref/tags/{args.tag}").stdout)["object"]
    if reference.get("type")=="tag":
        reference=json.loads(common.command("gh","api",f"repos/{args.repository}/git/tags/{reference['sha']}").stdout)["object"]
    if reference.get("type")!="commit" or reference.get("sha")!=args.commit:
        raise ValueError("候选标签不指向已核验的构建提交")
    build=json.loads(common.command("gh","run","view",args.run_id,"--repo",args.repository,"--json","headSha,jobs").stdout)
    validate_build_run(build,args.commit)
    changes=json.loads(common.command("gh","api",f"repos/{args.repository}/compare/{args.reuse_release}...{args.commit}").stdout)
    validate_client_changes(changes.get("files",[]))
    output=args.output.resolve();output.mkdir(parents=True,exist_ok=False)
    assets=output/"assets";assets.mkdir()
    reused=json.loads(common.command("gh","release","view",args.reuse_release,"--repo",args.repository,"--json","isDraft,tagName,targetCommitish").stdout)
    common.command("gh","release","download",args.reuse_release,"--repo",args.repository,"--pattern","candidate-manifest.json","--dir",output/"reuse")
    baseline=json.loads((output/"reuse/candidate-manifest.json").read_text(encoding="utf-8"));validate_reuse(reused,baseline,args.reuse_release)
    for name,directory in (("linux-bridge",output/"linux"),("windows-amd64",output/"windows")):
        common.command("gh","run","download",args.run_id,"--repo",args.repository,"--name",name,"--dir",directory)
    if list((output/"windows").rglob("index-input.json")):
        raise ValueError("客户端候选不能混入重新构建的适配仓库")
    bridges=common.bridge_artifacts(output/"linux")
    for artifact in bridges:
        shutil.copyfile(artifact["path"],assets/artifact["name"])
        if artifact["platform"]=="linux/arm64":
            validate_cross_compiled_version(artifact["path"].read_bytes(),args.tag,args.commit)
        if artifact["platform"]=="linux/amd64":
            info=read_native_version(artifact["path"])
            validate_version(info,args.tag,args.commit,artifact["sha256"],artifact["platform"])
    windows=common.unique_file(output/"windows","windows-amd64.zip")
    with zipfile.ZipFile(windows) as archive:
        if archive.testzip() is not None or sorted(archive.namelist())!=sorted(["agent-pulse-tray.exe","bridge.example.yaml","tray.example.yaml","使用说明.md"]):
            raise ValueError("Windows便携包必须保持单用户入口且不含私人配置或data")
    embedded=json.loads(common.unique_file(output/"windows","bridge-version.json").read_text(encoding="utf-8-sig"))
    validate_version(embedded,args.tag,args.commit,embedded.get("sha256"),"windows/amd64")
    if not re.fullmatch(r"[0-9a-f]{64}",embedded.get("sha256","")):
        raise ValueError("Windows内置Bridge摘要无效")
    shutil.copyfile(windows,assets/"windows-amd64.zip")
    maintenance=output/"maintenance";maintenance.mkdir()
    manifest={"tag":args.tag,"commit":args.commit,"workflow_run_id":args.run_id,"publication_workflow_run_id":os.getenv("GITHUB_RUN_ID",""),"draft":True,"prerelease":True,
              "updated_components":["bridge","tray"],"reuse_release":args.reuse_release,
              "reused_components":{"hub":baseline["hub"],"relay":baseline["relay"],"adapter_packages":baseline["packages"]},
              "bridge_assets":[{k:v for k,v in p.items() if k!="path"} for p in bridges],"windows_embedded_bridge":embedded}
    (maintenance/"candidate-manifest.json").write_text(json.dumps(manifest,ensure_ascii=False,indent=2)+"\n",encoding="utf-8")
    notes=f"""# AgentPulse {args.tag} 客户端修复草稿

**保持草稿和预发布状态，不自动公开。** 本次只更新Linux Bridge及Windows Tray（含内置Bridge）；Hub、Relay和全部适配包沿用已公开{args.reuse_release}，没有构建/发布新镜像、重新签名仓库或改动旧Release。

## 修复

- 已保存的旧OpenCode JSONC解析误判，在当前配置重新校验通过后可重试；宿主配置与文件写入失败不再被隔离成坏包，真正的坏入口、摘要或解包隔离仍保留。
- Windows普通盘符/UNC路径与加长路径别名统一识别，同一配置的重复钩子可在重新同步/启用时去重；保留其他配置和用户钩子。
- Bridge新增version/--version，可输出候选标签、构建提交、编译器、平台及当前程序SHA-256；version --json提供结构化结果。未标注的开发构建显示dev，不冒充候选。

## 本次附件与复用组件

- 用户附件仅为两架构Linux Bridge、Windows单入口ZIP与SHA256SUMS；源码随标签提供。构建清单和指南仅放CI Artifact。
- Hub继续使用 `{baseline['hub']}`；Relay继续使用 `{baseline['relay']}`，无需为RC4重建或升级。
- 适配包继续从现有Hub签名仓库同步，发布公钥和索引保持；本次不提供未签名仓库、发布工具或Hub构建归档。

## 安装与验证

草稿附件不能匿名下载。由有权限的账号下载并核对SHA256SUMS后，Linux安装使用已构建文件：

```sh
curl -fsSL https://raw.githubusercontent.com/fevenor/agent-pulse/main/scripts/install/bridge.sh | bash -s -- --version {args.tag} --install-only \\
  --file /已下载的Bridge路径 --sha256 "对应64位摘要"
```

摘要参数填写64位SHA-256，不是文件路径。已有Bridge停止前确认healthz的pending为0；安装后sync-adapters并启动用户服务，保留原YAML、来源身份、公钥、适配目录和数据。raw安装脚本的默认仍是公开RC3，必须显式选择RC4。

Windows正常退出Tray，备份后只替换EXE并保留tray.yaml与data/；内置Bridge随EXE更新。重新启用Codex或同步适配器后检查同一事件只保留一条同配置入口；按宿主提示审核钩子，不自动绕过用户信任。WSL仍使用独立Bridge。

`agent-pulse-bridge version --json`应显示{args.tag}和本清单commit；源码dev构建不作为候选安装证明。OpenCode JSONC完整向导重启用应完成；首次健康探测短暂连接失败不代表最终服务失败，以最终健康状态为准。

本次不迁移、清空数据，不修改私人Agent配置、手机渠道或自动部署服务，不批量重发历史任务。已有原生事件、主子/记忆过滤、computer use/RDP等未验边界不因客户端修复草稿自动改为通过。arm64只核验交叉构建、格式、摘要与注入元数据，不冒充真机运行。

来源提交：{args.commit}
[客户端构建与检查](https://github.com/{args.repository}/actions/runs/{args.run_id})
[草稿发布维护](https://github.com/{args.repository}/actions/runs/{os.getenv("GITHUB_RUN_ID",args.run_id)})
"""
    (maintenance/"trial-guide.md").write_text(notes,encoding="utf-8")
    sums=[hashlib.sha256(p.read_bytes()).hexdigest()+"  "+p.name for p in sorted(assets.iterdir())]
    (assets/"SHA256SUMS.txt").write_text("\n".join(sums)+"\n",encoding="utf-8")
    validate_client_assets(list(assets.iterdir()))
    fields="isDraft,isPrerelease,tagName,targetCommitish,url,assets"
    existing=common.command("gh","release","view",args.tag,"--repo",args.repository,"--json",fields,allow_failure=True)
    if existing.returncode==0:common.require_draft(json.loads(existing.stdout),args.tag,args.commit)
    else:common.command("gh","release","create",args.tag,"--repo",args.repository,"--target",args.commit,"--draft","--prerelease","--title","AgentPulse "+args.tag+"（客户端修复草稿）","--notes-file",maintenance/"trial-guide.md")
    common.command("gh","release","edit",args.tag,"--repo",args.repository,"--notes-file",maintenance/"trial-guide.md")
    common.command("gh","release","upload",args.tag,"--repo",args.repository,"--clobber",*sorted(assets.iterdir()))
    release=json.loads(common.command("gh","release","view",args.tag,"--repo",args.repository,"--json",fields).stdout)
    common.require_draft(release,args.tag,args.commit);common.validate_uploaded_assets(release,list(assets.iterdir()))
    result={**release,"commit":args.commit,"workflow_run_id":args.run_id,"updated_components":["bridge","tray"],"reuse_release":args.reuse_release}
    (output/"release-result.json").write_text(json.dumps(result,ensure_ascii=False,indent=2)+"\n",encoding="utf-8")
    if os.getenv("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"],"a",encoding="utf-8") as summary:summary.write(f"## 客户端候选草稿\n\n[{args.tag}]({release['url']})\n\n只更新Bridge/Tray；Hub/Relay与适配包复用{args.reuse_release}。\n")
    print(json.dumps(result,ensure_ascii=True))

if __name__=="__main__":main()
