"""Exercise the release image's real extraction script, including bad downloads.

Run on Linux: python3 tests/deploy/gateway_extract_test.py
Uses isolated synthetic archives, no account or camera credentials.
"""
import hashlib
import io
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parents[2] / "deploy" / "extract-gateway.sh"


class GatewayExtraction(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.downloads = self.root / "downloads"
        self.downloads.mkdir()
        self.out = self.root / "out"
        self.manifest = self.downloads / "SHA256SUMS.txt"

    def write_archive(self, name, member, payload):
        with tarfile.open(self.downloads / name, "w:gz") as tar:
            info = tarfile.TarInfo(f"{name[:-7]}/{member}")
            info.size = len(payload)
            info.mode = 0o755
            tar.addfile(info, io.BytesIO(payload))

    def archive(self, arch="amd64", version="1.0.0", member="bombecam-gateway"):
        name = f"BombeCam-{version}-linux-{arch}.tar.gz"
        payload = f"synthetic gateway fixture for {arch}".encode()
        self.write_archive(name, member, payload)
        digest = hashlib.sha256((self.downloads / name).read_bytes()).hexdigest()
        with self.manifest.open("a") as manifest:
            manifest.write(f"{digest}  {name}\n")
        return name, payload

    def extract(self, arch="amd64", success=True, reason=None):
        result = subprocess.run(
            ["sh", str(SCRIPT), arch, str(self.downloads), str(self.out)],
            capture_output=True, text=True, timeout=15,
        )
        if success:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertFalse((self.out / "bombecam-gateway").exists())
            # Each failure must come from the check the test is about.
            self.assertIn(reason.lower(), (result.stdout + result.stderr).lower())
        return result

    def test_selects_exact_platform_and_preserves_bytes_and_executable_mode(self):
        for target, download in [("amd64", "amd64"), ("arm64", "arm64"), ("arm", "armv6")]:
            with self.subTest(target=target):
                if self.out.exists():
                    (self.out / "bombecam-gateway").unlink()
                if not self.manifest.exists():
                    fixtures = {arch: self.archive(arch)[1] for arch in ["amd64", "arm64", "armv6"]}
                self.extract(target)
                binary = self.out / "bombecam-gateway"
                self.assertEqual(binary.read_bytes(), fixtures[download])
                self.assertTrue(binary.stat().st_mode & 0o111)

    def test_missing_archive(self):
        self.manifest.write_text("")
        self.extract(success=False, reason="need exactly one BombeCam-")

    def test_multiple_versions(self):
        self.archive()
        self.archive(version="1.0.1")
        self.extract(success=False, reason="need exactly one BombeCam-")

    def test_missing_manifest(self):
        self.archive()
        self.manifest.unlink()
        self.extract(success=False, reason="SHA256SUMS.txt")

    def test_missing_checksum(self):
        self.archive()
        self.manifest.write_text("")
        self.extract(success=False, reason="missing or invalid checksum")

    def test_suffixed_filename_cannot_validate_a_different_archive(self):
        name, _ = self.archive()
        other = self.downloads / (name + ".other")
        other.write_bytes(b"different archive")
        digest = hashlib.sha256(other.read_bytes()).hexdigest()
        self.manifest.write_text(f"{digest}  {other.name}\n")
        self.extract(success=False, reason="missing or invalid checksum")

    def test_duplicate_checksum(self):
        self.archive()
        self.manifest.write_text(self.manifest.read_text() * 2)
        self.extract(success=False, reason="missing or invalid checksum")

    def test_invalid_checksum(self):
        name, _ = self.archive()
        for digest, reason in [("0" * 63, "need exactly one SHA-256"), ("g" * 64, "missing or invalid checksum"),
                               ("not-a-checksum", "missing or invalid checksum")]:
            with self.subTest(digest=digest):
                self.manifest.write_text(f"{digest}  {name}\n")
                self.extract(success=False, reason=reason)

    def test_tampered_archive(self):
        # A valid archive with another program in it: only the checksum can
        # tell it from the real download.
        name, _ = self.archive()
        self.write_archive(name, "bombecam-gateway", b"not the released gateway")
        self.extract(success=False, reason="did NOT match")

    def test_missing_program(self):
        self.archive(member="different-program")
        self.extract(success=False, reason="not found in archive")

    def test_unsupported_architecture(self):
        self.archive()
        self.extract("riscv64", success=False, reason="unsupported gateway architecture")


if __name__ == "__main__":
    unittest.main(verbosity=2)
