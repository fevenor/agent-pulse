"""独立适配仓库发布回归；不创建GitHub发布或执行包入口。"""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import zipfile

spec = importlib.util.spec_from_file_location("adapter_release", Path(__file__).resolve().parents[1] / "release/publish_adapter_repository.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

class AdapterRepositoryReleaseTests(unittest.TestCase):
    def test_offline_bundle_only_contains_signed_index_and_packages(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            repo = root / "repository"
            (repo / "packages").mkdir(parents=True)
            (repo / "index.json").write_text("signed")
            (repo / "publisher.key").write_text("不得泄漏")
            package = "a" * 64 + ".zip"
            (repo / "packages" / package).write_bytes(b"package")
            index = {"sequence": 20, "packages": [{"sha256": "a" * 64}]}
            with patch.object(module, "command", return_value=json.dumps(index)) as command:
                got, paths = module.prepare(repo, root / "public", root / "publisher", root / "offline.zip")
                self.assertEqual(got, index)
                command.assert_called_once_with(root / "publisher", "verify", "--repository", repo, "--public", root / "public")
                with zipfile.ZipFile(root / "offline.zip") as bundle:
                    self.assertEqual(set(bundle.namelist()), {"index.json", "packages/" + package})
                with self.assertRaises(ValueError):
                    module.prepare(repo, root / "public", root / "publisher", root / "offline.zip")

    def test_draft_keeps_release_unpublished(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);index=root/"index.json";bundle=root/"offline.zip"
            index.write_bytes(b"signed");bundle.write_bytes(b"bundle")
            result={"isDraft":True,"tagName":"adapters-20","assets":[{"name":p.name,"size":p.stat().st_size} for p in (index,bundle)]}
            with patch.object(module.subprocess,"run") as run,patch.object(module,"command",side_effect=["",json.dumps(result)]) as command:
                run.return_value.returncode=1
                module.publish("owner/repo","a"*40,{"sequence":20},[index],bundle,draft=True)
                self.assertIn("--draft",command.call_args_list[0].args)

    def test_invalid_publisher_or_commit_never_reaches_github(self):
        with patch.object(module, "command", side_effect=RuntimeError("验签失败")):
            with self.assertRaises(RuntimeError):
                module.prepare(Path("repo"), Path("public"), Path("publisher"), Path("offline.zip"))
        for github, commit in (("invalid", "a" * 40), ("owner/repo", "main")):
            with patch.object(module.subprocess, "run") as run:
                with self.assertRaises(ValueError):
                    module.publish(github, commit, {"sequence": 1}, [], Path("offline.zip"))
                run.assert_not_called()

    def test_published_attachment_names_sizes_and_latest_boundary(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            index = root / "index.json"
            bundle = root / "offline.zip"
            index.write_bytes(b"signed")
            bundle.write_bytes(b"bundle")
            result = {"isDraft": False, "tagName": "adapters-20", "assets": [{"name": p.name, "size": p.stat().st_size} for p in (index, bundle)]}
            with patch.object(module.subprocess, "run") as run, patch.object(module, "command", side_effect=["", json.dumps(result)]) as command:
                run.return_value.returncode = 1
                module.publish("owner/repo", "a" * 40, {"sequence": 20}, [index], bundle)
                args = command.call_args_list[0].args
                self.assertIn("--latest=false", args)
                self.assertIn("--prerelease", args)
                self.assertNotIn("--clobber", args)
                self.assertNotIn("--draft", args)

if __name__ == "__main__":
    unittest.main()
