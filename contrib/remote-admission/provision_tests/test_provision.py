import copy
import importlib.util
import pathlib
import os
import tempfile
from unittest import mock
from types import SimpleNamespace
import unittest

PATH=pathlib.Path(__file__).resolve().parents[1]/"provision_bounded_home.py"

def module():
    spec=importlib.util.spec_from_file_location("provision",PATH)
    value=importlib.util.module_from_spec(spec);spec.loader.exec_module(value)
    return value

def manifest():
    return dict(schema_version=1,base_dir="/srv/gc-repairer",model_uid=988,model_gid=982,transport_uid=987,build_uid=990,home="/srv/gc-repairer/worker",image="/srv/gc-repairer/volumes/worker-home-2g.ext4",image_bytes=2147483648,archive="/srv/gc-repairer/Archive/worker-home-20261020T200000Z",staging="/srv/gc-repairer/home-staging-20261020T200000Z",state_dir="/srv/gc-repairer/home-provision-20261020T200000Z",unit_name="srv-gc\\x2drepairer-worker.mount",auth_relative=".codex/auth.json",home_device=1,home_inode=2,auth_device=1,auth_inode=3,reserve_bytes=1073741824,source_sha="a"*40,script_sha256="b"*64,freeze_receipt="/srv/gc-repairer/operator-freeze.json",freeze_receipt_sha256="c"*64)

class FakeHost:
    def __init__(self):self.effects=[];self.fail=None;self.processes=False;self.auth_ok=True
    def preflight(self,m):
        if self.processes:raise RuntimeError("UID occupied")
        return dict(home_device=m["home_device"],home_inode=m["home_inode"],auth_equal=True)
    def perform(self,name,m):
        self.effects.append(name)
        if self.fail==name or not self.auth_ok:raise RuntimeError("unknown")
        return {"completed":name}
    def allocate(self,m):return self.perform("allocate",m)
    def copy_verify(self,m):return self.perform("copy_verify",m)
    def cutover(self,m):return self.perform("cutover",m)
    def install_verify(self,m):return self.perform("install_verify",m)

class FakeJournal:
    def __init__(self):self.records=[]
    def exists(self):return bool(self.records)
    def write(self,value):self.records.append(copy.deepcopy(value))

class ProvisionTests(unittest.TestCase):
    def setUp(self):self.g=module();self.m=manifest();self.host=FakeHost();self.journal=FakeJournal()
    def test_default_plan_has_no_effect(self):
        plan=self.g.plan(self.m,self.host)
        self.assertEqual(self.host.effects,[])
        self.assertEqual(plan["status"],"plan")
        self.assertEqual(self.g.IMAGE_BYTES,2*1024**3)
    def test_manifest_rejects_unapproved_paths_and_size(self):
        for key,value in (("home","/srv/ci-runtime/a"),("image","/srv/ci-images/x"),("image_bytes",1),("archive","/srv/gc-repairer/worker"),("auth_relative","../secret")):
            m=copy.deepcopy(self.m);m[key]=value
            with self.assertRaises(self.g.Refusal):self.g.validate(m)
    def test_no_uid_processes_gate(self):
        self.host.processes=True
        with self.assertRaises(Exception):self.g.plan(self.m,self.host)
        self.assertEqual(self.host.effects,[])
    def test_apply_journals_before_each_effect(self):
        result=self.g.apply(self.m,self.host,self.journal)
        self.assertEqual(result["status"],"complete")
        self.assertEqual(self.host.effects,["allocate","copy_verify","cutover","install_verify"])
        self.assertEqual([r["phase"] for r in self.journal.records],["intent","allocate_intent","allocate_done","copy_verify_intent","copy_verify_done","cutover_intent","cutover_done","install_verify_intent","install_verify_done","complete"])
    def test_partial_state_refuses_recreation(self):
        self.journal.records.append(dict(phase="allocate_intent"))
        with self.assertRaises(self.g.Refusal):self.g.apply(self.m,self.host,self.journal)
        self.assertEqual(self.host.effects,[])
    def test_failure_retains_unknown_no_rollback(self):
        self.host.fail="cutover"
        with self.assertRaises(self.g.Refusal):self.g.apply(self.m,self.host,self.journal)
        self.assertEqual(self.host.effects,["allocate","copy_verify","cutover"])
        self.assertEqual(self.journal.records[-1]["status"],"unknown")
        with self.assertRaises(self.g.Refusal):self.g.apply(self.m,self.host,self.journal)
        self.assertEqual(self.host.effects.count("allocate"),1)
    def test_auth_failure_never_cutovers(self):
        self.host.fail="copy_verify"
        with self.assertRaises(self.g.Refusal):self.g.apply(self.m,self.host,self.journal)
        self.assertNotIn("cutover",self.host.effects)
    def test_no_auth_material_in_public_plan(self):
        import json
        raw=json.dumps(self.g.plan(self.m,self.host))
        self.assertNotIn("auth_hash",raw)
        self.assertNotIn("auth_bytes",raw)

class ProductionBoundaryTests(unittest.TestCase):
    def setUp(self):
        self.g=module()
        self.temp=tempfile.TemporaryDirectory(prefix="home-boundary-",dir="/srv/gc-build/home")
        self.root=pathlib.Path(self.temp.name);self.root.chmod(0o700)
        self.m=manifest();self.m.update(model_uid=os.getuid(),model_gid=os.getgid())
        for name in ("left","right"):
            home=self.root/name;home.mkdir(mode=0o700)
            (home/".codex").mkdir(mode=0o700)
            auth=home/".codex/auth.json";auth.write_bytes(b"synthetic fixture data");auth.chmod(0o600)
    def tearDown(self):self.temp.cleanup()
    def test_auth_compare_real_files_no_value_or_hash_output(self):
        import io,contextlib
        output=io.StringIO()
        with contextlib.redirect_stdout(output):self.g.compare_auth(self.root/"left",self.root/"right",self.m)
        self.assertEqual(output.getvalue(),"")
    def test_auth_symlink_refused(self):
        auth=self.root/"left/.codex/auth.json";auth.rename(auth.with_name("retained"));auth.symlink_to("retained")
        with self.assertRaises((self.g.Refusal,OSError)):self.g.auth_fd(self.root/"left",self.m)
    def test_auth_hardlink_refused(self):
        auth=self.root/"left/.codex/auth.json";os.link(auth,self.root/"extra")
        with self.assertRaises(self.g.Refusal):self.g.auth_fd(self.root/"left",self.m)
    def test_auth_ancestor_symlink_refused(self):
        home=self.root/"left";(home/".codex").rename(home/"retained");(home/".codex").symlink_to("retained",target_is_directory=True)
        with self.assertRaises((self.g.Refusal,OSError)):self.g.auth_fd(home,self.m)
    def test_auth_changed_during_compare_refused(self):
        original=self.g.os.read;changed=False
        def read(fd,size):
            nonlocal changed
            raw=original(fd,size)
            if not changed:
                changed=True
                auth=self.root/"left/.codex/auth.json"
                info=auth.stat();os.utime(auth,ns=(info.st_atime_ns,info.st_mtime_ns+1000000))
            return raw
        with mock.patch.object(self.g.os,"read",side_effect=read):
            with self.assertRaises(self.g.Refusal):self.g.compare_auth(self.root/"left",self.root/"right",self.m)
    def test_auth_bytes_different_refused(self):
        (self.root/"right/.codex/auth.json").write_bytes(b"different fixture")
        with self.assertRaises(self.g.Refusal):self.g.compare_auth(self.root/"left",self.root/"right",self.m)
    def test_journal_real_private_fsync_and_existing_detection(self):
        journal=self.g.Journal(self.root/"state",owner=os.getuid())
        self.assertFalse(journal.exists())
        journal.write({"phase":"intent"});self.assertTrue(journal.exists())
        journal.write({"phase":"allocate_intent"})
        import json,stat
        self.assertEqual(json.loads((journal.path/"receipt.json").read_text())["phase"],"allocate_intent")
        self.assertEqual(stat.S_IMODE((journal.path/"receipt.json").stat().st_mode),0o600)
    def test_partial_journal_dir_refuses_apply_no_effects(self):
        state=self.root/"state";state.mkdir(mode=0o700)
        host=FakeHost()
        with self.assertRaises(self.g.Refusal):self.g.apply(manifest(),host,self.g.Journal(state,owner=os.getuid()))
        self.assertEqual(host.effects,[])
    def test_archive_rename_never_overwrites_real_existing_dir(self):
        a=self.root/"a";b=self.root/"b";a.mkdir();b.mkdir()
        with self.assertRaises(OSError):self.g.rename_new(a,b)
        self.assertTrue(a.exists());self.assertTrue(b.exists())
    def test_fresh_file_no_overwrite_or_symlink(self):
        target=self.root/"existing";target.write_bytes(b"retained")
        with self.assertRaises(FileExistsError):self.g.fresh_file(target,b"new",owner=os.getuid())
        self.assertEqual(target.read_bytes(),b"retained")
        link=self.root/"link";link.symlink_to(target)
        with self.assertRaises(FileExistsError):self.g.fresh_file(link,b"new",owner=os.getuid())
    def test_sparse_image_refused(self):
        image=self.root/"image";image.write_bytes(b"")
        with image.open("r+b") as file:file.truncate(self.g.IMAGE_BYTES)
        m=manifest();m["image"]=str(image)
        host=self.g.Host()
        with mock.patch.object(self.g,"chain",return_value=image.stat()):
            with self.assertRaises(self.g.Refusal):host.image(m)
    def test_image_rechecked_after_mkfs(self):
        host=self.g.Host();calls=[]
        host.check_source=lambda m:None
        host.image=mock.Mock(side_effect=[SimpleNamespace(st_dev=1,st_ino=2,st_size=self.g.IMAGE_BYTES),self.g.Refusal()])
        host.command=lambda argv:calls.append(argv) or ""
        class Parent:
            def exists(self):return True
        class Base:
            def __truediv__(self,name):return Parent()
        with mock.patch.object(self.g.pathlib.Path,"exists",return_value=True),mock.patch.object(self.g,"chain"),mock.patch.object(self.g,"fresh_file"):
            with self.assertRaises(self.g.Refusal):host.allocate(manifest())
        self.assertEqual(host.image.call_count,2)
        mkfs=next(argv for argv in calls if argv[0].endswith("mkfs.ext4"))
        self.assertIn("nodiscard,lazy_itable_init=0,lazy_journal_init=0",mkfs)
    def test_wrong_mount_or_loop_backing_refused(self):
        host=self.g.Host();m=manifest()
        host.mounts=lambda:[(["1","0","7:1","/","/target"],["xfs","/dev/loop1","rw"])]
        with self.assertRaises(self.g.Refusal):host.verify_mount("/target",m)
        host.mounts=lambda:[(["1","0","7:1","/","/target"],["ext4","/dev/loop1","rw"])]
        with mock.patch.object(self.g.pathlib.Path,"read_text",return_value="/other/image"):
            with self.assertRaises(self.g.Refusal):host.verify_mount("/target",m)
    def test_still_mounted_staging_refuses_cutover(self):
        host=self.g.Host();m=manifest();host.check_source=lambda m:None;host.image=lambda m:None
        host.mounts=lambda:[(["1","0","7:1","/",m["staging"]],["ext4","/dev/loop1","rw"])]
        with mock.patch.object(self.g,"rename_new") as rename:
            with self.assertRaises(self.g.Refusal):host.cutover(m)
            rename.assert_not_called()

    def test_existing_unit_or_dropin_or_wantslink_refuses(self):
        host=self.g.Host();m=manifest()
        paths=["/etc/systemd/system/"+m["unit_name"],"/etc/systemd/system/"+m["unit_name"]+".d","/etc/systemd/system/local-fs.target.wants/"+m["unit_name"]]
        for blocked in paths:
            with self.subTest(path=blocked),mock.patch.object(self.g.os.path,"lexists",side_effect=lambda path:str(path)==blocked),mock.patch.object(host,"command") as command:
                with self.assertRaises(self.g.Refusal):host.new_unit(m)
                command.assert_not_called()
    def test_existing_generated_unit_refuses(self):
        host=self.g.Host();m=manifest()
        host.command=mock.Mock(side_effect=[m["unit_name"],"LoadState=loaded\nFragmentPath=/run/generated.mount\nDropInPaths="])
        with mock.patch.object(self.g.os.path,"lexists",return_value=False):
            with self.assertRaises(self.g.Refusal):host.new_unit(m)
    def test_freeze_receipt_metadata_and_bytes_pin(self):
        import json,hashlib,uuid
        m=copy.deepcopy(self.m);m["freeze_receipt"]=str(self.root/"freeze")
        receipt=dict(schema_version=1,model_uid=m["model_uid"],boot_id=uuid.UUID(pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip()).hex,controller_hold_reference="synthetic test freeze",all_uid_launchers_held=True)
        raw=json.dumps(receipt).encode();path=pathlib.Path(m["freeze_receipt"]);path.write_bytes(raw);path.chmod(0o600)
        m["freeze_receipt_sha256"]=hashlib.sha256(raw).hexdigest();host=self.g.Host()
        original=self.g.chain
        def fixture_chain(path,owner=0,private=False,ancestor_owner=0):return original(path,owner=os.getuid(),private=private,ancestor_owner=os.getuid())
        with mock.patch.object(self.g,"chain",side_effect=fixture_chain):
            host.freeze(m)
            info=path.stat();os.utime(path,ns=(info.st_atime_ns,info.st_mtime_ns+1000000))
            with self.assertRaises(self.g.Refusal):host.freeze(m)
        path.write_bytes(b"changed receipt")
        with mock.patch.object(self.g,"chain",side_effect=fixture_chain):
            with self.assertRaises(self.g.Refusal):self.g.Host().freeze(m)
    def test_manifest_principals_and_ci_scope(self):
        for key,value in (("model_uid",990),("model_uid",987),("model_uid",0),("base_dir","/srv/ci-runtime")):
            m=manifest();m[key]=value
            with self.assertRaises(self.g.Refusal):self.g.validate(m)

    def test_consistent_ci_build_base_never_accepted(self):
        for base in ("/srv/gc-build","/srv/ci-runtime","/srv/ci-images"):
            m=manifest()
            for key,value in list(m.items()):
                if type(value) is str:m[key]=value.replace("/srv/gc-repairer",base)
            with self.subTest(base=base),self.assertRaises(self.g.Refusal):self.g.validate(m)

if __name__=="__main__":unittest.main()
