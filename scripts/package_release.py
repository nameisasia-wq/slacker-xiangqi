"""Build a Moyu Xiangqi release with the matching verified cloudflared binary."""

import argparse
import hashlib
import io
import json
import os
import platform
import subprocess
import tarfile
import tempfile
import urllib.request
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = json.loads((ROOT / "internal/tunnel/assets.json").read_text())
MAX_BYTES = 100 << 20


def target_for_machine():
    system = {"Darwin": "darwin", "Linux": "linux", "Windows": "windows"}.get(platform.system())
    arch = {"x86_64": "amd64", "AMD64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine())
    if not system or not arch:
        raise SystemExit("unsupported build machine; pass --target explicitly")
    return f"{system}/{arch}"


def download_verified(url, expected_sha):
    request = urllib.request.Request(url, headers={"User-Agent": "moyu-xiangqi-release"})
    with urllib.request.urlopen(request, timeout=120) as response:
        data = response.read(MAX_BYTES + 1)
    if len(data) > MAX_BYTES:
        raise ValueError("cloudflared asset too large")
    if hashlib.sha256(data).hexdigest() != expected_sha:
        raise ValueError("cloudflared SHA-256 mismatch")
    return data


def extract_tar_binary(data):
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
        member = next((m for m in archive if Path(m.name).name == "cloudflared" and m.isfile()), None)
        if member is None or member.size > MAX_BYTES:
            raise ValueError("cloudflared executable missing or oversized")
        with archive.extractfile(member) as binary:
            return binary.read(MAX_BYTES + 1)


def extract_deb_binary(data):
    if not data.startswith(b"!<arch>\n"):
        raise ValueError("invalid Debian package")
    offset = 8
    while offset + 60 <= len(data):
        header = data[offset:offset + 60]
        if header[58:60] != b"`\n":
            raise ValueError("invalid Debian package member")
        size = int(header[48:58].strip())
        start = offset + 60
        if size < 0 or start + size > len(data):
            raise ValueError("invalid Debian package member size")
        if header[:16].strip().rstrip(b"/") == b"data.tar.gz":
            return extract_tar_binary(data[start:start + size])
        offset = start + size + size % 2
    raise ValueError("cloudflared package has no data.tar.gz")


def cached_asset(target, output_dir):
    asset = MANIFEST["assets"][target]
    archive_base = f"moyu-xiangqi-{target.replace('/', '-')}"
    name = "cloudflared.exe" if target.startswith("windows/") else "cloudflared"
    archive_path = output_dir / (archive_base + (".zip" if target.startswith("windows/") else ".tar.gz"))
    if not archive_path.is_file():
        return None
    try:
        member_name = f"{archive_base}/{name}"
        if target.startswith("windows/"):
            with zipfile.ZipFile(archive_path) as archive:
                with archive.open(member_name) as stream:
                    data = stream.read(MAX_BYTES + 1)
        else:
            with tarfile.open(archive_path, "r:gz") as archive:
                member = archive.getmember(member_name)
                if not member.isfile() or member.size > MAX_BYTES:
                    return None
                with archive.extractfile(member) as stream:
                    data = stream.read(MAX_BYTES + 1)
        if len(data) <= MAX_BYTES and hashlib.sha256(data).hexdigest() == asset["sha256"]:
            return data
    except (OSError, KeyError, ValueError, tarfile.TarError, zipfile.BadZipFile):
        pass
    return None


def fetch_asset(target, output_dir):
    cached = cached_asset(target, output_dir)
    if cached is not None:
        return cached
    asset = MANIFEST["assets"][target]
    if asset.get("package_file"):
        url = "https://pkg.cloudflare.com/cloudflared/pool/main/c/cloudflared/" + asset["package_file"]
        try:
            package = download_verified(url, asset["package_sha256"])
            binary = extract_deb_binary(package)
            if hashlib.sha256(binary).hexdigest() != asset["sha256"]:
                raise ValueError("cloudflared package binary SHA-256 mismatch")
            return binary
        except (OSError, ValueError, tarfile.TarError) as error:
            print(f"Cloudflare package repository failed ({error}); trying GitHub release")
    url = f"https://github.com/cloudflare/cloudflared/releases/download/{MANIFEST['version']}/{asset['file']}"
    data = download_verified(url, asset["sha256"])
    if asset["format"] == "binary":
        return data
    if asset["format"] == "tgz":
        return extract_tar_binary(data)
    raise ValueError(f"unknown cloudflared format: {asset['format']}")


def package(target, output_dir):
    goos, goarch = target.split("/")
    with tempfile.TemporaryDirectory() as temp:
        temp = Path(temp)
        game_name = "moyu.exe" if goos == "windows" else "moyu"
        cloud_name = "cloudflared.exe" if goos == "windows" else "cloudflared"
        game = temp / game_name
        env = os.environ.copy()
        env.update(GOOS=goos, GOARCH=goarch, CGO_ENABLED="0")
        subprocess.run(["go", "build", "-o", str(game), "./cmd/moyu"], cwd=ROOT, env=env, check=True)
        cloud = temp / cloud_name
        cloud.write_bytes(fetch_asset(target, output_dir))
        if goos != "windows":
            game.chmod(0o755)
            cloud.chmod(0o755)
        files = {
            game_name: game,
            cloud_name: cloud,
            "README.md": ROOT / "README.md",
            "LICENSE": ROOT / "LICENSE",
            "THIRD_PARTY_NOTICES.md": ROOT / "THIRD_PARTY_NOTICES.md",
            "CLOUDFLARED_LICENSE": ROOT / "third_party/cloudflared/LICENSE",
        }
        output_dir.mkdir(parents=True, exist_ok=True)
        archive_base = f"moyu-xiangqi-{goos}-{goarch}"
        if goos == "windows":
            archive_path = output_dir / f"{archive_base}.zip"
            with zipfile.ZipFile(archive_path, "w", compression=zipfile.ZIP_DEFLATED) as archive:
                for name, path in files.items():
                    archive.write(path, arcname=f"{archive_base}/{name}")
        else:
            archive_path = output_dir / f"{archive_base}.tar.gz"
            with tarfile.open(archive_path, "w:gz") as archive:
                for name, path in files.items():
                    archive.add(path, arcname=f"{archive_base}/{name}")
        print(archive_path)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", choices=sorted(MANIFEST["assets"]), help="OS/CPU; defaults to this machine")
    parser.add_argument("--all", action="store_true", help="build all supported packages")
    parser.add_argument("--output-dir", type=Path, default=ROOT / "dist")
    args = parser.parse_args()
    if args.all and args.target:
        parser.error("use either --all or --target")
    targets = sorted(MANIFEST["assets"]) if args.all else [args.target or target_for_machine()]
    for target in targets:
        package(target, args.output_dir)


if __name__ == "__main__":
    main()
