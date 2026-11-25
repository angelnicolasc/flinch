import hashlib
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest


SCRIPT = pathlib.Path(__file__).with_name("release_manifests.py")


class ReleaseManifestsTest(unittest.TestCase):
    def setUp(self):
        self.scratch = tempfile.TemporaryDirectory()
        self.addCleanup(self.scratch.cleanup)
        self.root = pathlib.Path(self.scratch.name)
        self.checksums = self.root / "checksums.txt"
        self.output = self.root / "manifests"
        self.archives = {}
        for platform in ("darwin", "linux", "windows"):
            for arch in ("amd64", "arm64"):
                suffix = "zip" if platform == "windows" else "tar.gz"
                name = f"flinch_1.2.3_{platform}_{arch}.{suffix}"
                self.archives[name] = hashlib.sha256(name.encode()).hexdigest()
        self.checksums.write_text(
            "".join(f"{digest}  {name}\n" for name, digest in self.archives.items())
        )

    def generate(self, tag):
        return subprocess.run(
            [sys.executable, str(SCRIPT), tag, str(self.checksums), str(self.output)],
            capture_output=True,
            text=True,
        )

    def test_release_archives_and_digests_match_for_each_architecture(self):
        result = self.generate("v1.2.3")
        self.assertEqual(result.returncode, 0, result.stderr)
        formula = (self.output / "Formula" / "flinch.rb").read_text()
        scoop = json.loads((self.output / "flinch.json").read_text())
        for name, digest in self.archives.items():
            url = f"https://github.com/angelnicolasc/flinch/releases/download/v1.2.3/{name}"
            if "windows" in name:
                arch = "arm64" if "arm64" in name else "64bit"
                spec = scoop["architecture"][arch]
                self.assertEqual(url, spec["url"])
                self.assertEqual(digest, spec["hash"])
                self.assertEqual(name.removesuffix(".zip"), spec["extract_dir"])
            else:
                self.assertIn(f'url "{url}"\n', formula)
                self.assertIn(f'sha256 "{digest}"', formula)
        self.assertEqual("flinch.exe", scoop["bin"])

    def test_rejects_prerelease_and_incomplete_checksums(self):
        self.assertNotEqual(self.generate("v1.2.3-rc.1").returncode, 0)
        self.checksums.write_text(self.checksums.read_text().splitlines()[0] + "\n")
        self.assertNotEqual(self.generate("v1.2.3").returncode, 0)
        self.assertFalse(self.output.exists())


if __name__ == "__main__":
    unittest.main()
