import contextlib
import json
import pathlib
import tempfile
import threading
import unittest
from unittest import mock
from types import SimpleNamespace
from test_finite_canary import m
import test_finite_canary as base

class ActionFixture:
    def __init__(self):
        self.tmp=tempfile.TemporaryDirectory()
        self.root=pathlib.Path(self.tmp.name)
        (self.root/"lock").write_bytes(b"")
        self.c=base.StateGuards().canary("entered")
        self.c.profile=mock.Mock(data={"profile_id":"test"})
        self.c.request=dict(generation="2",instance_token="private",attempt="a"*32,session_id="sid")
        self.c.context["fixture_unit_sha256"]=m.sha(b"unit")
        self.peer=dict(pid=42,uid=988,gid=982,start="10",cgroup="/model")
        self.c.authority.host.peer.return_value=self.peer
        self.c.authority.queue_observe.return_value={"identity":dict(self.c.request,sid="sid")}
        self.before=dict(state="inactive",pid=0,invocation="",cgroup="")
        self.active=dict(state="active",pid=43,invocation="b"*32,start="11",cgroup="/fixture")
        self.effects=0
        self.seen=[]
        self.stack=contextlib.ExitStack()
    def __enter__(self):
        original=m.os.fstat
        def fstat(fd):
            info=original(fd)
            return SimpleNamespace(st_mode=0o100600,st_uid=0,st_nlink=1) if info.st_size==0 else info
        for patch in (mock.patch.object(m,"ACTION",self.root),mock.patch.object(m,"check_directory"),
                      mock.patch.object(m.os,"fstat",side_effect=fstat),
                      mock.patch.object(m,"read",side_effect=lambda p:b"context" if pathlib.Path(p).name=="context.json" else pathlib.Path(p).read_bytes()),
                      mock.patch.object(m,"protected_unit",return_value=b"unit"),
                      mock.patch.object(m,"fixture_empty",return_value=True,create=True),
                      mock.patch.object(m,"effective_unit",create=True),mock.patch.object(m.time,"sleep"),
                      mock.patch.object(m,"fixture_view",side_effect=self.view),
                      mock.patch.object(m.subprocess,"run",side_effect=self.effect)):
            self.stack.enter_context(patch)
        return self
    def __exit__(self,*args):
        self.stack.close();self.tmp.cleanup()
    def view(self,*args):
        return dict(self.active if self.effects else self.before)
    def effect(self,*args,**kwargs):
        # This observation occurs INSIDE the actual effect callback, not after action().
        intent=json.loads((self.root/"intent.json").read_bytes())
        if intent["before"]["state"]!="inactive":
            raise AssertionError("effect preceded intent")
        self.effects+=1
        self.seen.append(intent)
    def restart(self):
        return self.c.action(self.peer,"restart")

class R2ActionTests(unittest.TestCase):
    def test_fresh_proof_inside_lock_and_immediately_before_effect(self):
        with ActionFixture() as f:
            f.restart()
            self.assertGreaterEqual(f.c.authority.queue_observe.call_count,3)
            self.assertGreaterEqual(f.c.authority.host.peer.call_count,3)
            self.assertEqual(f.effects,1)

    def test_lockwait_peer_drift_no_effect(self):
        with ActionFixture() as f:
            calls=[0]
            def flock(*args):
                calls[0]+=1
                f.c.authority.host.peer.return_value=dict(f.peer,start="new")
            with mock.patch.object(m.fcntl,"flock",side_effect=flock):
                with self.assertRaises(m.Refusal):
                    f.restart()
            self.assertEqual(f.effects,0)
            self.assertFalse((f.root/"intent.json").exists())

    def test_action_intent_fsync_failure_no_effect(self):
        with ActionFixture() as f, mock.patch.object(m.os,"fsync",side_effect=OSError("disk")):
            with self.assertRaises(OSError):
                f.restart()
            self.assertEqual(f.effects,0)
            self.assertTrue((f.root/"intent.json").exists())

    def test_initial_scope_not_empty_no_effect(self):
        with ActionFixture() as f, mock.patch.object(m,"fixture_empty",return_value=False):
            with self.assertRaises(m.Refusal):
                f.restart()
            self.assertEqual(f.effects,0)

    def test_orphan_active_no_effect(self):
        with ActionFixture() as f:
            (f.root/"active.json").write_bytes(b"orphan")
            with self.assertRaises(m.Refusal):
                f.restart()
            self.assertEqual(f.effects,0)

    def test_lost_effect_reply_no_replay(self):
        with ActionFixture() as f:
            original=f.effect
            def lost(*args,**kwargs):
                original(*args,**kwargs)
                raise TimeoutError()
            with mock.patch.object(m.subprocess,"run",side_effect=lost):
                with self.assertRaises(TimeoutError):
                    f.restart()
                with self.assertRaises(m.Refusal):
                    f.restart()
            self.assertEqual(f.effects,1)

    def test_postcheck_failure_no_replay(self):
        with ActionFixture() as f:
            f.active["state"]="failed"
            with self.assertRaises(m.Refusal):
                f.restart()
            with self.assertRaises(m.Refusal):
                f.restart()
            self.assertEqual(f.effects,1)

    def test_two_concurrent_calls_one_effect(self):
        with ActionFixture() as f:
            results=[]
            def call():
                try:results.append(f.restart())
                except m.Refusal:results.append("hold")
            threads=[threading.Thread(target=call) for _ in range(2)]
            for thread in threads:thread.start()
            for thread in threads:thread.join(2);self.assertFalse(thread.is_alive())
            self.assertEqual(f.effects,1)
            self.assertEqual(len(results),2)
            self.assertIn("hold",results)

    def test_fixed_status_returns_actual_safe_diagnosis(self):
        with ActionFixture() as f:
            result=f.c.action(f.peer,"status")
            self.assertEqual(result,dict(entered=True,unit=m.UNIT,state="inactive",main_pid=0,invocation_id="",result=None))
            self.assertEqual(f.effects,0)

class R2EvidenceTests(unittest.TestCase):
    def test_receipt_backed_status_survives_native_gc_with_fresh_parent(self):
        c=base.StateGuards().canary("exited")
        c.g=SimpleNamespace(decode=json.loads)
        row=dict(phase="exited",attested=True,revoked=False,identity={"unit":"model.service","invocation_id":"a"*32},
                 empty_scope={"boot_id":"b"*32,"device":29,"inode":12,"empty":True})
        c.row=lambda:row
        c.authority.host.scope.return_value=row["empty_scope"]
        receipt=dict(schema_version=1,context_sha256=m.sha(b"context"),identity=row["identity"],
                     empty_scope=row["empty_scope"],model_native={"MainPID":"0","InvocationID":"a"*32,
                     "ExecMainCode":"1","ExecMainStatus":"0","Result":"success"},
                     action={"sensor":"recovered","intent":{"context_sha256":m.sha(b"context"),"before":{"state":"inactive","pid":0}},
                             "active":{"context_sha256":m.sha(b"context"),"proof":{"invocation":"c"*32,"boot":"b"*32,"cgroup":"/fixture","started":"10"}},
                             "terminal":{"invocation":"c"*32,"boot":"b"*32,"cgroup":"","started":"10","exited":"90000010","state":"inactive","result":"success","pid":0,"code":"1","status":"0"}})
        with mock.patch.object(m,"read",side_effect=lambda p:b"context" if pathlib.Path(p).name=="context.json" else m.encoded(receipt)), \
             mock.patch.object(m.os.path,"lexists",return_value=True),mock.patch.object(m,"durable_read",return_value=m.encoded(receipt),create=True),mock.patch.object(m,"fixture_empty",return_value=True):
            self.assertEqual(c.status()["state"],"resolved")
            c.authority.host.show.assert_not_called()
            c.authority.host.scope.return_value=dict(row["empty_scope"],inode=13)
            with self.assertRaises(m.Refusal):
                c.status()

class R2EnvelopeAndClientTests(unittest.TestCase):
    def test_effective_unit_rejects_loaded_overrides_or_hooks(self):
        empty=("DropInPaths","ExecCondition","ExecStartPre","ExecStartPost","ExecStop","ExecStopPost","ExecReload")
        view={k:"" for k in empty}
        view.update(Transient="no",NeedDaemonReload="no",Type="exec",Slice="system.slice",
                    DynamicUser="yes",Restart="no",RemainAfterExit="no",
                    ExecStart="{ path=/usr/bin/sleep ; argv[]=/usr/bin/sleep 90 ; ignore_errors=no ; pid=0 ; code=(null) ; status=0/0 }")
        m.validate_effective(view)
        for key,value in [*( (k,"foreign") for k in empty),("NeedDaemonReload","yes"),("Transient","yes"),
                          ("ExecStart",view["ExecStart"]+" { path=/usr/bin/true ; argv[]=/usr/bin/true ; }")]:
            with self.subTest(key=key),self.assertRaises(m.Refusal):
                m.validate_effective(dict(view,**{key:value}))

    def test_client_rejects_wrong_target_active_and_malformed_before_restart(self):
        import importlib.util
        spec=importlib.util.spec_from_file_location("action_client_r2",m.pathlib.Path(m.__file__).with_name("finite_action_client.py"))
        client=importlib.util.module_from_spec(spec);spec.loader.exec_module(client)
        good=dict(entered=True,unit=m.UNIT,state="inactive",main_pid=0,invocation_id="",result="success")
        for value in (dict(good,unit="other.service"),dict(good,state="active",main_pid=42),{"entered":True}):
            with self.subTest(value=value),mock.patch.object(client.sys,"argv",["client","restart"]), \
                 mock.patch.object(client,"request",return_value=value) as request, \
                 mock.patch.object(client.time,"monotonic",side_effect=[0,0,31]),mock.patch.object(client.time,"sleep"):
                self.assertEqual(client.main(),1)
                self.assertTrue(request.call_args_list)
                self.assertTrue(all(call.args==("status",) for call in request.call_args_list))

    def test_terminal_payload_fsync_failure_no_resolved_record(self):
        with tempfile.TemporaryDirectory() as directory,mock.patch.object(m,"check_directory"):
            path=pathlib.Path(directory)/"terminal.json"
            with mock.patch.object(m.os,"fsync",side_effect=OSError("disk")):
                with self.assertRaises(OSError):
                    m.exclusive(path,{"evidence":"not-yet-durable"})
            self.assertTrue(path.exists())
            # Uncertain sink never permits overwriting an existing evidence record.
            with self.assertRaises(FileExistsError):
                m.exclusive(path,{"evidence":"replacement"})
