import copy
import importlib.util
import os
import pathlib
import tempfile
import threading
import unittest
from unittest import mock

PATH = pathlib.Path(__file__).resolve().parents[1] / "helper.py"
spec = importlib.util.spec_from_file_location("guard", PATH)
g = importlib.util.module_from_spec(spec)
spec.loader.exec_module(g)


def configuration(directory):
    return dict(schema_version=1, max_consumed_attempts=2, profile_id="test", city="city", template="role", binding=dict(selector="ssh", endpoint="test", resource_domain="test", policy_digest="a"*64), source_sha="a"*40, manifest_sha256="b"*64, transport_uid=987, model_uid=988, model_gid=982, unit_prefix="test", cgroup_parent="/test.slice", state_dir=str(directory), helper_path="/opt/immutable/helper.py", helper_sha256="c"*64, model_argv=["/opt/immutable/model", "exec", "--model", "test"], model_exe_sha256="d"*64, environment={"PATH":"/usr/bin:/bin","HOME":"/var/lib/test-model"}, environment_keys=["LANG"], resources=dict(cpu_quota_percent=1, memory_max_bytes=1, memory_swap_max_bytes=0, tasks_max=1, runtime_max_seconds=1,output_max_bytes=1048576,tmp_max_bytes=1048576,work_max_bytes=1048576), thresholds=dict(min_mem_available_bytes=1,max_memory_psi_full_avg10=0,max_io_psi_full_avg10=0,min_disk_available_bytes=1,min_free_inodes=1), disk_path="/srv", model_home="/var/lib/test-model", transport_home="/var/lib/test-transport", working_directory="/var/lib/test-work",home_mount=dict(source="/dev/loop0",device_major=7,device_minor=0,max_bytes=1048576,backing_file="/var/lib/test.img",backing_device=1,backing_inode=2))


class FakeHost:
    def __init__(self, profile):
        self.p=profile.data
        self.sc=dict(boot_id="a"*32, device=29, inode=123, empty=True)
        self.phase="entered"
        self.started=0
        self.killed=0
        self.fail_resources=False
        self.fail_start=False
        self.fail_inspect=False
        self.changed_identity=False
        self.peer_value=dict(pid=10,uid=988,gid=982,start="12",cgroup="/test.slice/test-"+"1"*32+".service")
    def resources(self):
        g.require(not self.fail_resources,"resources")
    def scope(self):
        return copy.deepcopy(self.sc)
    def start(self,row):
        self.started+=1
        self.sc["empty"]=False
        if self.fail_start:
            raise OSError("ambiguous")
    def identity(self,row):
        return dict(boot_id=self.sc["boot_id"],unit="test-"+row["request"]["attempt"]+".service",invocation_id=("a" if not self.changed_identity else "b")*32,cgroup_path="/test.slice/test-"+row["request"]["attempt"]+".service",cgroup_device=29,cgroup_inode=124,source_sha=self.p["source_sha"],manifest_sha256=self.p["manifest_sha256"],model_uid=988,transport_uid=987)
    def inspect(self,row):
        if self.fail_inspect:
            raise OSError("partition")
        return dict(scope=self.scope(),phase=self.phase,identity=self.identity(row))
    def validate_identity(self,row,identity):
        g.require(identity == self.identity(row),"identity")
    def cancel(self,row):
        self.killed+=1
        self.phase="exited"
        self.sc["empty"]=True
    def peer(self,pid):
        return copy.deepcopy(self.peer_value)


class GuardTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory(prefix="fixture-", dir="/srv/gc-build/home")
        self.directory=pathlib.Path(self.tmp.name)
        self.directory.chmod(0o700)
        self.config=configuration(self.directory)
        self.profile=g.Profile(self.config,owner=os.getuid())
        self.host=FakeHost(self.profile)
        g.atomic(self.directory/"ledger.json",dict(schema_version=1,current=None,attempts={}))
        self.a=g.Authority(self.profile,self.host)
        self.req=dict(city="city",session_id="sid",template="role",runtime_handle="runtime",generation="1",instance_token="private-incarnation",attempt="1"*32,binding=self.config["binding"])
    def tearDown(self):
        self.tmp.cleanup()
    def stage(self):
        self.a.prepare(self.req)
        self.a.stage_prompt(self.req,"literal $(never shell)",{"LANG":"C"})
    def entered(self):
        self.stage()
        self.a.enter(self.req["attempt"],987)
        self.assertEqual(self.a.observe(self.req)["phase"],"entered")
    def refuses(self,fn,*args):
        with self.assertRaises(g.Refusal): fn(*args)
    def test_parser(self):
        for raw in (b'{"x":1,"x":2}', b'{"x":NaN}', b'[]', b'{}x', bytes([255]), b'x'*(g.MAX_FRAME+1)):
            self.refuses(g.decode,raw)
    def test_profile_no_resource_defaults(self):
        for field in ("resources","thresholds","transport_uid","helper_sha256"):
            value=copy.deepcopy(self.config);value.pop(field)
            self.refuses(g.Profile,value)
    def test_principals_and_binding(self):
        value=copy.deepcopy(self.config);value["model_uid"]=987
        self.refuses(g.Profile,value)
        for key,val in (("generation","01"),("generation",1),("attempt","../x"),("binding",{})):
            req=copy.deepcopy(self.req);req[key]=val
            self.refuses(self.a.prepare,req)
    def test_private_files(self):
        path=self.directory/"profile"
        path.write_bytes(b'{}');path.chmod(0o600)
        self.assertEqual(g.read_private(path,os.getuid()),b'{}')
        path.chmod(0o644);self.refuses(g.read_private,path,os.getuid())
        path.chmod(0o600)
        alias=self.directory/"alias";alias.symlink_to(path)
        self.refuses(g.read_private,alias,os.getuid())
        hard=self.directory/"hard";os.link(path,hard)
        self.refuses(g.read_private,path,os.getuid())
    def test_prepare_idempotent_cap_one(self):
        receipt=self.a.prepare(self.req)
        self.assertEqual(receipt,self.a.prepare(self.req))
        req=copy.deepcopy(self.req);req["attempt"]="2"*32
        self.refuses(self.a.prepare,req)
        req=copy.deepcopy(self.req);req["instance_token"]="other"
        self.refuses(self.a.prepare,req)
    def test_prepare_race(self):
        results=[]
        def worker(attempt):
            req=copy.deepcopy(self.req);req["attempt"]=attempt*32
            try: self.a.prepare(req);results.append("ok")
            except g.Refusal: results.append("denied")
        threads=[threading.Thread(target=worker,args=(x,)) for x in ("1","2","3","4")]
        for thread in threads: thread.start()
        for thread in threads: thread.join()
        self.assertEqual(sorted(results),["denied"]*3+["ok"])
    def test_prompt_literal_and_immutable(self):
        self.stage()
        self.assertEqual((self.directory/(self.req["attempt"]+".prompt")).read_text(),"literal $(never shell)")
        self.a.stage_prompt(self.req,"literal $(never shell)",{"LANG":"C"})
        self.refuses(self.a.stage_prompt,self.req,"changed",{})
        self.refuses(self.a.stage_prompt,self.req,"literal",{"LD_PRELOAD":"x"})
    def test_enter_single_use_and_principal(self):
        self.stage()
        self.refuses(self.a.enter,self.req["attempt"],988)
        self.a.enter(self.req["attempt"],987)
        self.refuses(self.a.enter,self.req["attempt"],987)
        self.assertEqual(self.host.started,1)
    def test_entry_race_only_one_start(self):
        self.stage();results=[]
        def worker():
            try:self.a.enter(self.req["attempt"],987);results.append("ok")
            except g.Refusal:results.append("denied")
        threads=[threading.Thread(target=worker) for _ in range(8)]
        for t in threads:t.start()
        for t in threads:t.join()
        self.assertEqual(results.count("ok"),1)
        self.assertEqual(self.host.started,1)
    def test_enter_prompt_tamper(self):
        self.stage();g.atomic(self.directory/(self.req["attempt"]+".prompt"),b"tamper")
        self.refuses(self.a.enter,self.req["attempt"],987)
        self.assertEqual(self.host.started,0)
    def test_resources_rechecked_at_entry(self):
        self.stage();self.host.fail_resources=True
        self.refuses(self.a.enter,self.req["attempt"],987)
        self.assertEqual(self.host.started,0)
    def test_start_ambiguous_consumes_and_holds(self):
        self.stage();self.host.fail_start=True
        self.refuses(self.a.enter,self.req["attempt"],987)
        self.refuses(self.a.enter,self.req["attempt"],987)
        self.host.fail_inspect=True
        self.assertEqual(self.a.observe(self.req)["phase"],"unknown")
        req=copy.deepcopy(self.req);req["attempt"]="2"*32
        self.refuses(self.a.prepare,req)
    def test_entered_receipt(self):
        self.entered()
        receipt=self.a.observe(self.req)
        self.assertEqual(receipt["identity"]["model_uid"],988)
        self.assertEqual(receipt["request"],self.req)
    def test_reboot_and_scope_inode_hold(self):
        self.entered()
        for field,value in (("boot_id","other"),("inode",999)):
            with self.subTest(field=field):
                # Restore original proof before independently injecting each change.
                self.host.sc.update(boot_id="a"*32,inode=123)
                if not (self.directory/"ledger.json").exists():self.entered()
                self.host.sc[field]=value
                self.assertEqual(self.a.observe(self.req)["phase"],"unknown")
    def test_partial_and_changed_identity_hold(self):
        self.entered();self.host.changed_identity=True
        self.assertEqual(self.a.observe(self.req)["phase"],"unknown")
        self.host.fail_inspect=True
        self.assertEqual(self.a.observe(self.req)["phase"],"unknown")
    def test_terminal_requires_prior_entry_and_recursive_empty(self):
        self.stage();self.a.enter(self.req["attempt"],987)
        self.host.phase="exited";self.host.sc["empty"]=True
        self.assertEqual(self.a.observe(self.req)["phase"],"unknown")
    def test_terminal_nonempty_hold_then_empty_releases(self):
        self.entered();self.host.phase="exited"
        self.assertEqual(self.a.observe(self.req)["phase"],"unknown")
        self.host.sc["empty"]=True
        self.assertEqual(self.a.observe(self.req)["phase"],"exited")
        req=copy.deepcopy(self.req);req["attempt"]="2"*32
        self.assertEqual(self.a.prepare(req)["phase"],"prepared")
    def test_cancel_before_enter_revokes_first(self):
        self.stage();self.assertEqual(self.a.cancel(self.req)["phase"],"exited")
        self.refuses(self.a.enter,self.req["attempt"],987)
        self.assertEqual(self.host.started,0)
    def test_cancel_active_uses_authority(self):
        self.entered();self.assertEqual(self.a.cancel(self.req)["phase"],"exited")
        self.assertEqual(self.host.killed,1)
    def test_cancel_ambiguous_holds(self):
        self.entered();self.host.cancel=lambda row: (_ for _ in ()).throw(OSError())
        self.assertEqual(self.a.cancel(self.req)["phase"],"unknown")
    def test_corrupt_journal_denies(self):
        g.atomic(self.directory/"ledger.json",b'{broken')
        self.refuses(self.a.prepare,self.req)
    def test_queue_observe_derives_current(self):
        self.entered()
        frame=dict(schema_version=1,profile_id="test",peer=self.host.peer_value)
        out=self.a.queue_observe(frame)
        self.assertEqual(out["identity"]["sid"],"sid")
        self.assertEqual(out["observe"]["phase"],"entered")
        frame["expected_identity"]=out["identity"]
        self.a.queue_observe(frame)
        frame["expected_identity"]={}
        self.refuses(self.a.queue_observe,frame)
    def test_queue_peer_required_and_stale(self):
        self.entered()
        self.refuses(self.a.queue_observe,dict(schema_version=1,profile_id="test"))
        for key,value in (("pid",11),("start","13"),("cgroup","/other"),("uid",987)):
            peer=copy.deepcopy(self.host.peer_value);peer[key]=value
            self.refuses(self.a.queue_observe,dict(schema_version=1,profile_id="test",peer=peer))
    def test_queue_unknown_no_ttl_release(self):
        self.entered();self.host.fail_inspect=True
        self.refuses(self.a.queue_observe,dict(schema_version=1,profile_id="test",peer=self.host.peer_value))
        req=copy.deepcopy(self.req);req["attempt"]="2"*32
        self.refuses(self.a.prepare,req)
    def test_linux_command_has_fixed_boundaries(self):
        linux=g.LinuxHost(self.profile)
        linux.pin=lambda *args:None
        calls=[];linux.command=lambda argv:calls.append(argv)
        self.stage()
        with self.a.locked() as state:row=copy.deepcopy(state["attempts"][self.req["attempt"]])
        linux.start(row)
        argv=calls[0]
        self.assertIn("--service-type=exec",argv)
        self.assertIn("--property=User=988",argv)
        self.assertIn("--property=Slice=test.slice",argv)
        self.assertIn("--property=NoNewPrivileges=yes",argv)
        self.assertIn("--property=Delegate=no",argv)
        self.assertEqual(argv[-len(self.config["model_argv"]):],self.config["model_argv"])
        self.assertNotIn("literal $(never shell)"," ".join(argv))
        self.assertNotIn(self.req["instance_token"]," ".join(argv))

    def test_queue_profile_change_denied(self):
        self.entered()
        config=copy.deepcopy(self.config)
        config["binding"]["policy_digest"]="e"*64
        profile=g.Profile(config,owner=os.getuid())
        other=g.Authority(profile,self.host)
        self.refuses(other.queue_observe,dict(schema_version=1,profile_id="test",peer=self.host.peer_value))
    def test_ambiguous_current_ledger_denied(self):
        self.entered()
        with self.a.locked() as state:
            state["attempts"]["2"*32]=copy.deepcopy(state["attempts"][self.req["attempt"]])
            g.atomic(self.directory/"ledger.json",state)
        self.refuses(self.a.observe,self.req)
    def test_missing_scope_does_not_free(self):
        self.entered()
        self.host.scope=lambda: (_ for _ in ()).throw(OSError())
        self.assertEqual(self.a.observe(self.req)["phase"],"unknown")
    def test_entry_write_failure_never_starts(self):
        self.stage()
        with mock.patch.object(g,"atomic",side_effect=OSError()):
            with self.assertRaises(OSError):self.a.enter(self.req["attempt"],987)
        self.assertEqual(self.host.started,0)
    def test_ambiguous_fsync_retains_consumed(self):
        self.stage()
        original=g.atomic
        def ambiguous(path,value):
            original(path,value)
            raise OSError("directory fsync uncertain")
        with mock.patch.object(g,"atomic",side_effect=ambiguous):
            with self.assertRaises(OSError):self.a.enter(self.req["attempt"],987)
        self.assertEqual(self.host.started,0)
        self.refuses(self.a.enter,self.req["attempt"],987)
    def test_prepared_identity_profile_only(self):
        ident=self.a.prepare(self.req)["identity"]
        self.assertEqual(ident["boot_id"],"a"*32)
        self.assertEqual(ident["source_sha"],self.config["source_sha"])
        self.assertEqual(ident["unit"],"")
        self.assertEqual(ident["cgroup_inode"],0)
    def test_explicit_disk_budgets(self):
        for key in ("output_max_bytes","tmp_max_bytes","work_max_bytes"):
            config=copy.deepcopy(self.config);config["resources"].pop(key)
            self.refuses(g.Profile,config)
        config=copy.deepcopy(self.config);config.pop("home_mount")
        self.refuses(g.Profile,config)
    def test_model_transport_paths_separate(self):
        config=copy.deepcopy(self.config);config["model_home"]=config["transport_home"]
        config["environment"]["HOME"]=config["model_home"]
        self.refuses(g.Profile,config)
    def test_profile_rejects_nonfinite_and_boolean_version(self):
        config=copy.deepcopy(self.config);config["schema_version"]=True
        self.refuses(g.Profile,config)
        config=copy.deepcopy(self.config);config["thresholds"]["min_free_inodes"]=float("inf")
        self.refuses(g.Profile,config)
    def test_kernel_peer_parser_own_process(self):
        # Read-only proc test runs unprivileged; no service/model/root helper execution.
        linux=g.LinuxHost(self.profile)
        peer=linux.peer(os.getpid())
        self.assertEqual(peer["pid"],os.getpid())
        self.assertEqual(peer["uid"],os.getuid())
        self.assertTrue(peer["start"].isdigit())
    def test_linux_boot_normalization(self):
        self.assertRegex(g.LinuxHost(self.profile).boot(),r"^[0-9a-f]{32}$")
    def test_command_storage_limits(self):
        linux=g.LinuxHost(self.profile);linux.pin=lambda *args:None
        calls=[];linux.command=lambda argv:calls.append(argv)
        self.stage()
        with self.a.locked() as state:row=copy.deepcopy(state["attempts"][self.req["attempt"]])
        linux.start(row)
        argv=calls[0]
        self.assertIn("--property=LimitFSIZE=1048576",argv)
        self.assertIn("--property=ReadWritePaths="+str(self.directory)+" /var/lib/test-model",argv)
        self.assertIn("--property=WorkingDirectory=/var/lib/test-work",argv)
        tmpfs=next(x for x in argv if x.startswith("--property=TemporaryFileSystem="))
        self.assertIn("/tmp:rw,nosuid,nodev,size=1048576",tmpfs)
        self.assertIn("/var/lib/test-work:rw,nosuid,nodev,size=1048576",tmpfs)

    def test_tmpfs_permissions_and_oom_policy(self):
        linux=g.LinuxHost(self.profile);linux.pin=lambda *args:None
        calls=[];linux.command=lambda argv:calls.append(argv)
        self.stage()
        with self.a.locked() as state:row=copy.deepcopy(state["attempts"][self.req["attempt"]])
        linux.start(row)
        argv=calls[0]
        tmpfs=next(x for x in argv if x.startswith("--property=TemporaryFileSystem="))
        self.assertIn("/tmp:rw,nosuid,nodev,size=1048576,mode=1777",tmpfs)
        self.assertIn("/var/tmp:rw,nosuid,nodev,size=1048576,mode=1777",tmpfs)
        self.assertIn("mode=0700,uid=988,gid=982",tmpfs)
        self.assertIn("--property=OOMPolicy=kill",argv)
    def test_queue_observe_rfc3339(self):
        import datetime
        self.entered()
        out=self.a.queue_observe(dict(schema_version=1,profile_id="test",peer=self.host.peer_value))
        timestamp=out["observe"]["observed_at"]
        self.assertTrue(timestamp.endswith("Z"))
        self.assertIsNotNone(datetime.datetime.fromisoformat(timestamp.replace("Z","+00:00")).tzinfo)
    def test_no_effect_cancel_fresh_scope_required(self):
        self.stage();self.host.sc["empty"]=False
        self.refuses(self.a.cancel,self.req)
        self.refuses(self.a.enter,self.req["attempt"],987)
    def test_staged_prompt_cannot_change_instance(self):
        self.stage();req=copy.deepcopy(self.req);req["instance_token"]="changed"
        self.refuses(self.a.stage_prompt,req,"literal",{})
    def test_root_cli_absent_profile_and_no_bypass(self):
        self.assertFalse(pathlib.Path(g.PROFILE).exists())
        self.refuses(g.run,["rpc"],b'{}')
    def test_no_private_tuple_in_unit_command(self):
        linux=g.LinuxHost(self.profile);linux.pin=lambda *args:None
        calls=[];linux.command=lambda argv:calls.append(argv)
        self.stage()
        with self.a.locked() as state:row=copy.deepcopy(state["attempts"][self.req["attempt"]])
        linux.start(row)
        raw=" ".join(calls[0])
        self.assertNotIn(self.req["instance_token"],raw)
        self.assertNotIn(self.req["session_id"],raw)
        self.assertNotIn("literal $(never shell)",raw)

    def test_actual_wire_has_explicit_scope_proof(self):
        frame=dict(schema_version=1,op="prepare",request=self.req)
        prepared=g.dispatch(self.a,frame)
        self.assertEqual(set(prepared),{"schema_version","receipt","scope_empty","never_entered","scope_identity"})
        self.assertFalse(prepared["scope_empty"])
        self.assertEqual(prepared["scope_identity"],dict(boot_id="a"*32,device=29,inode=123,cgroup_path="/test.slice"))
        g.dispatch(self.a,dict(schema_version=1,op="stage_prompt",request=self.req,prompt="literal",environment={}))
        self.a.enter(self.req["attempt"],987)
        entered=g.dispatch(self.a,dict(schema_version=1,op="observe",request=self.req))
        self.assertEqual(entered["receipt"]["phase"],"entered")
        self.assertFalse(entered["scope_empty"])
        self.host.phase="exited";self.host.sc["empty"]=True
        exited=g.dispatch(self.a,dict(schema_version=1,op="observe",request=self.req))
        self.assertEqual(exited["receipt"]["phase"],"exited")
        self.assertTrue(exited["scope_empty"])
        self.host.sc["empty"]=False
        later=g.dispatch(self.a,dict(schema_version=1,op="observe",request=self.req))
        self.assertFalse(later["scope_empty"])
    def test_actual_wire_strict_keys(self):
        for frame in (dict(schema_version=1,op="exec",request=self.req),dict(schema_version=1,op="prepare",request=self.req,command="x"),dict(schema_version=True,op="prepare",request=self.req)):
            self.refuses(g.dispatch,self.a,frame)
    def test_bridge_final_drain_race(self):
        import io
        self.entered()
        path=self.directory/(self.req["attempt"]+".output")
        path.write_bytes(b"");path.chmod(0o600)
        linux=g.LinuxHost(self.profile)
        class ExitAuthority:
            def observe(inner,request):
                with path.open("ab") as output:output.write(b"last chunk")
                return {"phase":"exited"}
        class Output:
            buffer=io.BytesIO()
        with mock.patch.object(g.sys,"stdout",Output):
            linux.bridge(ExitAuthority(),self.req)
            self.assertEqual(Output.buffer.getvalue(),b"last chunk")

    def test_cancel_consumed_start_race_attests_before_signal(self):
        self.stage();self.a.enter(self.req["attempt"],987)
        receipt=self.a.cancel(self.req)
        self.assertEqual(receipt["phase"],"exited")
        self.assertEqual(self.host.killed,1)
    def test_cancel_changed_boot_never_signals(self):
        self.entered();self.host.sc["boot_id"]="b"*32
        self.assertEqual(self.a.cancel(self.req)["phase"],"unknown")
        self.assertEqual(self.host.killed,0)
    def test_cancel_changed_invocation_never_signals(self):
        self.entered();self.host.changed_identity=True
        self.assertEqual(self.a.cancel(self.req)["phase"],"unknown")
        self.assertEqual(self.host.killed,0)

    def test_never_entered_explicit_before_consumption_only(self):
        self.stage()
        response=g.dispatch(self.a,dict(schema_version=1,op="cancel",request=self.req))
        self.assertTrue(response["never_entered"])
        self.assertTrue(response["scope_empty"])
        self.assertEqual(response["receipt"]["identity"]["unit"],"")
        self.assertEqual(response["scope_identity"]["cgroup_path"],"/test.slice")
    def test_never_entered_false_after_consumption(self):
        self.entered()
        response=g.dispatch(self.a,dict(schema_version=1,op="cancel",request=self.req))
        self.assertEqual(response["receipt"]["phase"],"exited")
        self.assertTrue(response["scope_empty"])
        self.assertFalse(response["never_entered"])
    def test_json_rejects_utf16_and_overflow_float(self):
        for raw in ('{"x":1}'.encode("utf-16"),b'{"x":1e999}'):
            self.refuses(g.decode,raw)
    def test_queue_revision_is_canonical_decimal_text(self):
        self.entered()
        out=self.a.queue_observe(dict(schema_version=1,profile_id="test",peer=self.host.peer_value))
        self.assertIs(type(out["observe"]["revision"]),str)
        self.assertRegex(out["observe"]["revision"],r"^[1-9][0-9]*$")

    def test_staged_environment_tamper_refused_before_start(self):
        self.stage()
        with self.a.locked() as state:
            row=state["attempts"][self.req["attempt"]]
            row["environment"]["LANG"]="changed"
            self.a.save(state,row)
        self.refuses(self.a.enter,self.req["attempt"],987)
        self.assertEqual(self.host.started,0)
    def test_staged_environment_injection_refused(self):
        self.stage()
        with self.a.locked() as state:
            row=state["attempts"][self.req["attempt"]]
            row["environment"]["LD_PRELOAD"]="/model/library"
            self.a.save(state,row)
        self.refuses(self.a.enter,self.req["attempt"],987)
        self.assertEqual(self.host.started,0)

    def test_private_devices_ipc_and_bounded_shm_properties(self):
        linux=g.LinuxHost(self.profile);linux.pin=lambda *args:None
        calls=[];linux.command=lambda argv:calls.append(argv)
        self.stage()
        with self.a.locked() as state:row=copy.deepcopy(state["attempts"][self.req["attempt"]])
        linux.start(row)
        argv=calls[0]
        self.assertIn("--property=PrivateDevices=yes",argv)
        self.assertIn("--property=PrivateIPC=yes",argv)
        tmpfs=next(x for x in argv if x.startswith("--property=TemporaryFileSystem="))
        self.assertIn("/dev/shm:rw,nosuid,nodev,noexec,size=1048576,mode=1777",tmpfs)

    def test_lost_consumed_ledger_never_becomes_fresh_authority(self):
        self.entered()
        # Preserve unknown history in an archive; absence must never bootstrap a fresh seat.
        (self.directory/"ledger.json").rename(self.directory/"lost-ledger-retained.json")
        self.host.sc["empty"]=True
        request=copy.deepcopy(self.req);request["attempt"]="2"*32
        self.refuses(self.a.prepare,request)

    def test_private_kernel_namespaces_rejects_host_sharing(self):
        from types import SimpleNamespace
        linux=g.LinuxHost(self.profile)
        def fake(path):
            if path.endswith("ns/ipc"):return SimpleNamespace(st_dev=1,st_ino=2)
            return SimpleNamespace(st_dev=3,st_ino=4)
        with mock.patch.object(g.os,"stat",side_effect=fake):
            self.refuses(linux.private_namespaces,123)
    def test_private_kernel_namespace_distinct_proof(self):
        from types import SimpleNamespace
        linux=g.LinuxHost(self.profile)
        def fake(path):
            if path.endswith("ns/ipc"):return SimpleNamespace(st_dev=1,st_ino=2 if "/1/" in path else 3)
            return SimpleNamespace(st_dev=4 if "/1/" in path else 5,st_ino=6)
        with mock.patch.object(g.os,"stat",side_effect=fake):linux.private_namespaces(123)
    def test_private_device_mount_shared_rejected(self):
        from types import SimpleNamespace
        linux=g.LinuxHost(self.profile)
        def fake(path):
            if path.endswith("ns/ipc"):return SimpleNamespace(st_dev=1,st_ino=2 if "/1/" in path else 3)
            return SimpleNamespace(st_dev=4,st_ino=6)
        with mock.patch.object(g.os,"stat",side_effect=fake):self.refuses(linux.private_namespaces,123)
    def test_fresh_authority_requires_operator_provisioned_ledger(self):
        (self.directory/"ledger.json").rename(self.directory/"operator-empty-retained.json")
        self.refuses(self.a.prepare,self.req)

    def test_total_budget_profile_requires_explicit_limit(self):
        config=copy.deepcopy(self.config)
        config.pop("max_consumed_attempts",None)
        self.refuses(g.Profile,config)

    def budget(self,limit):
        self.config["max_consumed_attempts"]=limit
        self.profile=g.Profile(self.config,owner=os.getuid())
        self.host=FakeHost(self.profile)
        self.a=g.Authority(self.profile,self.host)
    def finish_first(self):
        self.entered();self.host.phase="exited";self.host.sc["empty"]=True
        self.assertEqual(self.a.observe(self.req)["phase"],"exited")
    def test_total_budget_one_refuses_second_after_terminal(self):
        self.budget(1);self.finish_first()
        req=copy.deepcopy(self.req);req["attempt"]="2"*32
        self.refuses(self.a.prepare,req)
        self.assertEqual(self.host.started,1)
    def test_total_budget_preconsume_cancel_does_not_charge(self):
        self.budget(1);self.stage();self.a.cancel(self.req)
        req=copy.deepcopy(self.req);req["attempt"]="2"*32
        self.a.prepare(req);self.a.stage_prompt(req,"second",{})
        self.a.enter(req["attempt"],987)
        self.assertEqual(self.host.started,1)
    def test_total_budget_enter_rechecks_retained_history(self):
        self.budget(1);self.stage()
        # Simulate a retained historical consumed record appearing before entry.
        with self.a.locked() as state:
            row=copy.deepcopy(state["attempts"][self.req["attempt"]])
            row["request"]["attempt"]="2"*32
            row.update(phase="exited",ever_consumed=True,attested=True,never_entered=False)
            state["attempts"]["2"*32]=row
            g.atomic(self.directory/"ledger.json",state)
        self.refuses(self.a.enter,self.req["attempt"],987)
        self.assertEqual(self.host.started,0)
    def test_total_budget_two_allows_explicit_second(self):
        self.budget(2);self.finish_first()
        req=copy.deepcopy(self.req);req["attempt"]="2"*32
        self.a.prepare(req);self.a.stage_prompt(req,"second",{})
        self.a.enter(req["attempt"],987)
        self.assertEqual(self.host.started,2)
    def test_total_budget_same_consumed_never_reenters(self):
        self.budget(1);self.finish_first()
        receipt=self.a.prepare(self.req)
        self.assertEqual(receipt["phase"],"exited")
        self.refuses(self.a.enter,self.req["attempt"],987)
        self.assertEqual(self.host.started,1)
    def test_total_budget_profile_rejects_boolean_zero_negative(self):
        for limit in (True,0,-1,"1"):
            config=copy.deepcopy(self.config);config["max_consumed_attempts"]=limit
            self.refuses(g.Profile,config)
    def test_total_budget_missing_malformed_history_refuses(self):
        self.budget(1);self.finish_first()
        for malformed in (None,False,"true"):
            with self.a.locked() as state:
                row=state["attempts"][self.req["attempt"]]
                if malformed is None:row.pop("ever_consumed")
                else:row["ever_consumed"]=malformed
                g.atomic(self.directory/"ledger.json",state)
            req=copy.deepcopy(self.req);req["attempt"]="2"*32
            self.refuses(self.a.prepare,req)
            # Restore exact positive history directly as an operator fixture only.
            state["attempts"][self.req["attempt"]]["ever_consumed"]=True
            g.atomic(self.directory/"ledger.json",state)

if __name__ == "__main__":unittest.main()
