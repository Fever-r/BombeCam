"""Run the boot guard scripts with fake system tools; never installs a firewall.

Linux (or Windows with WSL Ubuntu): python tests/deploy/guard_test.py
"""
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]

def linux_path(path):
    # Resolve the folder only: resolving a symlink itself would check its target.
    path = Path(path).parent.resolve() / Path(path).name
    if os.name == 'nt':
        return '/mnt/' + path.drive[0].lower() + path.as_posix()[2:]
    return str(path)

def run_linux(args, **kwargs):
    if os.name == 'nt':
        args = ['wsl', '-d', 'Ubuntu', '--exec'] + args
    return subprocess.run(args, text=True, encoding='utf-8', errors='replace', timeout=15, **kwargs)

def main():
    (ROOT / '.cache').mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='guard-test-', dir=ROOT / '.cache') as tmp:
        folder = Path(tmp)
        tools = folder / 'bin'; tools.mkdir()
        trace, writes = folder / 'nft.txt', folder / 'writes.txt'
        defaults = folder / 'defaults'
        defaults.write_text('CAMERA_VLAN_IF=camera.42\n', encoding='utf-8', newline='\n')
        scripts = {
            'ip': '#!/bin/sh\nif [ "$1" = "-o" ]; then echo "1: unrelated.30: <UP>"; exit 0; fi\n[ "$1 $2 $3 $4" = "link show dev camera.42" ]\n',
            'nft': '#!/bin/sh\ncat > "$TRACE"\n[ "$NFT_FAIL" != "1" ]\n',
            'mkdir': '#!/bin/sh\necho write-attempt > "$WRITES"\nexit 1\n',
        }
        for name, contents in scripts.items():
            (tools / name).write_text(contents, encoding='utf-8', newline='\n')
        chmod = run_linux(['chmod', '+x'] + [linux_path(tools / n) for n in scripts], capture_output=True)
        assert chmod.returncode == 0, chmod.stderr
        common = ['env', 'PATH=' + linux_path(tools) + ':/usr/bin:/bin',
                  'TRACE=' + linux_path(trace), 'WRITES=' + linux_path(writes)]

        def check(script, interface, *, config=False, nft_fail=False, success=False):
            if trace.exists(): trace.unlink()
            if writes.exists(): writes.unlink()
            result = run_linux(common + ['CAMERA_VLAN_IF=' + interface,
                'BOMBECAM_DEFAULTS_FILE=' + linux_path(defaults if config else folder / 'absent'),
                'NFT_FAIL=' + str(int(nft_fail)), 'sh', linux_path(ROOT / 'deploy/scripts' / script)], capture_output=True)
            assert (result.returncode == 0) == success, (interface, result.stdout, result.stderr)
            assert not writes.exists(), 'invalid installer input attempted privileged writes'
            if success:
                rules = trace.read_text(encoding='utf-8')
                assert 'iifname "camera.42" counter drop' in rules, rules
                assert 'iifname != "camera.42" accept' in rules, rules
            elif not nft_fail:
                assert not trace.exists(), 'invalid interface reached nft'
            assert 'boot secured' not in result.stdout

        for interface in ['', 'unrelated.30', 'lo', 'a' * 16, 'camera";accept', 'camera\n42']:
            check('bombecam-guard.sh', interface)
            check('install-guard.sh', interface)
            print('PASS: guard/installer reject', repr(interface), 'before firewall or privileged writes')
        check('bombecam-guard.sh', 'camera.42', success=True)
        print('PASS: guard targets the explicit existing camera interface')
        check('bombecam-guard.sh', '', config=True, success=True)
        print('PASS: configured camera interface is reused without guessing another VLAN')
        check('bombecam-guard.sh', 'camera.42', nft_fail=True)
        print('PASS: nft application failure is propagated')
        install_root = folder / 'systemd-root'
        units = install_root / 'etc/systemd/system'; units.mkdir(parents=True)
        (units / 'bombecam-guard.service').write_text((ROOT / 'deploy/systemd/bombecam-guard.service').read_text(encoding='utf-8'), encoding='utf-8', newline='\n')
        result = run_linux(['systemctl', '--root', linux_path(install_root), 'enable', 'bombecam-guard.service'], capture_output=True)
        assert result.returncode == 0, result.stderr
        for unit in ['network-pre.target', 'systemd-networkd.service', 'NetworkManager.service', 'networking.service']:
            result = run_linux(['test', '-L', linux_path(units / (unit + '.requires') / 'bombecam-guard.service')], capture_output=True)
            assert result.returncode == 0, unit + ' does not require the guard'
        print('PASS: actual systemctl enable creates required guard dependencies in an isolated install root')

if __name__ == '__main__':
    main()
