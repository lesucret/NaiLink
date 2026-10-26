"""Exercise menu choices against real BusyBox/UCI in a disposable router."""
import os
import pty
import re
import select
import subprocess
import tarfile
import time
import unittest

import test_router


class InstallerMenuTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        test_router.RouterTests.setUpClass()

    def setUp(self):
        self.router = test_router.RouterTests()
        self.router.setUp()
        self.addCleanup(self.router.tearDown)
        self.router.managed()
        self.router.write('/etc/daed/wing.db', 'existing database')
        self.router.write('/etc/daed/wing.db-wal', 'existing WAL')
        self.router.write('/tmp/pm-calls', '')
        self.config = (self.router.root / 'etc/config/daed').read_bytes()

    def menu(self, input, *args):
        return self.router.run_router('/bin/sh', '/installer/install.sh', '--menu', *args, input=input)

    def unchanged(self):
        root = self.router.root
        self.assertTrue((root / 'tmp/daemon-running').exists())
        self.assertEqual((root / 'etc/daed/wing.db').read_text(), 'existing database')
        self.assertEqual((root / 'etc/daed/wing.db-wal').read_text(), 'existing WAL')
        self.assertEqual((root / 'etc/config/daed').read_bytes(), self.config)
        self.assertFalse((root / 'root/daed-backups').exists())
        self.assertFalse((root / 'var/lock/daed-install.lock').exists())

    def test_terminal_default_shows_menu_without_locking_or_changing_service(self):
        master, slave = pty.openpty()
        process = subprocess.Popen(
            ['chroot', str(self.router.root), '/bin/sh', '/installer/install.sh'],
            stdin=slave, stdout=slave, stderr=slave,
            env={'PATH': '/usr/sbin:/usr/bin:/sbin:/bin'},
        )
        os.close(slave)
        try:
            output = b''
            deadline = time.monotonic() + 5
            while '请选择'.encode() not in output and time.monotonic() < deadline:
                if select.select([master], [], [], .1)[0]:
                    output += os.read(master, 4096)
            self.assertIn('当前版本：0.1.0-1', output.decode())
            self.assertIn('请选择', output.decode())
            self.unchanged()
            os.write(master, b'0\n')
            self.assertEqual(process.wait(timeout=5), 0)
            self.assertEqual((self.router.root / 'tmp/pm-calls').read_text(), '')
        finally:
            if process.poll() is None:
                process.kill()
                process.wait(timeout=5)
            os.close(master)

    def test_exit_invalid_input_and_eof_never_start_an_install(self):
        for input in ('0\n', '\n', '', 'invalid\n0\n'):
            with self.subTest(input=input):
                result = self.menu(input)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.unchanged()
                self.assertEqual((self.router.root / 'tmp/pm-calls').read_text(), '')

    def test_cancelled_uninstall_preserves_everything(self):
        for input in ('2\nn\n0\n', '2\n\n0\n', '2\n'):
            with self.subTest(input=input):
                result = self.menu(input)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn('已取消卸载', result.stdout)
                self.unchanged()

    def test_confirmed_uninstall_is_offline_and_keeps_verified_backup(self):
        for fmt in ('ipk', 'apk'):
            with self.subTest(format=fmt):
                router = test_router.RouterTests()
                router.setUp()
                try:
                    if fmt == 'apk':
                        router.use_apk()
                    router.managed()
                    router.write('/etc/daed/wing.db', 'existing database')
                    router.write('/etc/daed/wing.db-wal', 'existing WAL')
                    router.write('/etc/daed/cores/state.json', '{"current":"dae","phase":"stable"}')
                    router.write('/usr/libexec/daed/cores/sing-box', 'optional managed core', True)
                    router.write('/tmp/installed-vmlinux-btf', '1')
                    router.write('/tmp/installed-luci-app-passwall', '1')
                    (router.root / 'sys/kernel/btf/vmlinux').unlink()
                    (router.root / 'www/luci-static/resources/luci.js').unlink()
                    router.script('/bin/curl', 'touch /tmp/unexpected-network; exit 99')
                    router.script('/bin/uclient-fetch', 'touch /tmp/unexpected-network; exit 99')
                    result = router.run_router('/usr/sbin/nailink', input='2\ny\n')
                    self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
                    self.assertFalse((router.root / 'tmp/unexpected-network').exists())
                    self.assertFalse((router.root / 'etc/daed').exists())
                    self.assertFalse((router.root / 'usr/sbin/nailink').exists())
                    self.assertFalse((router.root / 'usr/sbin/daed-router-setup').exists())
                    self.assertFalse((router.root / 'usr/libexec/daed/cores/sing-box').exists())
                    self.assertTrue((router.root / 'tmp/installed-vmlinux-btf').exists())
                    self.assertTrue((router.root / 'tmp/installed-luci-app-passwall').exists())
                    backups = list((router.root / 'root/daed-backups').glob('*.tar.gz'))
                    self.assertEqual(len(backups), 1)
                    self.assertEqual(backups[0].stat().st_mode & 0o777, 0o600)
                    with tarfile.open(backups[0]) as archive:
                        self.assertEqual(archive.extractfile('etc/daed/wing.db-wal').read(), b'existing WAL')
                finally:
                    router.tearDown()

    def test_menu_install_uses_local_assets_and_keeps_existing_data(self):
        result = self.menu('1\n', '--asset-dir', '/assets', '--release', 'openwrt-v0.1.0')
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertIn('目标版本：0.1.0', result.stdout)
        self.assertEqual((self.router.root / 'etc/daed/wing.db-wal').read_text(), 'existing WAL')
        self.assertTrue((self.router.root / 'usr/sbin/nailink').stat().st_mode & 0o111)
        self.assertEqual(len(list((self.router.root / 'root/daed-backups').glob('*.tar.gz'))), 1)

    def test_menu_environment_check_preserves_service_and_config(self):
        result = self.menu('3\n', '--asset-dir', '/assets')
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.unchanged()
        calls = (self.router.root / 'tmp/pm-calls').read_text().splitlines()
        self.assertFalse(any(line.startswith(('update', 'install ', 'remove ', 'add ', 'del ')) for line in calls))

    def test_online_update_refuses_new_install_or_downgrade_before_stopping(self):
        for installed in (False, True):
            with self.subTest(installed=installed):
                router = test_router.RouterTests()
                router.setUp()
                try:
                    if installed:
                        router.managed()
                        router.write('/usr/share/daed-router/version', '0.7.99-1\n')
                    result = router.install('--install', '--update-only')
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn('Online update refused' if installed else 'Existing managed install required', result.stderr)
                    calls = (router.root / 'tmp/pm-calls').read_text()
                    self.assertFalse(any(line.startswith(('update', 'install ', 'add ', 'remove ', 'del ')) for line in calls.splitlines()))
                    if installed:
                        self.assertTrue((router.root / 'tmp/daemon-running').exists())
                finally:
                    router.tearDown()

    def test_uninstall_stops_maintenance_and_backs_up_private_geodata(self):
        self.router.script('/usr/libexec/daed/nailink-maintenance', 'echo "$*" > /tmp/maintenance-stopped')
        self.router.write('/etc/daed/geodata/data-1/geosite.dat', 'old private rules')
        self.router.write('/var/run/daed-maintenance/job.json', '{}')
        result = self.router.install('--uninstall')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.router.root / 'tmp/maintenance-stopped').read_text().strip(), 'stop')
        self.assertFalse((self.router.root / 'usr/libexec/daed/nailink-maintenance').exists())
        self.assertFalse((self.router.root / 'var/run/daed-maintenance').exists())
        backup = next((self.router.root / 'root/daed-backups').glob('*.tar.gz'))
        with tarfile.open(backup) as archive:
            self.assertEqual(archive.extractfile('etc/daed/geodata/data-1/geosite.dat').read(), b'old private rules')

    def test_explicit_older_release_still_works_without_the_new_shortcut(self):
        self.router.assets(version='0.6.14')
        with (self.router.root / 'fixtures/install').open('a') as stream:
            stream.write('\nrm -f /usr/sbin/nailink\n')
        result = self.router.run_router('/bin/sh', '/installer/install.sh', '--install', '--release', 'openwrt-v0.6.14', '--asset-dir', '/assets')
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertNotIn('请选择', result.stdout)
        self.assertEqual((self.router.root / 'etc/daed/wing.db-wal').read_text(), 'existing WAL')
        self.assertTrue((self.router.root / 'tmp/daemon-running').exists())

    def test_update_download_failure_never_stops_existing_service(self):
        self.router.script('/bin/curl', 'exit 22')
        result = self.menu('1\n')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Cannot download the latest installer', result.stderr)
        self.unchanged()
        self.assertFalse(list((self.router.root / 'tmp').glob('daed-install.*')))

    def test_latest_update_uses_new_installer_without_recursing_into_menu(self):
        source = (test_router.OPENWRT / 'install.sh').read_text()
        source = re.sub(r'^release=openwrt-v\d+\.\d+\.\d+$', 'release=openwrt-v0.7.99', source, flags=re.MULTILINE)
        self.router.write('/fixtures/latest-installer', source)
        self.router.assets(version='0.7.99')
        self.router.script('/bin/curl', '''output=''; url=''
while [ "$#" -gt 0 ]; do
 case "$1" in
 --output) output=$2; shift 2 ;;
 http://*|https://*) url=$1; shift ;;
 *) shift ;;
 esac
done
printf '%s\\n' "$url" >> /tmp/download-urls
case "$url" in
 https://raw.githubusercontent.com/slobys/NaiLink/main/openwrt/install.sh) cp /fixtures/latest-installer "$output" ;;
 https://github.com/slobys/NaiLink/releases/download/openwrt-v0.7.99/*) cp "/assets/${url##*/}" "$output" ;;
 *) exit 44 ;;
esac''')
        result = self.menu('1\n')
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertIn('release 0.7.99', result.stdout)
        self.assertEqual(result.stdout.count('请选择'), 1)
        self.assertEqual((self.router.root / 'etc/daed/wing.db-wal').read_text(), 'existing WAL')
        self.assertTrue((self.router.root / 'tmp/daemon-running').exists())
        self.assertFalse(list((self.router.root / 'tmp').glob('daed-install.*')))

    def test_invalid_download_is_not_executed(self):
        self.router.script('/bin/curl', '''while [ "$#" -gt 0 ]; do
 if [ "$1" = --output ]; then printf 'touch /tmp/invalid-executed; if then\\n' > "$2"; exit 0; fi
 shift
done
exit 44''')
        result = self.menu('1\n')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.router.root / 'tmp/invalid-executed').exists())
        self.unchanged()

    def prepare_network_update(self):
        source = (test_router.OPENWRT / 'install.sh').read_text()
        source = re.sub(r'^release=openwrt-v\d+\.\d+\.\d+$', 'release=openwrt-v0.7.99', source, flags=re.MULTILINE)
        self.router.write('/fixtures/latest-installer', source)
        self.router.assets(version='0.7.99')

    def downloader(self, client, raw_failure=False, all_failure=False):
        self.router.script('/bin/' + client, '''output=''; url=''; accept=''
while [ "$#" -gt 0 ]; do
 case "$1" in
 --output|-O) output=$2; shift 2 ;;
 --header) accept=$2; shift 2 ;;
 --header=*) accept=${1#--header=}; shift ;;
 http://*|https://*) url=$1; shift ;;
 *) shift ;;
 esac
done
printf '%s %s\\n' "''' + client + '''" "$url" >> /tmp/download-urls
[ "$url" != http://127.0.0.1:2023/ ] || exit 0
''' + ('printf partial > "$output"; exit 6\n' if all_failure else '') + '''case "$url" in
 https://raw.githubusercontent.com/slobys/NaiLink/main/openwrt/install.sh)
 ''' + ('exit 6' if raw_failure else 'cp /fixtures/latest-installer "$output"') + ''' ;;
 'https://api.github.com/repos/slobys/NaiLink/contents/openwrt/install.sh?ref=main')
 [ "$accept" = 'Accept: application/vnd.github.raw+json' ] || exit 45
 cp /fixtures/latest-installer "$output" ;;
 https://github.com/slobys/NaiLink/releases/download/openwrt-v0.7.99/*) cp "/assets/${url##*/}" "$output" ;;
 *) exit 44 ;;
esac''')

    def assert_network_update_preserved_data(self, result):
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertIn('release 0.7.99', result.stdout)
        self.assertEqual((self.router.root / 'etc/daed/wing.db-wal').read_text(), 'existing WAL')
        self.assertTrue((self.router.root / 'tmp/daemon-running').exists())
        self.assertFalse(list((self.router.root / 'tmp').glob('daed-install.*')))

    def test_raw_dns_failure_uses_official_api_without_stopping_service(self):
        self.prepare_network_update()
        self.downloader('curl', raw_failure=True)
        self.downloader('uclient-fetch', all_failure=True)
        result = self.menu('1\n')
        self.assert_network_update_preserved_data(result)
        self.assertIn('api.github.com/repos/slobys/NaiLink/contents/openwrt/install.sh?ref=main', (self.router.root / 'tmp/download-urls').read_text())

    def test_curl_failure_uses_firmware_downloader_for_installer_and_assets(self):
        self.prepare_network_update()
        self.downloader('curl', all_failure=True)
        self.downloader('uclient-fetch')
        self.assert_network_update_preserved_data(self.menu('1\n'))
        urls = (self.router.root / 'tmp/download-urls').read_text()
        self.assertIn('uclient-fetch https://raw.githubusercontent.com/', urls)
        self.assertIn('uclient-fetch https://github.com/', urls)

    def test_firmware_downloader_alone_can_use_official_api(self):
        self.prepare_network_update()
        self.downloader('uclient-fetch', raw_failure=True)
        self.assert_network_update_preserved_data(self.menu('1\n'))

    def test_both_origins_fail_preserves_data_and_explains_dns(self):
        self.downloader('curl', all_failure=True)
        self.downloader('uclient-fetch', all_failure=True)
        result = self.menu('1\n')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('raw.githubusercontent.com', result.stderr)
        self.assertIn('api.github.com', result.stderr)
        self.assertIn('DNS', result.stderr)
        self.unchanged()
        self.assertFalse(list((self.router.root / 'tmp').glob('daed-install.*')))

    def test_stale_installer_cannot_downgrade_script_or_installed_version(self):
        self.router.script('/bin/curl', '''while [ "$#" -gt 0 ]; do
 if [ "$1" = --output ]; then cp /fixtures/latest-installer "$2"; exit 0; fi
 shift
done
exit 44''')
        source = (test_router.OPENWRT / 'install.sh').read_text()
        for target, installed in (('0.6.14', '0.1.0-1'), ('0.6.15', '0.7.99-r1')):
            with self.subTest(target=target, installed=installed):
                self.router.write('/fixtures/latest-installer', re.sub(r'^release=openwrt-v\d+\.\d+\.\d+$', 'release=openwrt-v' + target, source, flags=re.MULTILINE))
                self.router.write('/usr/share/daed-router/version', installed + '\n')
                result = self.menu('1\n')
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('older than this script/installation', result.stderr)
                self.unchanged()

    def test_conflicting_action_flags_do_not_reach_the_menu_or_mutate_system(self):
        for action in ('--uninstall', '--install', '--check', '--dry-run'):
            with self.subTest(action=action):
                result = self.menu('2\ny\n', action)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn('请选择', result.stdout)
                self.unchanged()
