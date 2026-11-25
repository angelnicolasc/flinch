"""Generate package-manager metadata for a stable Flinch release."""

import argparse
import json
from pathlib import Path
import re


STABLE_TAG = re.compile(r"v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)", re.ASCII)
PROJECT = "https://github.com/angelnicolasc/flinch"
DESCRIPTION = "Turn corrections to coding agents into repository checks"


def generate(tag, checksum_file, output):
    if not STABLE_TAG.fullmatch(tag):
        raise ValueError("package managers require a stable release tag such as v1.2.3")
    version = tag[1:]
    checksums = {}
    for line in checksum_file.read_text(encoding="utf-8").splitlines():
        entry = re.fullmatch(r"([0-9a-f]{64})  ([^/\\\s]+)", line)
        if not entry or entry[2] in checksums:
            raise ValueError("invalid or duplicate entry in checksums.txt")
        checksums[entry[2]] = entry[1]

    def archive(platform, arch):
        suffix = "zip" if platform == "windows" else "tar.gz"
        directory = f"flinch_{version}_{platform}_{arch}"
        filename = f"{directory}.{suffix}"
        if filename not in checksums:
            raise ValueError(f"missing checksum for {filename}")
        return {
            "url": f"{PROJECT}/releases/download/{tag}/{filename}",
            "hash": checksums[filename],
            "extract_dir": directory,
        }

    formula = [
        "class Flinch < Formula",
        f'  desc "{DESCRIPTION}"',
        f'  homepage "{PROJECT}"',
        f'  version "{version}"',
        '  license "Apache-2.0"',
    ]
    for platform, block in (("darwin", "on_macos"), ("linux", "on_linux")):
        formula += ["", f"  {block} do"]
        for arch, cpu in (("amd64", "intel"), ("arm64", "arm")):
            record = archive(platform, arch)
            formula += [
                f"    on_{cpu} do",
                f'      url "{record["url"]}"',
                f'      sha256 "{record["hash"]}"',
                "    end",
            ]
        formula.append("  end")
    formula += [
        "",
        "  def install",
        '    bin.install "flinch"',
        "  end",
        "",
        "  test do",
        '    assert_match version.to_s, shell_output("#{bin}/flinch --version")',
        "  end",
        "end",
    ]
    scoop = {
        "version": version,
        "homepage": PROJECT,
        "license": "Apache-2.0",
        "description": DESCRIPTION,
        "architecture": {
            "64bit": archive("windows", "amd64"),
            "arm64": archive("windows", "arm64"),
        },
        "bin": "flinch.exe",
    }
    (output / "Formula").mkdir(parents=True, exist_ok=True)
    (output / "Formula" / "flinch.rb").write_text("\n".join(formula) + "\n", encoding="utf-8")
    (output / "flinch.json").write_text(json.dumps(scoop, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("tag")
    parser.add_argument("checksums", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    try:
        generate(args.tag, args.checksums, args.output)
    except (OSError, ValueError) as error:
        parser.exit(1, f"{error}\n")
