#!/usr/bin/env python3
"""Group the release assets into one download per platform."""

import sys
from pathlib import Path
from zipfile import ZipFile


def main(version, source, output):
    assets = sorted(path for path in source.iterdir() if path.is_file())
    apple = [path for path in assets if "-darwin-" in path.name]
    windows = [path for path in assets if "-windows-" in path.name]
    linux = [
        path for path in assets
        if "-linux-" in path.name or path.name == f"masir-desktop-arch-{version}.tar.gz"
    ]
    apk = source / f"masir-android-{version}.apk"
    checksums = [path for path in assets if path.name.startswith("checksums")]
    if not apple or not windows or not linux or not apk.is_file():
        raise SystemExit("Release assets are missing for at least one platform")
    output.mkdir(parents=True, exist_ok=True)
    for platform, files in {
        "macos": apple + checksums,
        "windows": windows + checksums,
        "linux": linux + checksums,
        "android": [apk],
    }.items():
        with ZipFile(output / f"masir-all-{platform}-{version}.zip", "w") as bundle:
            for path in files:
                bundle.write(path, path.name)


if __name__ == "__main__":
    main(sys.argv[1], Path(sys.argv[2]), Path(sys.argv[3]))
