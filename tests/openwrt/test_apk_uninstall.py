"""Exercise architecture selection, preflight and uninstall using real APK v3."""
import hashlib
import http.server
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import threading
import unittest

import test_router


def install_host_apk(case, apk):
    """Run the SDK's real APK binary inside the isolated musl rootfs."""
    host_lib = apk.parent.parent / "lib"
    loader = host_lib / "ld-linux-x86-64.so.2"
    binary = apk.parent / ".apk.bin"
    libraries = subprocess.check_output([str(loader), "--library-path", str(host_lib), "--list", str(binary)], text=True)
    for item in re.findall(r"=> (/\S+)", libraries):
        case.write("/opt/apk-host/" + Path(item).name, Path(item).read_bytes(), True)
    case.write("/opt/apk-host/ld.so", loader.read_bytes(), True)
    case.write("/opt/apk-host/apk", binary.read_bytes(), True)
    case.script("/bin/apk", 'exec /opt/apk-host/ld.so --library-path /opt/apk-host /opt/apk-host/apk "$@"')


class NativeAPKUninstallTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        location = os.environ.get("DAED_TEST_APK")
        if not location:
            raise unittest.SkipTest("Set DAED_TEST_APK to an x86_64 OpenWrt SDK host/bin/apk")
        test_router.RouterTests.setUpClass()
        cls.apk = Path(location).resolve()

    def test_verified_local_packages_can_restore_an_older_native_version(self):
        case = test_router.RouterTests(); case.setUp()
        try:
            install_host_apk(case,self.apk)
            paths = []
            for version in ('0.1.0-r1','0.2.0-r1'):
                payload = Path(case.temp.name)/('payload-'+version)
                marker = payload/'usr/share/nailink-recovery-test'; marker.parent.mkdir(parents=True)
                marker.write_text(version)
                package = Path(case.temp.name)/(version+'.apk')
                subprocess.run([str(self.apk),'mkpkg','--info','name:nailink-recovery-test','--info','version:'+version,'--info','arch:x86_64','--files',str(payload),'--output',str(package)],check=True,capture_output=True)
                destination = '/fixtures/'+package.name
                case.write(destination,package.read_bytes()); paths.append(destination)
            for package in (paths[1],paths[0]):
                result = case.run_router('apk','add','--initdb','--allow-untrusted',package)
                self.assertEqual(result.returncode,0,result.stdout+result.stderr)
            self.assertEqual((case.root/'usr/share/nailink-recovery-test').read_text(),'0.1.0-r1')
        finally:
            case.tearDown()

    def test_real_apk_uninstall_keeps_orphan_btf_and_backs_up_data(self):
        case = test_router.RouterTests()
        case.setUp()
        try:
            case.legacy()
            install_host_apk(case, self.apk)
            directory = Path(case.temp.name)
            packages = []
            definitions = [
                ("vmlinux-btf", "6.6.73", "", {"usr/lib/debug/boot/vmlinux-6.6.73": b"BTF retention fixture"}),
                ("daed", "1-r1", "vmlinux-btf", {
                    "usr/bin/daed": b"old binary fixture",
                    "etc/init.d/daed": (case.root / "fixtures/daed-init").read_bytes(),
                    "etc/config/daed": b"config daed 'config'\n option enabled '1'\n",
                }),
                ("luci-app-daed", "1-r1", "daed", {"usr/share/old-daed-ui": b"fixture"}),
            ]
            for name, version, depends, files in definitions:
                payload = directory / ("payload-" + name)
                for relative, data in files.items():
                    target = payload / relative
                    target.parent.mkdir(parents=True, exist_ok=True)
                    target.write_bytes(data)
                    target.chmod(0o755)
                package = directory / (name + ".apk")
                args = [str(self.apk), "mkpkg", "--info", "name:" + name, "--info", "version:" + version, "--info", "arch:x86_64", "--files", str(payload), "--output", str(package)]
                if depends:
                    args += ["--info", "depends:" + depends]
                subprocess.run(args, check=True, capture_output=True)
                packages.append(str(package))
            # vmlinux-btf is initially a dependency, not a world entry.
            subprocess.run([str(self.apk), "--root", str(case.root), "add", "--initdb", "--no-scripts", "--allow-untrusted", *packages], check=True, capture_output=True)
            world = case.root / "etc/apk/world"
            world.write_text("luci-app-daed\n")
            names = case.run_router("apk", "info")
            self.assertEqual(names.returncode, 0, names.stderr)
            self.assertIn("daed", names.stdout.splitlines())
            result = case.install("--uninstall")
            self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
            self.assertEqual(case.run_router("apk", "info", "--exists", "vmlinux-btf").returncode, 0)
            self.assertNotEqual(case.run_router("apk", "info", "--exists", "daed").returncode, 0)
            self.assertTrue((case.root / "usr/lib/debug/boot/vmlinux-6.6.73").exists())
            self.assertIn("vmlinux-btf", world.read_text())
            self.assertFalse((case.root / "etc/daed").exists())
            backup = next((case.root / "root/daed-backups").glob("*.tar.gz"))
            with tarfile.open(backup) as archive:
                self.assertEqual(archive.extractfile("etc/daed/wing.db-wal").read(), b"old WAL")
        finally:
            case.tearDown()

    def selection_case(self, depends=""):
        case = test_router.RouterTests()
        case.setUp()
        self.addCleanup(case.tearDown)
        case.write("/tmp/machine", "aarch64\n")
        install_host_apk(case, self.apk)
        directory = Path(case.temp.name)
        payload = directory / "empty-payload"
        payload.mkdir()
        rows = []
        for name, arch, dependency, filename in (
            ("fixture-anchor", "noarch", "", "anchor.apk"),
            ("daed-router", "aarch64", depends, "wrong.apk"),
            ("daed-router", "aarch64_generic", depends, "runtime.apk"),
            ("luci-app-daed-router", "noarch", "daed-router", "luci.apk"),
        ):
            package = directory / filename
            args = [str(self.apk), "mkpkg", "--info", "name:" + name, "--info", "version:0.1.2-r1", "--info", "arch:" + arch, "--files", str(payload), "--output", str(package)]
            if dependency:
                args += ["--info", "depends:" + dependency]
            subprocess.run(args, check=True, capture_output=True)
            case.write("/assets/" + filename, package.read_bytes())
            if name != "fixture-anchor":
                rows.append("\t".join(["apk", "all" if arch == "noarch" else arch, name, filename, hashlib.sha256(package.read_bytes()).hexdigest(), "0.1.2"]))
        subprocess.run([str(self.apk), "--root", str(case.root), "add", "--initdb", "--no-scripts", "--allow-untrusted", str(directory / "anchor.apk")], check=True, capture_output=True)
        case.write("/etc/apk/arch", "aarch64_generic\n")
        # Keep the real checker plus an explicit BTF/parser fixture. The test
        # exercises APK architecture/solver behavior without executing ARM code.
        for role, arch, filename, data in (
            ("check", "all", "daed-check", (test_router.OPENWRT / "files/daed-check").read_bytes()),
            ("btf-probe", "arm64", "btf-probe-arm64", (case.root / "fixtures/btf-probe").read_bytes()),
        ):
            case.write("/assets/" + filename, data)
            rows.append("\t".join(["support", arch, role, filename, hashlib.sha256(data).hexdigest(), "0.1.2"]))
        case.write("/assets/manifest.tsv", "# daed-openwrt-manifest-v1\n" + "\n".join(rows) + "\n")
        case.legacy()
        return case

    def test_real_apk_rejects_default_arch_and_installer_selects_database_arch(self):
        case = self.selection_case()
        self.assertEqual(case.run_router("apk", "--print-arch").stdout.strip(), "x86_64")
        world = (case.root / "etc/apk/world").read_bytes()
        wrong = case.run_router("apk", "add", "--simulate", "--no-network", "--allow-untrusted", "/assets/wrong.apk", "/assets/luci.apk")
        self.assertNotEqual(wrong.returncode, 0)
        self.assertIn("uninstallable", wrong.stderr)
        result = case.install("--reinstall", "--dry-run")
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertIn("aarch64 / apk / aarch64_generic", result.stdout)
        self.assertIn("Installing daed-router", result.stdout)
        self.assertEqual((case.root / "etc/apk/world").read_bytes(), world)
        self.assertNotEqual(case.run_router("apk", "info", "--exists", "daed-router").returncode, 0)
        self.assertTrue((case.root / "tmp/daemon-running").exists())
        self.assertEqual((case.root / "etc/daed/wing.db-wal").read_text(), "old WAL")

    def test_real_apk_missing_dependency_stops_reinstall_before_service_or_data_changes(self):
        case = self.selection_case("fixture-unavailable-kmod=999")
        result = case.install("--reinstall")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Package preflight failed", result.stderr)
        self.assertIn("fixture-unavailable-kmod", result.stderr)
        self.assertTrue((case.root / "tmp/daemon-running").exists())
        self.assertEqual((case.root / "etc/daed/wing.db-wal").read_text(), "old WAL")
        self.assertFalse((case.root / "root/daed-backups").exists())

    def test_real_apk_cached_http_index_can_select_uncached_dependency_without_mutations(self):
        case = self.selection_case("fixture-remote-kmod")
        repository = Path(case.temp.name) / "http-repository"
        payload = repository / "payload"
        payload.mkdir(parents=True)
        (payload / "should-not-install").write_text("remote dependency payload")
        package = repository / "fixture-remote-kmod-1.apk"
        subprocess.run([str(self.apk), "mkpkg", "--info", "name:fixture-remote-kmod", "--info", "version:1", "--info", "arch:aarch64_generic", "--files", str(payload), "--output", str(package)], check=True, capture_output=True)
        subprocess.run([str(self.apk), "mkndx", "--allow-untrusted", "--output", str(repository / "packages.adb"), str(package)], check=True, capture_output=True)

        requests = []

        class Handler(http.server.SimpleHTTPRequestHandler):
            def __init__(self, *args, **kwargs):
                super().__init__(*args, directory=str(repository), **kwargs)

            def do_GET(self):
                requests.append(self.path)
                super().do_GET()

            def log_message(self, *args):
                pass

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        try:
            case.write("/etc/apk/repositories", "http://127.0.0.1:" + str(server.server_port) + "/packages.adb\n")
            # Fetch only the index, as on a router just after `apk update`.
            subprocess.run([str(self.apk), "--root", str(case.root), "update", "--allow-untrusted"], check=True, capture_output=True)
            self.assertEqual(requests, ["/packages.adb"])
            world = (case.root / "etc/apk/world").read_bytes()
            # This is the previous release's command, with an actual HTTP repo.
            offline = case.run_router("apk", "add", "--simulate", "--no-network", "--allow-untrusted", "/assets/runtime.apk", "/assets/luci.apk")
            self.assertNotEqual(offline.returncode, 0)
            self.assertIn("masked in: --no-network", offline.stderr)
            result = case.install("--reinstall", "--dry-run")
            self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
            self.assertIn("Installing fixture-remote-kmod", result.stdout)
            # Simulation reads the cached index without fetching a package or
            # refreshing the index, installing files or modifying world/service.
            self.assertEqual(requests, ["/packages.adb"])
            self.assertEqual((case.root / "etc/apk/world").read_bytes(), world)
            self.assertFalse((case.root / "should-not-install").exists())
            self.assertNotEqual(case.run_router("apk", "info", "--exists", "fixture-remote-kmod").returncode, 0)
            self.assertTrue((case.root / "tmp/daemon-running").exists())
            self.assertEqual((case.root / "etc/daed/wing.db-wal").read_text(), "old WAL")
        finally:
            server.shutdown()
            worker.join()
            server.server_close()
