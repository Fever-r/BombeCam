"""Build and run the actual gateway images; Docker/Buildx and QEMU are required.

python3 tests/deploy/gateway_image_test.py --downloads dist --platform linux/amd64
Add --source to check the default source build as well as the release build.
Failures are fatal; nothing is skipped or pushed to a registry.
"""
import argparse
import hashlib
import http.client
from pathlib import Path
import socket
import subprocess
import tarfile
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
ARCHIVES = {"linux/amd64": "amd64", "linux/arm64": "arm64", "linux/arm/v7": "armv6", "linux/arm/v6": "armv6"}


def run(*args):
    result = subprocess.run(args, cwd=ROOT, check=True, capture_output=True, text=True, timeout=900)
    return result.stdout.strip()


def check_runtime(image, platform, version):
    actual = run("docker", "run", "--rm", "--platform", platform, image, "-version")
    if actual != f"BombeCam {version}":
        raise AssertionError(f"wrong gateway version: {actual}")
    # Prove that the unmodified image installs FFmpeg and can encode and decode.
    run("docker", "run", "--rm", "--platform", platform, "--entrypoint", "sh", image, "-c", """set -eu
ffmpeg -nostdin -hide_banner -loglevel error -f lavfi -i sine=frequency=440:sample_rate=8000 -t 0.25 -ac 1 -c:a aac -f adts /tmp/check.aac
ffmpeg -nostdin -hide_banner -loglevel error -i /tmp/check.aac -f null -
"""
    )
    container = run("docker", "run", "--detach", "--platform", platform,
                    "--publish", "127.0.0.1::8654",
                    "--env", "OSAIO_HTTP=0.0.0.0:8654", "--env", "BOMBECAM_HEADLESS=true",
                    "--env", "BOMBECAM_NO_MEDIAMTX_SUPERVISOR=true", image)
    try:
        host, port = run("docker", "port", container, "8654/tcp").split(":")
        deadline = time.monotonic() + 30
        while True:
            try:
                with socket.create_connection((host, int(port)), timeout=1):
                    break
            except (ConnectionRefusedError, ConnectionResetError, socket.timeout):
                if time.monotonic() >= deadline:
                    raise RuntimeError("gateway did not listen within 30 seconds: " + run("docker", "logs", container))
                time.sleep(0.25)
        # Docker's proxy may accept TCP before the gateway is ready. Retry
        # only low-level socket churn; application status codes fail at once.
        while True:
            connection = http.client.HTTPConnection(host, int(port), timeout=5)
            try:
                connection.request("GET", "/")
                response = connection.getresponse()
                body = response.read()
                if response.status != 200 or b"BombeCam" not in body:
                    raise AssertionError(f"gateway page: HTTP {response.status}")
                break
            except (ConnectionResetError, socket.timeout):
                if time.monotonic() >= deadline or run("docker", "inspect", "--format", "{{.State.Running}}", container) != "true":
                    raise RuntimeError("gateway HTTP startup failed: " + run("docker", "logs", container))
                time.sleep(0.25)
            finally:
                connection.close()
    finally:
        run("docker", "rm", "--force", container)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--downloads", required=True, type=Path)
    parser.add_argument("--platform", choices=ARCHIVES, default="linux/amd64")
    parser.add_argument("--source", action="store_true")
    args = parser.parse_args()
    archives = list(args.downloads.glob(f"BombeCam-*-linux-{ARCHIVES[args.platform]}.tar.gz"))
    if len(archives) != 1:
        raise ValueError("need exactly one download for the requested platform")
    archive = archives[0]
    version = archive.name.removeprefix("BombeCam-").removesuffix(f"-linux-{ARCHIVES[args.platform]}.tar.gz")
    with tarfile.open(archive, "r:gz") as tar:
        expected = hashlib.sha256(tar.extractfile(f"{archive.name[:-7]}/bombecam-gateway").read()).hexdigest()
    for source in ([False, True] if args.source else [False]):
        image = "bombecam-review:" + uuid.uuid4().hex
        command = ["docker", "buildx", "build", "--load", "--platform", args.platform,
                   "--file", "deploy/Dockerfile.gateway", "--tag", image]
        if not source:
            command += ["--build-arg", "GATEWAY_FROM=release", "--build-context", f"downloads={args.downloads.resolve()}"]
        try:
            run(*command, ".")
            if not source:
                container = run("docker", "create", "--platform", args.platform, image)
                try:
                    with tempfile.TemporaryDirectory() as folder:
                        binary = Path(folder) / "bombecam-gateway"
                        run("docker", "cp", f"{container}:/usr/local/bin/bombecam-gateway", str(binary))
                        if hashlib.sha256(binary.read_bytes()).hexdigest() != expected:
                            raise AssertionError("image binary differs from the download")
                finally:
                    run("docker", "rm", container)
            check_runtime(image, args.platform, version)
            print(f"PASS: {args.platform} {'source' if source else 'release'} image, version, FFmpeg encode/decode, HTTP page" + (" and exact binary" if not source else ""), flush=True)
        finally:
            subprocess.run(["docker", "image", "rm", image], capture_output=True, timeout=30)


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as error:
        raise SystemExit(error.stdout + error.stderr) from error
