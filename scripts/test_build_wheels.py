"""Tests for build_wheels.py. Run: python3 -m unittest discover -s scripts -p 'test_*.py'"""

import base64
import csv
import hashlib
import io
import json
import os
import sys
import tempfile
import unittest
import zipfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import build_wheels  # noqa: E402


def fake_dist(root, targets):
    dist = os.path.join(root, "dist")
    artifacts = []
    for goos, goarch in targets:
        d = os.path.join(dist, f"audd_{goos}_{goarch}")
        os.makedirs(d)
        name = "audd.exe" if goos == "windows" else "audd"
        path = os.path.join(d, name)
        with open(path, "wb") as f:
            f.write(f"binary for {goos}/{goarch}".encode())
        artifacts.append(
            {
                "name": name,
                "path": os.path.relpath(path, root),
                "goos": goos,
                "goarch": goarch,
                "type": "Binary",
                "extra": {"ID": "audd", "Binary": "audd"},
            }
        )
    # Archives and checksums are listed too and must be ignored.
    artifacts.append({"name": "checksums.txt", "path": "dist/checksums.txt", "type": "Checksum"})
    with open(os.path.join(dist, "artifacts.json"), "w") as f:
        json.dump(artifacts, f)
    with open(os.path.join(dist, "metadata.json"), "w") as f:
        json.dump({"project_name": "audd", "version": "1.2.3"}, f)
    return dist


class NormalizeVersionTest(unittest.TestCase):
    def test_versions(self):
        cases = {
            "1.2.3": "1.2.3",
            "v1.2.3": "1.2.3",
            "0.1.1-dev": "0.1.1.dev0",
            "1.0.0-rc.1": "1.0.0rc1",
            "1.0.0-rc1": "1.0.0rc1",
            "1.0.0-beta.2": "1.0.0b2",
            "1.0.0-alpha": "1.0.0a0",
        }
        for given, want in cases.items():
            self.assertEqual(build_wheels.normalize_version(given), want, given)

    def test_rejects_unknown(self):
        for bad in ["1.2", "1.2.3-SNAPSHOT-abc123", "latest", ""]:
            with self.assertRaises(ValueError, msg=bad):
                build_wheels.normalize_version(bad)


class BuildTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = self.tmp.name
        self.dist = fake_dist(
            self.root,
            [
                ("linux", "amd64"),
                ("linux", "arm64"),
                ("darwin", "amd64"),
                ("darwin", "arm64"),
                ("windows", "amd64"),
                ("windows", "arm64"),
            ],
        )
        self.out = os.path.join(self.root, "wheels")

    def tearDown(self):
        self.tmp.cleanup()

    def build(self, version=None):
        return build_wheels.build(self.dist, self.out, version)

    def test_one_wheel_per_platform(self):
        wheels = sorted(os.path.basename(w) for w in self.build())
        self.assertEqual(
            wheels,
            sorted(
                [
                    "audd_cli-1.2.3-py3-none-macosx_11_0_arm64.whl",
                    "audd_cli-1.2.3-py3-none-macosx_11_0_x86_64.whl",
                    "audd_cli-1.2.3-py3-none-manylinux_2_17_aarch64.manylinux2014_aarch64.musllinux_1_1_aarch64.whl",
                    "audd_cli-1.2.3-py3-none-manylinux_2_17_x86_64.manylinux2014_x86_64.musllinux_1_1_x86_64.whl",
                    "audd_cli-1.2.3-py3-none-win_amd64.whl",
                    "audd_cli-1.2.3-py3-none-win_arm64.whl",
                ]
            ),
        )

    def test_version_override_is_normalized(self):
        wheels = self.build("0.2.0-dev")
        self.assertTrue(all("audd_cli-0.2.0.dev0-" in os.path.basename(w) for w in wheels), wheels)

    def test_wheel_contents(self):
        wheels = self.build()
        linux = next(w for w in wheels if "manylinux_2_17_x86_64" in w)
        windows = next(w for w in wheels if "win_amd64" in w)
        with zipfile.ZipFile(linux) as z:
            names = set(z.namelist())
            for want in [
                "audd_cli/__init__.py",
                "audd_cli/__main__.py",
                "audd_cli/bin/audd",
                "audd_cli-1.2.3.dist-info/METADATA",
                "audd_cli-1.2.3.dist-info/WHEEL",
                "audd_cli-1.2.3.dist-info/entry_points.txt",
                "audd_cli-1.2.3.dist-info/RECORD",
                "audd_cli-1.2.3.dist-info/licenses/LICENSE",
            ]:
                self.assertIn(want, names)
            self.assertEqual(z.read("audd_cli/bin/audd"), b"binary for linux/amd64")
            mode = z.getinfo("audd_cli/bin/audd").external_attr >> 16
            self.assertTrue(mode & 0o111, oct(mode))

            wheel = z.read("audd_cli-1.2.3.dist-info/WHEEL").decode()
            self.assertIn("Root-Is-Purelib: false", wheel)
            for tag in ["manylinux_2_17_x86_64", "manylinux2014_x86_64", "musllinux_1_1_x86_64"]:
                self.assertIn(f"Tag: py3-none-{tag}", wheel)

            meta = z.read("audd_cli-1.2.3.dist-info/METADATA").decode()
            self.assertIn("Name: audd-cli", meta)
            self.assertIn("Version: 1.2.3", meta)
            self.assertIn("License-Expression: MIT", meta)
            self.assertIn("Requires-Python: >=3.8", meta)
            self.assertNotIn("lyrics", meta.lower())

            entry = z.read("audd_cli-1.2.3.dist-info/entry_points.txt").decode()
            self.assertIn("audd = audd_cli.__main__:main", entry)
            # uvx audd-cli runs the command named like the package.
            self.assertIn("audd-cli = audd_cli.__main__:main", entry)

            init = z.read("audd_cli/__init__.py").decode()
            self.assertIn('__version__ = "1.2.3"', init)

            # RECORD lists every other file with its sha256 and size.
            record = list(csv.reader(io.StringIO(z.read("audd_cli-1.2.3.dist-info/RECORD").decode())))
            listed = {row[0]: row for row in record}
            for name in names:
                self.assertIn(name, listed)
                if name.endswith("/RECORD"):
                    continue
                data = z.read(name)
                digest = base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b"=").decode()
                self.assertEqual(listed[name][1], "sha256=" + digest, name)
                self.assertEqual(listed[name][2], str(len(data)), name)
        with zipfile.ZipFile(windows) as z:
            self.assertIn("audd_cli/bin/audd.exe", z.namelist())

    def test_missing_platform_fails(self):
        os.remove(os.path.join(self.dist, "artifacts.json"))
        with self.assertRaises(SystemExit):
            self.build()


if __name__ == "__main__":
    unittest.main()
