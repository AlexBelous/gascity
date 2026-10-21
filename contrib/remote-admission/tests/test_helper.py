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
    def test_unit_hook_uses_only_fenced_unit_resources(self):
        self.stage()
        self.a.enter(self.req["attempt"],987)
        self.host.cgroup = lambda row: self.host.peer_value["cgroup"]
        self.host.unit_resources = mock.Mock()
        g.unit_hook(self.a,self.req["attempt"],False)
        self.host.unit_resources.assert_called_once_with()
        self.host.unit_resources.reset_mock()
        g.unit_hook(self.a,self.req["attempt"],True)
        self.host.unit_resources.assert_called_once_with()
        for field,value in (("current",None),("phase","unknown"),("revoked",True),("profile_digest","wrong")):
            with self.subTest(field=field):
                with self.a.locked() as state:
                    original=copy.deepcopy(state)
                    if field=="current":state[field]=value
                    else:state["attempts"][self.req["attempt"]][field]=value
                    g.atomic(self.directory/"ledger.json",state)
                self.host.unit_resources.reset_mock()
                self.refuses(g.unit_hook,self.a,self.req["attempt"],False)
                self.host.unit_resources.assert_not_called()
                g.atomic(self.directory/"ledger.json",original)
        self.host.peer_value["cgroup"]="/other.slice"
        self.host.cgroup=lambda row:"/test.slice/test-"+self.req["attempt"]+".service"
        self.host.unit_resources.reset_mock()
        self.refuses(g.unit_hook,self.a,self.req["attempt"],False)
        self.host.unit_resources.assert_not_called()

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


class UnitWorkspaceTests(unittest.TestCase):
    """Unprivileged filesystem fixture; mountinfo is a controlled kernel snapshot."""
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="hook-work-", dir="/srv/gc-build/home")
        self.work = pathlib.Path(self.tmp.name)
        self.work.chmod(0o700)
        config = configuration(self.work.with_name(self.work.name + "-state"))
        config.update(working_directory=str(self.work), model_uid=os.getuid(), model_gid=os.getgid())
        self.host = g.LinuxHost(g.Profile(config, owner=os.getuid()))
        self.host.p["resources"]["work_max_bytes"] = os.statvfs(self.work).f_blocks * os.statvfs(self.work).f_frsize
        info = self.work.stat()
        self.mount = f"900 1 {os.major(info.st_dev)}:{os.minor(info.st_dev)} / {self.work} rw,nosuid,nodev - tmpfs tmpfs rw\n"
        self.actual_lstat = pathlib.Path.lstat
        self.actual_read = pathlib.Path.read_text

    def tearDown(self):
        self.tmp.cleanup()

    def check(self, mount=None, root_changed=False, action=None, mount_changed=False):
        # The real target/fd is owned by this unprivileged test UID. Only ancestor
        # ownership and the proc mount table model the root-provisioned unit.
        from types import SimpleNamespace
        def lstat(path, *args, **kwargs):
            value = self.actual_lstat(path, *args, **kwargs)
            if path in self.work.parents:
                return SimpleNamespace(st_mode=value.st_mode & ~0o022, st_uid=0)
            return value
        reads=0
        def read(path, *args, **kwargs):
            nonlocal reads
            if str(path) == "/proc/self/mountinfo":
                reads+=1
                if mount_changed and reads>1:
                    return self.mount.replace("900 ", "901 ", 1)
                return self.mount if mount is None else mount
            return self.actual_read(path, *args, **kwargs)
        with mock.patch.object(pathlib.Path, "lstat", lstat), mock.patch.object(pathlib.Path, "read_text", read):
            if root_changed:
                real = os.fstat
                def changed(fd):
                    value = real(fd)
                    return SimpleNamespace(st_dev=value.st_dev, st_ino=value.st_ino + 1, st_uid=value.st_uid, st_gid=value.st_gid, st_mode=value.st_mode)
                with mock.patch.object(g.os, "fstat", changed):
                    (action or self.host.unit_workspace)()
            else:
                (action or self.host.unit_workspace)()

    def test_bounded_model_owned_unit_tmpfs(self):
        self.check()

    def test_wrong_owner_gid_or_mode_refused(self):
        for field in ("model_uid", "model_gid"):
            with self.subTest(field=field):
                previous = self.host.p[field]
                self.host.p[field] += 1
                with self.assertRaises(g.Refusal):
                    self.check()
                self.host.p[field] = previous
        self.work.chmod(0o750)
        with self.assertRaises(g.Refusal):
            self.check()

    def test_ordinary_path_or_wrong_mount_root_refused(self):
        for mount in ("", self.mount.replace(" - tmpfs ", " - ext4 "), self.mount.replace(" / ", " /subdir ", 1), self.mount + self.mount):
            with self.subTest(mount=mount), self.assertRaises(g.Refusal):
                self.check(mount=mount)

    def test_mount_device_and_size_refused(self):
        with self.assertRaises(g.Refusal):
            self.check(mount=self.mount.replace(self.mount.split()[2], "0:999", 1))
        self.host.p["resources"]["work_max_bytes"] -= 1
        with self.assertRaises(g.Refusal):
            self.check()

    def test_changed_opened_root_refused(self):
        with self.assertRaises(g.Refusal):
            self.check(root_changed=True)
        with self.assertRaises(g.Refusal):
            self.check(mount_changed=True)

    def test_missing_safe_mount_options_refused(self):
        for option in ("rw", "nosuid", "nodev"):
            with self.subTest(option=option), self.assertRaises(g.Refusal):
                self.check(mount=self.mount.replace("rw,nosuid,nodev", ",".join(x for x in ("rw","nosuid","nodev") if x!=option)))

    def test_symlink_or_model_owned_ancestor_refused(self):
        alias=self.work.with_name(self.work.name+"-alias")
        alias.symlink_to(self.work)
        self.host.p["working_directory"]=str(alias)
        try:
            with self.assertRaisesRegex(g.Refusal, "symlink"):
                self.check()
        finally:
            alias.rename(self.work/"retained-alias")
            self.host.p["working_directory"]=str(self.work)
        # The actual test home ancestor is non-root-owned; without the modeled
        # root-owned ancestors, an otherwise valid leaf must still be refused.
        with self.assertRaises(g.Refusal):
            self.host.unit_workspace()

    def test_unit_resources_preserves_shared_resource_thresholds(self):
        self.host.p["state_dir"]=str(self.work)
        self.host.p["thresholds"].update(max_memory_psi_full_avg10=100,max_io_psi_full_avg10=100)
        with mock.patch.object(self.host,"home_mount"):
            self.check(action=self.host.unit_resources)
            for field in ("min_mem_available_bytes","min_disk_available_bytes","min_free_inodes"):
                with self.subTest(field=field):
                    original=self.host.p["thresholds"][field]
                    self.host.p["thresholds"][field]=10**30
                    with self.assertRaises(g.Refusal):
                        self.check(action=self.host.unit_resources)
                    self.host.p["thresholds"][field]=original
        with mock.patch.object(self.host,"home_mount",side_effect=g.Refusal("home_proof")):
            with self.assertRaisesRegex(g.Refusal,"home_proof"):
                self.host.unit_resources()

    def test_default_host_resources_still_refuses_model_owned_target(self):
        self.host.p["state_dir"]=str(self.work)
        self.host.p["thresholds"].update(max_memory_psi_full_avg10=100,max_io_psi_full_avg10=100)
        with mock.patch.object(self.host, "home_mount"):
            self.check(action=self.host.unit_resources)
            with self.assertRaisesRegex(g.Refusal, "unsafe_owner_mode"):
                self.check(action=self.host.resources)



class StartupObservationTests(unittest.TestCase):
    setUp = GuardTests.setUp
    tearDown = GuardTests.tearDown
    stage = GuardTests.stage

    def pending(self):
        self.stage()
        self.a.enter(self.req["attempt"],987)
        self.linux=g.LinuxHost(self.profile)
        self.linux.scope=self.host.scope
        self.linux.boot=lambda:self.host.sc["boot_id"]
        self.unit=dict(Id=self.linux.unit({"request":self.req}),LoadState="loaded",
                       ActiveState="activating",SubState="start-pre",MainPID="0",
                       InvocationID="a"*32,ControlGroup=self.linux.cgroup({"request":self.req}),
                       ExecMainCode="0",ExecMainStatus="0",Result="success")
        self.linux.show=lambda unit:copy.deepcopy(self.unit)
        self.host.inspect=self.linux.inspect
        self.host.validate_identity=self.linux.validate_identity
        self.host.cgroup=self.linux.cgroup
        self.checked=[]
        self.host.unit_resources=lambda:self.checked.append("resources")

    def state(self):
        with self.a.locked() as state:
            return copy.deepcopy(state),copy.deepcopy(state["attempts"][self.req["attempt"]])

    def kernel(self, mutate=None):
        from types import SimpleNamespace
        actual=pathlib.Path.stat
        cg="/sys/fs/cgroup"+self.unit["ControlGroup"]
        def info(path,*args,**kwargs):
            if str(path)==cg:
                value=dict(st_dev=29,st_ino=124,st_uid=0,st_mode=0o40755)
                if mutate:mutate(value)
                return SimpleNamespace(**value)
            return actual(path,*args,**kwargs)
        return mock.patch.object(pathlib.Path,"stat",info)

    def test_early_observe_preserves_pending_hook_but_denies_queue(self):
        self.pending()
        with self.kernel():
            self.assertEqual(self.a.observe(self.req)["phase"],"unknown")
            state,row=self.state()
            self.assertEqual(row["phase"],"consumed")
            self.assertFalse(row["attested"])
            self.assertTrue(row["ever_consumed"])
            self.assertEqual(row["startup_identity"]["invocation_id"],"a"*32)
            g.unit_hook(self.a,self.req["attempt"],False)
            self.assertEqual(self.checked,["resources"])
            with self.assertRaisesRegex(g.Refusal,"not_entered"):
                self.a.queue_observe(dict(schema_version=1,profile_id="test",peer=self.host.peer_value))
            self.assertEqual(self.state()[1]["phase"],"consumed")
            self.assertEqual(self.host.started,1)

    def test_forward_progress_second_snapshot_keeps_pending(self):
        self.pending()
        second={**self.unit,"ActiveState":"active","SubState":"running","MainPID":"123"}
        replies=iter((copy.deepcopy(self.unit),second))
        self.linux.show=lambda unit:next(replies)
        with self.kernel():
            self.a.observe(self.req)
        self.assertEqual(self.state()[1]["phase"],"consumed")

    def test_failed_missing_or_foreign_pending_snapshot_holds(self):
        cases=(
            {"ActiveState":"failed","SubState":"failed","Result":"exit-code"},
            {"InvocationID":""},{"ControlGroup":"/other.slice/foreign.service"},
            {"ExecMainCode":"1"},{"LoadState":"not-found"},
        )
        for change in cases:
            with self.subTest(change=change):
                self.pending()
                self.unit.update(change)
                with self.kernel():
                    self.assertEqual(self.a.observe(self.req)["phase"],"unknown")
                self.assertEqual(self.state()[1]["phase"],"unknown")
                with self.assertRaisesRegex(g.Refusal,"hook_denied"):
                    g.unit_hook(self.a,self.req["attempt"],False)
                # Each case owns an independent fresh synthetic ledger.
                self.tearDown();self.setUp()

    def test_second_snapshot_identity_or_failure_drift_holds(self):
        for change in ({"InvocationID":"b"*32},{"ControlGroup":"/other.slice"},
                       {"ActiveState":"failed","SubState":"failed","Result":"exit-code"}):
            with self.subTest(change=change):
                self.pending()
                replies=iter((copy.deepcopy(self.unit),{**self.unit,**change}))
                self.linux.show=lambda unit:next(replies)
                with self.kernel():
                    self.a.observe(self.req)
                self.assertEqual(self.state()[1]["phase"],"unknown")
                self.tearDown();self.setUp()

    def test_foreign_kernel_or_parent_holds(self):
        for which in ("owner","inode","parent"):
            with self.subTest(which=which):
                self.pending()
                def drift(value):
                    if which=="owner":value["st_uid"]=988
                    elif which=="inode":value["st_ino"]=0
                if which=="parent":self.host.sc["inode"]+=1
                with self.kernel(drift):
                    self.a.observe(self.req)
                self.assertEqual(self.state()[1]["phase"],"unknown")
                self.tearDown();self.setUp()

    def test_existing_unknown_or_revoked_never_becomes_pending(self):
        for field,value in (("phase","unknown"),("revoked",True)):
            with self.subTest(field=field):
                self.pending()
                with self.a.locked() as state:
                    state["attempts"][self.req["attempt"]][field]=value
                    g.atomic(self.directory/"ledger.json",state)
                with self.kernel():
                    self.a.observe(self.req)
                self.assertEqual(self.state()[1]["phase"],"unknown")
                with self.assertRaisesRegex(g.Refusal,"hook_denied"):
                    g.unit_hook(self.a,self.req["attempt"],False)
                self.tearDown();self.setUp()

    def test_kernel_or_boot_changes_between_reads_hold(self):
        for which in ("kernel","boot"):
            with self.subTest(which=which):
                self.pending()
                calls=[]
                def changed(value):
                    calls.append(True)
                    if len(calls)>1:value["st_ino"]+=1
                if which=="boot":
                    first=self.host.scope()
                    scopes=iter((first,{**first,"boot_id":"b"*32}))
                    self.linux.scope=lambda:next(scopes)
                with self.kernel(changed if which=="kernel" else None):
                    self.a.observe(self.req)
                self.assertEqual(self.state()[1]["phase"],"unknown")
                self.tearDown();self.setUp()

    def test_startup_pin_drift_denies_hook_before_resources(self):
        self.pending()
        with self.kernel():
            self.a.observe(self.req)
            self.unit["InvocationID"]="b"*32
            with self.assertRaisesRegex(g.Refusal,"startup_changed"):
                g.unit_hook(self.a,self.req["attempt"],False)
        self.assertEqual(self.checked,[])
        self.assertFalse(self.state()[1]["attested"])

    def test_nonconsumed_or_attested_row_cannot_classify_pending(self):
        self.pending()
        _,row=self.state()
        for phase,attested in (("prepared",False),("staged",False),("entered",True)):
            with self.subTest(phase=phase),self.kernel():
                with self.assertRaisesRegex(g.Refusal,"terminal_history"):
                    self.linux.inspect({**row,"phase":phase,"attested":attested})

    def test_entered_must_match_startup_identity(self):
        for changed in (False,True):
            with self.subTest(changed=changed):
                self.pending()
                with self.kernel():self.a.observe(self.req)
                self.host.inspect=lambda row:dict(phase="entered",scope=self.host.scope(),identity={**self.host.identity(row),"invocation_id":("b" if changed else "a")*32})
                receipt=self.a.observe(self.req)
                self.assertEqual(receipt["phase"],"unknown" if changed else "entered")
                self.assertEqual(self.state()[1]["attested"],not changed)
                self.tearDown();self.setUp()

    def test_present_malformed_startup_pin_refuses_without_overwrite(self):
        for malformed in (None,{},0,"",[],False):
            with self.subTest(malformed=malformed):
                try:
                    self.pending()
                    with self.a.locked() as state:
                        state["attempts"][self.req["attempt"]]["startup_identity"]=malformed
                        g.atomic(self.directory/"ledger.json",state)
                    before=(self.directory/"ledger.json").read_bytes()
                    with self.kernel():
                        with self.assertRaisesRegex(g.Refusal,"schema"):
                            self.a.observe(self.req)
                        with self.assertRaisesRegex(g.Refusal,"schema"):
                            g.unit_hook(self.a,self.req["attempt"],False)
                    self.assertEqual((self.directory/"ledger.json").read_bytes(),before)
                    self.assertEqual(self.checked,[])
                finally:
                    self.tearDown();self.setUp()

    def test_native_bridge_early_poll_does_not_poison_pre_hook(self):
        self.pending()
        path=self.directory/(self.req["attempt"]+".output")
        path.write_bytes(b"");path.chmod(0o600)
        calls=[]
        actual_inspect=FakeHost.inspect.__get__(self.host)
        def advance(delay):
            calls.append(delay)
            if len(calls)==1:
                self.assertEqual(self.state()[1]["phase"],"consumed")
                g.unit_hook(self.a,self.req["attempt"],False)
                self.host.inspect=actual_inspect
            elif len(calls)==2:
                self.host.phase="exited";self.host.sc["empty"]=True
            else:self.fail("unexpected bridge retry")
        with self.kernel(),mock.patch.object(g.time,"sleep",advance):
            self.linux.bridge(self.a,self.req)
        self.assertEqual(self.state()[1]["phase"],"exited")
        self.assertEqual(self.host.started,1)
        self.assertEqual(len(calls),2)



class DiagnosticTests(unittest.TestCase):
    setUp = GuardTests.setUp
    tearDown = GuardTests.tearDown
    stage = GuardTests.stage

    def diagnostic(self, leaf):
        return g.decode(g.read_private(self.directory / leaf, os.getuid()))

    def test_first_unknown_is_recorded_without_private_identity(self):
        self.stage();self.a.enter(self.req["attempt"],987)
        self.host.fail_inspect=True
        receipt=self.a.observe(self.req)
        self.assertEqual(receipt["phase"],"unknown")
        raw=(self.directory/"diagnostic-first-unknown.json").read_bytes()
        event=g.decode(raw)
        self.assertEqual(event["reason"],"os_error")
        self.assertEqual((event["phase_before"],event["phase_after"]),("consumed","unknown"))
        for value in (self.req["attempt"],self.req["instance_token"],"partition",self.req["session_id"]):
            self.assertNotIn(value,raw.decode())
        self.assertLessEqual(len(raw),1024)
        self.assertIs(type(event["monotonic_ns"]),int)
        self.assertEqual(event["pid"],os.getpid())

    def test_repeated_unknown_never_overwrites_first_evidence(self):
        self.stage();self.a.enter(self.req["attempt"],987);self.host.fail_inspect=True
        self.a.observe(self.req)
        path=self.directory/"diagnostic-first-unknown.json"
        before=path.read_bytes();inode=path.stat().st_ino
        self.host.fail_inspect=False;self.host.inspect=lambda row:(_ for _ in ()).throw(g.Refusal("pending_state"))
        self.a.observe(self.req)
        self.assertEqual(path.read_bytes(),before);self.assertEqual(path.stat().st_ino,inode)

    def test_start_failure_keeps_original_refusal_and_consumed_hold(self):
        self.stage();self.host.fail_start=True
        with self.assertRaisesRegex(g.Refusal,"start_unknown"):self.a.enter(self.req["attempt"],987)
        event=self.diagnostic("diagnostic-first-unknown.json")
        self.assertEqual(event["reason"],"os_error")
        self.assertEqual(event["phase_before"],"consumed")
        state=g.decode((self.directory/"ledger.json").read_bytes())
        self.assertEqual(state["attempts"][self.req["attempt"]]["phase"],"unknown")

    def test_only_literal_refusal_allowlist_survives(self):
        for kind,error,expected in (("unit-pre",g.Refusal("hook_denied"),"hook_denied"),("unit-post",g.Refusal("PRIVATE_TOKEN arbitrary/path"),"refusal_other"),("unknown-transition",ValueError("PRIVATE_TOKEN"),"value_error")):
            self.assertTrue(g.capture_diagnostic(self.profile,kind,error))
            leaf={"unit-pre":"diagnostic-unit-pre.json","unit-post":"diagnostic-unit-post.json","unknown-transition":"diagnostic-first-unknown.json"}[kind]
            raw=(self.directory/leaf).read_bytes()
            self.assertEqual(g.decode(raw)["reason"],expected);self.assertNotIn(b"PRIVATE_TOKEN",raw)

    def test_nonstring_refusal_args_are_generic_without_formatting(self):
        class Private:
            def __str__(self):raise AssertionError("must not format")
        self.assertTrue(g.capture_diagnostic(self.profile,"unit-pre",g.Refusal(Private())))
        self.assertEqual(self.diagnostic("diagnostic-unit-pre.json")["reason"],"refusal_other")

    def test_fixed_three_files_existing_evidence_never_replaced(self):
        for kind in ("unit-pre","unit-post","unknown-transition"):
            self.assertTrue(g.capture_diagnostic(self.profile,kind,g.Refusal("scope_changed")))
            self.assertFalse(g.capture_diagnostic(self.profile,kind,g.Refusal("hook_denied")))
        leaves=sorted(p.name for p in self.directory.glob("diagnostic-*.json"))
        self.assertEqual(leaves,["diagnostic-first-unknown.json","diagnostic-unit-post.json","diagnostic-unit-pre.json"])
        for path in self.directory.glob("diagnostic-*.json"):
            self.assertEqual(path.stat().st_mode&0o777,0o600);self.assertEqual(path.stat().st_nlink,1)
        self.assertFalse(g.capture_diagnostic(self.profile,"../escape",g.Refusal("hook_denied")))

    def test_existing_fifo_is_not_opened_or_overwritten(self):
        path=self.directory/"diagnostic-unit-pre.json";os.mkfifo(path,0o600)
        inode=path.lstat().st_ino
        self.assertFalse(g.capture_diagnostic(self.profile,"unit-pre",g.Refusal("hook_denied")))
        self.assertEqual(path.lstat().st_ino,inode)

    def test_existing_symlink_and_hardlink_are_preserved(self):
        original=self.directory/"original";original.write_bytes(b"retained");original.chmod(0o600)
        (self.directory/"diagnostic-unit-pre.json").symlink_to(original)
        os.link(original,self.directory/"diagnostic-unit-post.json")
        self.assertFalse(g.capture_diagnostic(self.profile,"unit-pre",g.Refusal("hook_denied")))
        self.assertFalse(g.capture_diagnostic(self.profile,"unit-post",g.Refusal("hook_denied")))
        self.assertEqual(original.read_bytes(),b"retained")

    def test_unsafe_state_directory_refuses_diagnostic_only(self):
        self.directory.chmod(0o755)
        self.assertFalse(g.capture_diagnostic(self.profile,"unit-pre",g.Refusal("hook_denied")))
        self.assertFalse((self.directory/"diagnostic-unit-pre.json").exists())
        self.directory.chmod(0o700)

    def test_diagnostic_open_failure_does_not_replace_original_unknown(self):
        self.stage();self.a.enter(self.req["attempt"],987);self.host.fail_inspect=True
        original=os.open
        def fail_diagnostic(path,*args,**kwargs):
            if str(path).startswith("diagnostic-"):raise OSError("PRIVATE_TOKEN diagnostic denied")
            return original(path,*args,**kwargs)
        with mock.patch.object(g.os,"open",side_effect=fail_diagnostic):
            self.assertEqual(self.a.observe(self.req)["phase"],"unknown")
        state=g.decode((self.directory/"ledger.json").read_bytes())
        self.assertTrue(state["attempts"][self.req["attempt"]]["ever_consumed"])

    def test_existing_native_snapshot_is_sanitized_without_extra_query(self):
        linux=g.LinuxHost(self.profile)
        unit="test-"+self.req["attempt"]+".service"
        values=dict(Id=unit,LoadState="loaded",ActiveState="activating",SubState="start-pre",MainPID="0",InvocationID="a"*32,ControlGroup="/PRIVATE_TOKEN",ExecMainCode="0",ExecMainStatus="0",Result="success")
        linux.command=mock.Mock(return_value="\n".join(k+"="+v for k,v in values.items()))
        linux.show(unit)
        self.assertEqual(linux.command.call_count,1)
        native=linux.diagnostic_state
        self.assertEqual(native["sub_state"],"start-pre")
        self.assertTrue(native["main_pid_zero"])
        self.assertNotIn("PRIVATE_TOKEN",g.canonical(native).decode())
        self.assertNotIn(self.req["attempt"],g.canonical(native).decode())
        self.assertIs(type(native["observed_monotonic_ns"]),int)

    def test_hook_refusal_records_first_reason_and_preserves_original_exception(self):
        self.stage();self.a.enter(self.req["attempt"],987)
        with self.a.locked() as state:
            state["attempts"][self.req["attempt"]]["phase"]="unknown"
            g.atomic(self.directory/"ledger.json",state)
        with self.assertRaisesRegex(g.Refusal,"hook_denied"):
            g.dispatch_command(self.a,["unit-pre",self.req["attempt"]],b"")
        self.assertEqual(self.diagnostic("diagnostic-unit-pre.json")["reason"],"hook_denied")

    def test_cli_public_refusal_remains_generic(self):
        import subprocess
        p=subprocess.run(["/usr/bin/python3.12","-I","-B",str(PATH),"unit-pre","1"*32],capture_output=True,check=False)
        self.assertEqual(p.returncode,1)
        self.assertEqual(p.stdout,b'{"schema_version":1,"error":"refused"}\n')
        self.assertEqual(p.stderr,b"")


    def test_unknown_state_is_saved_before_diagnostic_capture(self):
        self.stage();self.a.enter(self.req["attempt"],987);self.host.fail_inspect=True
        actual=g.capture_diagnostic
        seen=[]
        def capture(*args,**kwargs):
            state=g.decode((self.directory/"ledger.json").read_bytes())
            seen.append(state["attempts"][self.req["attempt"]]["phase"])
            return actual(*args,**kwargs)
        with mock.patch.object(g,"capture_diagnostic",side_effect=capture):
            self.a.observe(self.req)
        self.assertEqual(seen,["unknown"])

    def test_inspect_clears_old_snapshot_before_a_failing_scope_read(self):
        linux=g.LinuxHost(self.profile);linux.diagnostic_state={"sub_state":"PRIVATE_TOKEN"}
        with mock.patch.object(linux,"scope",side_effect=g.Refusal("scope_owner")):
            with self.assertRaisesRegex(g.Refusal,"scope_owner"):linux.inspect({})
        self.assertIsNone(linux.diagnostic_state)

    def test_hook_diagnostic_failure_preserves_original_refusal(self):
        self.stage();self.a.enter(self.req["attempt"],987)
        with self.a.locked() as state:
            state["attempts"][self.req["attempt"]]["phase"]="unknown"
            g.atomic(self.directory/"ledger.json",state)
        original=os.open
        def fail_diagnostic(path,*args,**kwargs):
            if str(path).startswith("diagnostic-"):raise OSError("PRIVATE_TOKEN")
            return original(path,*args,**kwargs)
        with mock.patch.object(g.os,"open",side_effect=fail_diagnostic):
            with self.assertRaisesRegex(g.Refusal,"hook_denied"):
                g.dispatch_command(self.a,["unit-pre",self.req["attempt"]],b"")

    def test_native_unknown_values_and_fields_cannot_escape(self):
        unsafe=dict(sub_state="PRIVATE_TOKEN",unit="PRIVATE_TOKEN",main_pid_zero="PRIVATE_TOKEN",observed_monotonic_ns="PRIVATE_TOKEN")
        self.assertTrue(g.capture_diagnostic(self.profile,"unit-pre",g.Refusal("hook_denied"),native=unsafe))
        raw=(self.directory/"diagnostic-unit-pre.json").read_bytes()
        self.assertNotIn(b"PRIVATE_TOKEN",raw)
        value=g.decode(raw)["native"]
        self.assertEqual(value["sub_state"],"other")
        self.assertIsNone(value["main_pid_zero"])
        self.assertIsNone(value["observed_monotonic_ns"])
        self.assertLessEqual(len(raw),1024)


    def test_native_cache_failure_cannot_replace_original_show_refusal(self):
        linux=g.LinuxHost(self.profile);unit="test-"+self.req["attempt"]+".service"
        values=dict(Id=unit,LoadState="not-found",ActiveState="inactive",SubState="dead",MainPID="0",InvocationID="",ControlGroup="",ExecMainCode="0",ExecMainStatus="0",Result="success")
        linux.command=lambda argv:"\n".join(k+"="+v for k,v in values.items())
        with mock.patch.object(g,"diagnostic_native",side_effect=ValueError("PRIVATE_TOKEN")):
            with self.assertRaisesRegex(g.Refusal,"unit_unknown"):linux.show(unit)

if __name__ == "__main__":unittest.main()
