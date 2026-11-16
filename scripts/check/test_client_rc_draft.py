"""客户端候选边界回归：不联网、不构建、不发布，保留Hub/Relay与适配包来源。"""
import importlib.util
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace

ROOT=Path(__file__).resolve().parents[2]
sys.path.insert(0,str(ROOT/"scripts/release"))
spec=importlib.util.spec_from_file_location("client_rc",ROOT/"scripts/release/create_client_rc_draft.py")
module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)

class ClientDraftTests(unittest.TestCase):
    def test_native_query_restores_artifact_execution_permission(self):
        with tempfile.TemporaryDirectory() as directory:
            path=Path(directory)/"candidate";path.write_bytes(b"fixture-not-executed");path.chmod(0o600)
            with patch.object(module.common,"command",return_value=SimpleNamespace(stdout='{"version":"v0.5.0-rc.4"}')) as run:
                self.assertEqual(module.read_native_version(path)["version"],"v0.5.0-rc.4")
                run.assert_called_once_with(path,"version","--json")
                if sys.platform!="win32":self.assertTrue(path.stat().st_mode & 0o100)
    def test_version_is_bound_to_real_artifact_and_commit(self):
        good={"version":"v0.5.0-rc.4","commit":"a"*40,"sha256":"b"*64,"platform":"windows/amd64","go_version":"go1.27.4"}
        module.validate_version(good,good["version"],good["commit"],good["sha256"],good["platform"])
        for key,value in (("version","dev"),("commit","c"*40),("sha256","d"*64),("platform","linux/amd64"),("go_version","go1.25.11")):
            with self.assertRaises(ValueError):module.validate_version({**good,key:value},good["version"],good["commit"],good["sha256"],good["platform"])

    def test_reuse_requires_published_baseline_and_matching_images(self):
        tag="v0.5.0-rc.3";release={"isDraft":False,"tagName":tag,"targetCommitish":"a"*40}
        manifest={"tag":tag,"commit":"a"*40,"hub":"ghcr.io/example/agent-pulse-hub:"+tag,"relay":"ghcr.io/example/agent-pulse-relay:"+tag,"packages":[{"id":i,"platform":p} for i,p in module.common.EXPECTED_SCOPE]}
        module.validate_reuse(release,manifest,tag)
        for changed in ({**release,"isDraft":True},{**release,"targetCommitish":"b"*40}):
            with self.assertRaises(ValueError):module.validate_reuse(changed,manifest,tag)
        with self.assertRaises(ValueError):module.validate_reuse(release,{**manifest,"hub":"ghcr.io/example/agent-pulse-hub:v0.5.0-rc.4"},tag)

    def test_only_client_assets_are_allowed(self):
        names=["agent-pulse-bridge-linux-amd64","agent-pulse-bridge-linux-arm64","windows-amd64.zip","install-bridge.sh","candidate-manifest.json","trial-guide.md","SHA256SUMS.txt"]
        paths=[Path(n) for n in names];module.validate_client_assets(paths)
        for extra in ("hub-build.tar","adapters-unsigned.tar.gz","agent-pulse-relay-linux-amd64"):
            with self.assertRaises(ValueError):module.validate_client_assets(paths+[Path(extra)])
        with self.assertRaises(ValueError):module.validate_client_assets(paths[:-1])

    def test_changed_servers_or_adapters_cannot_be_silently_skipped(self):
        module.validate_client_changes([{"filename":"go/internal/adapters/manager.go"},{"filename":"go/cmd/agent-pulse-bridge/version.go"}])
        for name in ("go/internal/hub/store.go","go/internal/relay/relay.go","go/internal/event/event.go","adapters/opencode/index.ts","protocol/schema.json","web/hub/src/main.ts","go/go.mod"):
            with self.assertRaises(ValueError):module.validate_client_changes([{"filename":name}])
        with self.assertRaises(ValueError):module.validate_client_changes([{"filename":"safe.txt","previous_filename":"adapters/codex/manifest.json"}])

    def test_passed_builds_are_reusable_even_if_publication_failed(self):
        run={"headSha":"a"*40,"conclusion":"failure","jobs":[{"name":name,"status":"completed","conclusion":"success"} for name in ("linux","windows / windows")]}
        module.validate_build_run(run,"a"*40)
        for changed in ({**run,"headSha":"b"*40},{**run,"jobs":run["jobs"][:1]}):
            with self.assertRaises(ValueError):module.validate_build_run(changed,"a"*40)

    def test_trimpath_does_not_require_missing_linker_flags(self):
        tag="v0.5.0-rc.4";commit="a"*40
        module.validate_cross_compiled_version(b"binary\0"+tag.encode()+b"\0"+commit.encode(),tag,commit)
        for raw in (b"binary",tag.encode()):
            with self.assertRaises(ValueError):module.validate_cross_compiled_version(raw,tag,commit)
    def test_client_workflow_never_builds_or_publishes_images(self):
        text=(ROOT/".github/workflows/release-clients.yml").read_text(encoding="utf-8")
        for forbidden in ("images.yml","docker/","docker build","docker login","package-adapters","hub-build"):
            self.assertNotIn(forbidden,text)
        self.assertIn("clients_only: true",text)
        self.assertIn("create_client_rc_draft.py",text)
        full=(ROOT/".github/workflows/release-draft.yml").read_text(encoding="utf-8")
        self.assertNotIn("tags: ['v*-rc.*']",full)
        self.assertIn("workflow_dispatch",full)
        windows=(ROOT/".github/workflows/windows.yml").read_text(encoding="utf-8")
        self.assertIn("      clients_only:",windows)
        self.assertIn("if: ${{ !inputs.clients_only }}",windows)

if __name__=="__main__":unittest.main()