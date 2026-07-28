#!/usr/bin/env python3
"""Generate GCMS release checksums, manifest and release notes."""

from __future__ import annotations

import datetime as dt
import hashlib
import json
import os
from pathlib import Path


def required_env(name: str) -> str:
    value = os.environ.get(name, "").strip()
    if not value:
        raise SystemExit(f"missing required environment variable: {name}")
    return value


def package_target(name: str) -> tuple[str, str] | None:
    stem = name[:-7] if name.endswith(".tar.gz") else Path(name).stem
    parts = stem.rsplit("-", 2)
    if len(parts) != 3:
        return None
    return parts[1], parts[2]


def main() -> None:
    output = Path(os.environ.get("OUTPUT_DIR", "dist")).resolve()
    version = required_env("VERSION")
    repo = required_env("PUBLISH_REPO")
    source_commit = os.environ.get("SOURCE_COMMIT", "local").strip() or "local"
    signing_enabled = os.environ.get("MANIFEST_SIGNING_ENABLED", "1") != "0"
    base = os.environ.get("RELEASE_BASE_URL", "").strip().rstrip("/")
    if not base:
        base = f"https://github.com/{repo}/releases/download/{version}"

    prefix = f"cms-{version}-"
    packages = sorted(
        package
        for package in (*output.glob("*.tar.gz"), *output.glob("*.zip"))
        if package.name.startswith(prefix)
    )
    if not packages:
        raise SystemExit(f"no release packages found in {output}")

    assets: list[dict[str, object]] = []
    checksum_lines: list[str] = []
    for package in packages:
        target = package_target(package.name)
        if target is None:
            continue
        goos, goarch = target
        digest = hashlib.sha256(package.read_bytes()).hexdigest()
        checksum_lines.append(f"{digest}  {package.name}")
        assets.append(
            {
                "name": package.name,
                "os": goos,
                "arch": goarch,
                "url": f"{base}/{package.name}",
                "sha256": digest,
                "size": package.stat().st_size,
            }
        )

    if not assets:
        raise SystemExit(f"no valid GCMS release packages found in {output}")

    manifest: dict[str, object] = {
        "schema": 1,
        "name": "gcms",
        "version": version,
        "release_repo": repo,
        "release_url": f"https://github.com/{repo}/releases/tag/{version}",
        "manifest_url": f"{base}/manifest.json",
        "checksum_url": f"{base}/checksums.txt",
        "published_at": dt.datetime.now(dt.timezone.utc)
        .replace(microsecond=0)
        .isoformat()
        .replace("+00:00", "Z"),
        "notes": f"GCMS {version} 发布包。",
        "source_commit": source_commit,
        "assets": assets,
    }
    if signing_enabled:
        manifest["manifest_signature_url"] = f"{base}/manifest.json.sig"
        manifest["manifest_signature_algorithm"] = "openssl-sha256-rsa"

    output.joinpath("checksums.txt").write_text(
        "\n".join(checksum_lines) + "\n", encoding="utf-8"
    )
    output.joinpath("manifest.json").write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )
    output.joinpath("RELEASE_NOTES.md").write_text(
        f"GCMS {version} 发布包。\n\n"
        "此公开仓库只存放已编译二进制、校验文件和更新清单；"
        "源码仓库保持私有。\n",
        encoding="utf-8",
    )


if __name__ == "__main__":
    main()
