#!/usr/bin/env python3
"""Build the audd-cli PyPI wheels from a GoReleaser dist directory.

Each wheel holds the audd binary for one platform in audd_cli/bin and
installs the `audd` command (and `audd-cli`, so `uvx audd-cli` works).

    python3 scripts/build_wheels.py [--dist dist] [--out dist/pypi] [--version X.Y.Z]

The version defaults to the one in dist/metadata.json. Only the Python
standard library is used.
"""

import argparse
import base64
import csv
import hashlib
import io
import json
import os
import re
import zipfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PKG_DIR = os.path.join(ROOT, "pypi", "audd_cli")
README = os.path.join(ROOT, "pypi", "README.md")
LICENSE = os.path.join(ROOT, "LICENSE")

# Go builds are static, so one Linux wheel serves glibc and musl systems.
# Go 1.24 needs macOS 11 or later.
PLATFORM_TAGS = {
    ("linux", "amd64"): ["manylinux_2_17_x86_64", "manylinux2014_x86_64", "musllinux_1_1_x86_64"],
    ("linux", "arm64"): ["manylinux_2_17_aarch64", "manylinux2014_aarch64", "musllinux_1_1_aarch64"],
    ("darwin", "amd64"): ["macosx_11_0_x86_64"],
    ("darwin", "arm64"): ["macosx_11_0_arm64"],
    ("windows", "amd64"): ["win_amd64"],
    ("windows", "arm64"): ["win_arm64"],
}

SUMMARY = "Command-line tool for AudD music recognition: files, URLs, folders, streams, and your account"

# A fixed timestamp keeps the wheels reproducible.
ZIP_DATE = (2026, 1, 1, 0, 0, 0)

_PRE = {"dev": ".dev", "alpha": "a", "a": "a", "beta": "b", "b": "b", "rc": "rc"}


def normalize_version(v):
    """Turn a release version (1.2.3, 1.2.3-rc.1, 0.1.1-dev) into PEP 440."""
    m = re.fullmatch(r"v?(\d+\.\d+\.\d+)(?:-([A-Za-z]+)\.?(\d+)?)?", (v or "").strip())
    if not m:
        raise ValueError(f"cannot turn version {v!r} into a Python package version")
    base, label, num = m.groups()
    if label is None:
        return base
    pre = _PRE.get(label.lower())
    if pre is None:
        raise ValueError(f"cannot turn version {v!r} into a Python package version")
    return f"{base}{pre}{num or 0}"


def _digest(data):
    return "sha256=" + base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b"=").decode()


def _read(path):
    with open(path, "rb") as f:
        return f.read()


def _metadata(version):
    with open(README, encoding="utf-8") as f:
        readme = f.read()
    head = [
        "Metadata-Version: 2.4",
        "Name: audd-cli",
        f"Version: {version}",
        f"Summary: {SUMMARY}",
        "Author-email: \"AudD, LLC\" <hello@audd.io>",
        "License-Expression: MIT",
        "License-File: LICENSE",
        "Project-URL: Homepage, https://audd.io",
        "Project-URL: Documentation, https://docs.audd.io",
        "Project-URL: Source, https://github.com/AudDMusic/audd-cli",
        "Keywords: audd,music recognition,audio fingerprinting,cli",
        "Classifier: Environment :: Console",
        "Classifier: Intended Audience :: Developers",
        "Classifier: Operating System :: MacOS",
        "Classifier: Operating System :: Microsoft :: Windows",
        "Classifier: Operating System :: POSIX :: Linux",
        "Classifier: Topic :: Multimedia :: Sound/Audio :: Analysis",
        "Requires-Python: >=3.8",
        "Description-Content-Type: text/markdown",
    ]
    return ("\n".join(head) + "\n\n" + readme).encode("utf-8")


def _wheel_file(tags):
    lines = ["Wheel-Version: 1.0", "Generator: audd-cli build_wheels.py", "Root-Is-Purelib: false"]
    lines += [f"Tag: py3-none-{t}" for t in tags]
    return ("\n".join(lines) + "\n").encode()


def _add(z, records, name, data, mode=0o644):
    info = zipfile.ZipInfo(name, date_time=ZIP_DATE)
    info.compress_type = zipfile.ZIP_DEFLATED
    info.external_attr = (0o100000 | mode) << 16
    info.create_system = 3  # Unix, so the mode bits are honored
    z.writestr(info, data)
    records.append((name, _digest(data), str(len(data))))


def build_wheel(binary, goos, goarch, version, out):
    tags = PLATFORM_TAGS[(goos, goarch)]
    dist_info = f"audd_cli-{version}.dist-info"
    path = os.path.join(out, f"audd_cli-{version}-py3-none-{'.'.join(tags)}.whl")
    exe = "audd.exe" if goos == "windows" else "audd"

    init = _read(os.path.join(PKG_DIR, "__init__.py")).decode()
    init, n = re.subn(r'^__version__ = ".*"$', f'__version__ = "{version}"', init, flags=re.M)
    if n != 1:
        raise SystemExit("pypi/audd_cli/__init__.py has no __version__ line")

    records = []
    with zipfile.ZipFile(path, "w") as z:
        _add(z, records, "audd_cli/__init__.py", init.encode())
        _add(z, records, "audd_cli/__main__.py", _read(os.path.join(PKG_DIR, "__main__.py")))
        _add(z, records, f"audd_cli/bin/{exe}", _read(binary), 0o755)
        _add(z, records, f"{dist_info}/licenses/LICENSE", _read(LICENSE))
        _add(z, records, f"{dist_info}/METADATA", _metadata(version))
        _add(z, records, f"{dist_info}/WHEEL", _wheel_file(tags))
        _add(
            z,
            records,
            f"{dist_info}/entry_points.txt",
            b"[console_scripts]\naudd = audd_cli.__main__:main\naudd-cli = audd_cli.__main__:main\n",
        )
        buf = io.StringIO()
        w = csv.writer(buf, lineterminator="\n")
        for r in records:
            w.writerow(r)
        w.writerow((f"{dist_info}/RECORD", "", ""))
        info = zipfile.ZipInfo(f"{dist_info}/RECORD", date_time=ZIP_DATE)
        info.compress_type = zipfile.ZIP_DEFLATED
        info.external_attr = (0o100644) << 16
        info.create_system = 3
        z.writestr(info, buf.getvalue())
    return path


def _binaries(dist):
    path = os.path.join(dist, "artifacts.json")
    if not os.path.isfile(path):
        raise SystemExit(f"{path} not found; run goreleaser first")
    with open(path) as f:
        artifacts = json.load(f)
    root = os.path.dirname(os.path.abspath(dist))
    found = {}
    for a in artifacts:
        if a.get("type") != "Binary":
            continue
        key = (a.get("goos"), a.get("goarch"))
        if key not in PLATFORM_TAGS:
            continue
        p = a["path"]
        if not os.path.isabs(p):
            p = os.path.join(root, p)
        found[key] = p
    return found


def build(dist, out, version=None):
    """Build every wheel; returns their paths."""
    if version is None:
        with open(os.path.join(dist, "metadata.json")) as f:
            version = json.load(f)["version"]
    version = normalize_version(version)
    binaries = _binaries(dist)
    missing = [f"{o}/{a}" for (o, a) in PLATFORM_TAGS if (o, a) not in binaries]
    if missing:
        raise SystemExit("no binary in the dist directory for: " + ", ".join(missing))
    os.makedirs(out, exist_ok=True)
    return [build_wheel(binaries[k], k[0], k[1], version, out) for k in PLATFORM_TAGS]


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--dist", default=os.path.join(ROOT, "dist"))
    p.add_argument("--out", default=None, help="default: <dist>/pypi")
    p.add_argument("--version", default=None, help="default: the version in <dist>/metadata.json")
    args = p.parse_args(argv)
    out = args.out or os.path.join(args.dist, "pypi")
    try:
        wheels = build(args.dist, out, args.version)
    except ValueError as e:
        raise SystemExit(str(e))
    for w in wheels:
        print(w)


if __name__ == "__main__":
    main()
