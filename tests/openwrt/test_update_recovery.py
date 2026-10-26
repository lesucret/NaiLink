"""Online update rollback with native BusyBox/UCI and package-manager faults."""
from pathlib import Path
import shutil
import sqlite3
import tarfile
import unittest

import test_router


class UpdateRecoveryTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        test_router.RouterTests.setUpClass()

    def make_router(self, mode, fmt='ipk', running=True):
        case = test_router.RouterTests()
        case.setUp()
        self.addCleanup(case.tearDown)
        if fmt == 'apk': case.use_apk()
        case.managed(running=running)
        case.write('/etc/daed/wing.db', 'original nodes/config/groups')
        case.write('/etc/daed/wing.db-wal', 'original WAL')
        before = (case.root/'etc/config/daed').read_bytes()
        shutil.copytree(case.root/'assets', case.root/'old-assets')
        case.assets(fmt, 'aarch64_cortex-a53' if fmt == 'apk' else 'x86_64', '0.2.0')
        shutil.copytree(case.root/'old-assets', case.root/'assets/rollback')
        case.write('/tmp/recovery-mode', mode)
        # Pass package paths into a fault injector. The real package solvers'
        # downgrade behavior is covered independently by native APK/opkg tests.
        for manager in ('opkg', 'apk'):
            script = case.root/'bin'/manager
            if not script.exists() or script.is_symlink(): continue
            text = script.read_text()
            if manager == 'opkg':
                text = text.replace('case "$1" in', '[ "$1" != --force-downgrade ] || shift\ncase "$1" in', 1)
                text = text.replace('install) /fixtures/install ;;', 'install) shift; /fixtures/update-fault "$@" ;;')
            else:
                text = text.replace('*) /fixtures/install ;;', '*) shift; /fixtures/update-fault "$@" ;;')
            script.write_text(text)
        case.script('/fixtures/update-fault', '''mode=$(cat /tmp/recovery-mode)
case "$*" in
 */rollback/*)
  [ "$mode" != rollback_failed ] || exit 24
  rm -f /tmp/http-fail
  /fixtures/install
  echo 0.1.0-1 > /usr/share/daed-router/version ;;
 *)
  /fixtures/install
  echo 0.2.0-1 > /usr/share/daed-router/version
  echo 'new process DB' > /etc/daed/wing.db
  echo 'new process WAL' > /etc/daed/wing.db-wal
  case "$mode" in package_failed|rollback_failed) exit 23;; http_failed) touch /tmp/http-fail;; esac ;;
esac''')
        case.script('/bin/sleep', ':')
        return case, before

    def test_package_and_management_failure_restore_data_version_and_service(self):
        for fmt in ('ipk', 'apk'):
            for mode in ('package_failed', 'http_failed'):
                with self.subTest(format=fmt, failure=mode):
                    case, before = self.make_router(mode, fmt)
                    result = case.install('--install', '--update-only')
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn('previous installation and service state restored', result.stderr)
                    self.assertEqual((case.root/'usr/share/daed-router/version').read_text().strip(), '0.1.0-1')
                    self.assertEqual((case.root/'etc/config/daed').read_bytes(), before)
                    self.assertEqual((case.root/'etc/daed/wing.db').read_text(), 'original nodes/config/groups')
                    self.assertEqual((case.root/'etc/daed/wing.db-wal').read_text(), 'original WAL')
                    self.assertTrue((case.root/'tmp/daemon-running').exists())
                    self.assertFalse((case.root/'tmp/autostart').exists())
                    failed = next((case.root/'root/daed-backups').glob('failed-update-*.tar.gz'))
                    with tarfile.open(failed) as archive:
                        self.assertEqual(archive.extractfile('etc/daed/wing.db').read(), b'new process DB\n')

    def test_backup_failure_restarts_untouched_old_service(self):
        case, before = self.make_router('backup_failed')
        case.script('/bin/tar', 'exit 23')
        result = case.install('--install', '--update-only')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('previous installation and service state restored', result.stderr)
        self.assertTrue((case.root/'tmp/daemon-running').exists())
        self.assertEqual((case.root/'etc/config/daed').read_bytes(), before)

    def test_missing_or_corrupt_rollback_never_stops_service(self):
        for missing in (False, True):
            with self.subTest(missing=missing):
                case, before = self.make_router('package_failed')
                asset = case.root/'assets/rollback/runtime.ipk'
                if missing: asset.unlink()
                else: asset.write_text('corrupted')
                case.write('/tmp/service-calls', '')
                result = case.install('--install', '--update-only')
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn('stop', (case.root/'tmp/service-calls').read_text().splitlines())
                self.assertTrue((case.root/'tmp/daemon-running').exists())
                self.assertEqual((case.root/'etc/config/daed').read_bytes(), before)

    def test_failed_rollback_is_not_reported_as_recovered_and_backup_survives(self):
        case, _ = self.make_router('rollback_failed')
        result = case.install('--install', '--update-only')
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('previous installation and service state restored', result.stderr)
        self.assertFalse((case.root/'tmp/daemon-running').exists())
        backup = next((case.root/'root/daed-backups').glob('*.tar.gz'))
        with tarfile.open(backup) as archive:
            self.assertEqual(archive.extractfile('etc/daed/wing.db').read(), b'original nodes/config/groups')

    def test_rollback_preserves_stopped_service(self):
        case, _ = self.make_router('package_failed', running=False)
        result = case.install('--install', '--update-only')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('previous installation and service state restored', result.stderr)
        self.assertFalse((case.root/'tmp/daemon-running').exists())

    def test_external_database_is_not_overwritten_by_automatic_recovery(self):
        case, _ = self.make_router('package_failed')
        external = case.write('/mnt/external.db','external nodes')
        database = case.root/'etc/daed/wing.db'; database.unlink(); database.symlink_to('/mnt/external.db')
        result = case.install('--install','--update-only')
        self.assertNotEqual(result.returncode,0)
        self.assertIn('symlinked',result.stderr)
        self.assertEqual(external.read_text(),'external nodes')
        self.assertTrue((case.root/'tmp/daemon-running').exists())

    def test_sqlite_rows_recover_with_or_without_an_original_wal(self):
        for with_wal in (False,True):
            with self.subTest(with_wal=with_wal):
                case, _ = self.make_router('package_failed')
                source = Path(case.temp.name)/'original.db'
                connection = sqlite3.connect(source)
                if with_wal: connection.execute('PRAGMA journal_mode=WAL')
                connection.execute('CREATE TABLE nodes (id INTEGER PRIMARY KEY, link TEXT)')
                connection.execute('INSERT INTO nodes VALUES (1,?)',('fixture-node-and-settings',)); connection.commit()
                if not with_wal: connection.close()
                database = case.root/'etc/daed/wing.db'
                shutil.copyfile(source,database)
                wal = database.with_name('wing.db-wal'); wal.unlink(missing_ok=True)
                if with_wal:
                    shutil.copyfile(str(source)+'-wal',wal); connection.close()
                result = case.install('--install','--update-only')
                self.assertNotEqual(result.returncode,0)
                self.assertIn('previous installation and service state restored',result.stderr)
                with sqlite3.connect('file:'+str(database)+'?mode=ro',uri=True) as restored:
                    self.assertEqual(restored.execute('PRAGMA integrity_check').fetchone()[0],'ok')
                    self.assertEqual(restored.execute('SELECT * FROM nodes').fetchall(),[(1,'fixture-node-and-settings')])
