"""Offline contract tests. No actual CLI, SSH, queue or model is invoked."""
import json
import os
import tempfile
import unittest
from pathlib import Path

from coordinator import Hold, NativeCLI, NativeQueue, OncePermit, Session, SSHTransport, coordinate, bounded_process

SID = "gcg-session-fixture208"
TOKEN = "private-memory-only-token"
FIXTURE = "gc-fixture208"

class FakeCLI:
    def __init__(self):
        self.calls = []
        self.comments = []
        self.status = "in_progress"
        self.outcome = ""
        self.metadata = {}
        self.editor_sid = SID
        self.generation = "1"
        self.session_state = "awake"
        self.idle_reason = "no_work"
        self.change_generation_on_idle = False
        self.discovery_ids = [FIXTURE]
        self.claimed_id = FIXTURE
        self.lease_id = "fixture-lease"
        self.lease_stale = False
        self.close_lost = False
        self.new_work_after_close = False
        self.ack_failure = False
        self.current = FIXTURE
        self.corrupt_accepted = False

    def __call__(self, tool, args, payload=None, allowed=(0,)):
        self.calls.append((tool, tuple(args), payload))
        if tool == "lease":
            if args[0] == "claim":
                return 0, json.dumps({"bd":FIXTURE,"lease_id":self.lease_id}).encode()
            if args[0] == "status":
                return 0, json.dumps({"leases":[{"bd":FIXTURE,"lease_id":self.lease_id,"session_id":self.editor_sid,"holder_host":os.uname().nodename.split(".")[0],"stale":self.lease_stale}]}).encode()
            return 0, b'{}' 
        if args == ["hook", "--json"]:
            return (0 if self.discovery_ids else 1), json.dumps([{"id":identity} for identity in self.discovery_ids]).encode()
        if args[:2] == ["hook", "--claim"]:
            if self.status == "closed":
                if self.new_work_after_close: return 0, b'{"action":"work","bead_id":"gc-unexpected"}'
                if self.change_generation_on_idle: self.generation="2"
                return 1, json.dumps({"action":"drain","reason":self.idle_reason}).encode()
            return 0, json.dumps({"action":"work", "reason":"claimed", "bead_id":self.claimed_id, "assignee":SID}).encode()
        if args[:2] == ["hook", "current"]:
            return 0, self.current.encode()
        if args[:2] == ["bd", "show"]:
            if args[2] == SID:
                row = {"id":SID,"status":"open","metadata":{"state":self.session_state,"template":"repairer-coordinator","instance_token":TOKEN,"generation":self.generation}}
            else:
                row = {"id":FIXTURE,"status":self.status,"assignee":SID,"metadata":{"gc.session_id":SID,"gc.work_outcome":self.outcome,**self.metadata},"comments":[{"text":x} for x in self.comments]}
            return 0, json.dumps([row]).encode()
        if args[:2] == ["bd", "comment"]:
            self.comments.append("corrupted" if self.corrupt_accepted else payload.decode())
        elif args[:2] == ["bd", "update"]:
            for value in args:
                if value.startswith("gc.work_") and "=" in value:
                    key,val=value.split("=",1);self.metadata[key]=val
                if value.startswith("repairer.accepted_sha256="):
                    self.metadata["repairer.accepted_sha256"]=value.split("=",1)[1]
                if value.startswith("gc.work_outcome="):
                    self.outcome=value.split("=",1)[1]
        elif args[:2] == ["bd", "close"]:
            self.status="closed"
            self.current=""
            if self.close_lost: raise Hold("close_reply_lost")
        if args[:2]==["runtime","drain-ack"] and self.ack_failure: return 1,b"{}"
        return 0, b'{}'

class FakeTransport:
    def __init__(self, ready=True, unknown=False):
        self.ready=ready
        self.unknown=unknown
        self.runs=0
        self.accepted=None
    def await_ready(self, accepted):
        self.accepted=accepted
        if not self.ready:
            raise Hold("profile_not_ready")
    def run_once(self):
        self.runs += 1
        if self.unknown:
            raise Hold("run_unknown")
        return {"state":"resolved","evidence_sha256":"a"*64,"summary":"fixture diagnosis verified; no target mutation"}

class CoordinatorTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory()
        self.root=Path(self.temp.name)
        self.cli=FakeCLI()
        self.session=Session(SID,TOKEN,"repairer-coordinator")
        self.artifact={"commit":"4"*40,"branch":"codex/repairer-fixture208","work_dir":"/cloud/installed-source"}
        self.queue=NativeQueue(self.cli,self.session,FIXTURE,self.artifact)
        self.accepted={"incident_id":FIXTURE,"owner_session":SID,"executor_host":"vm328699","target_host":"vm328699","resource":"fixture.service","scope":"read-only fixture diagnosis","source_sha":"4"*40,"sop_sha256":"b"*64,"next_checkpoint":"profile readiness within 60 seconds"}
    def tearDown(self):
        self.temp.cleanup()
    def invoke(self, transport):
        return coordinate(self.queue,OncePermit(self.root),transport,self.accepted)
    def test_complete_cycle_closes_fixture_and_drains_after_typed_result(self):
        transport=FakeTransport()
        self.assertEqual(self.invoke(transport),"resolved")
        self.assertEqual(transport.runs,1)
        self.assertEqual(self.cli.status,"closed")
        self.assertEqual(self.cli.outcome,"shipped")
        self.assertEqual(self.cli.calls[-1][1],("runtime","drain-ack","--json"))
    def test_restart_cannot_run_model_again(self):
        first=FakeTransport()
        self.invoke(first)
        second=FakeTransport()
        with self.assertRaises(Hold): self.invoke(second)
        self.assertEqual(second.runs,0)
    def test_timeout_consumes_permit_and_does_not_close(self):
        transport=FakeTransport(unknown=True)
        with self.assertRaises(Hold): self.invoke(transport)
        self.assertEqual(transport.runs,1)
        self.assertEqual(self.cli.status,"in_progress")
        with self.assertRaises(Hold): self.invoke(FakeTransport())
    def test_other_current_claim_prevents_accepted_and_vm_run(self):
        self.cli.current="gc-other"
        transport=FakeTransport()
        with self.assertRaises(Hold): self.invoke(transport)
        self.assertEqual(transport.runs,0)
        self.assertEqual(self.cli.comments,[])
    def test_accepted_must_be_read_back_before_transport(self):
        self.cli.corrupt_accepted=True
        transport=FakeTransport()
        with self.assertRaises(Hold): self.invoke(transport)
        self.assertEqual(transport.runs,0)
        self.assertIsNone(transport.accepted)
    def test_no_profile_means_no_run_or_close(self):
        transport=FakeTransport(ready=False)
        with self.assertRaises(Hold): self.invoke(transport)
        self.assertEqual(transport.runs,0)
        self.assertEqual(self.cli.status,"in_progress")
    def test_session_token_never_enters_accepted_or_transport(self):
        transport=FakeTransport()
        self.invoke(transport)
        self.assertNotIn(TOKEN,json.dumps(transport.accepted))
        self.assertNotIn(TOKEN,"".join(self.cli.comments))
        self.assertNotIn(TOKEN,(self.root/"once.json").read_text())
    def test_writes_are_preceded_by_editor_lease_check(self):
        self.invoke(FakeTransport())
        for index,(tool,args,payload) in enumerate(self.cli.calls):
            if tool=="gc" and args[:2] in (("bd","comment"),("bd","update"),("bd","close")):
                self.assertEqual(self.cli.calls[index-1][0],"lease")
                self.assertEqual(self.cli.calls[index-1][1],("status",))
    def test_failopen_editor_identity_prevents_claim_and_vm(self):
        self.cli.editor_sid=""
        transport=FakeTransport()
        with self.assertRaises(Hold): self.invoke(transport)
        self.assertEqual(transport.runs,0)
        self.assertFalse(any(tool=="gc" and args[:2]==("hook","--claim") for tool,args,payload in self.cli.calls))
    def test_generation_change_while_model_runs_prevents_close(self):
        transport=FakeTransport()
        def complete():
            self.cli.generation="2"
            return {"state":"resolved","evidence_sha256":"a"*64,"summary":"fixture verified"}
        transport.run_once=complete
        with self.assertRaises(Hold): self.invoke(transport)
        self.assertEqual(self.cli.status,"in_progress")
    def test_editor_ownership_change_while_model_runs_prevents_close(self):
        transport=FakeTransport()
        def complete():
            self.cli.editor_sid="other-session"
            return {"state":"resolved","evidence_sha256":"a"*64,"summary":"fixture verified"}
        transport.run_once=complete
        with self.assertRaises(Hold): self.invoke(transport)
        self.assertEqual(self.cli.status,"in_progress")
    def test_native_cli_refuses_conflicting_editor_identity(self):
        env={"GC_SESSION_ID":SID,"GC_INSTANCE_TOKEN":TOKEN,"GC_CITY_PATH":"/tmp/city","CLAUDE_SESSION_ID":"unrelated"}
        with self.assertRaises(Hold): NativeCLI("/usr/bin/true","/usr/bin/true","/tmp/city",env)
    def test_native_cli_refuses_alternate_editor_authority(self):
        env={"GC_SESSION_ID":SID,"GC_INSTANCE_TOKEN":TOKEN,"GC_CITY_PATH":"/tmp/city","GT_BD_STATE":"/tmp/fake"}
        with self.assertRaises(Hold): NativeCLI("/usr/bin/true","/usr/bin/true","/tmp/city",env)
    def test_fake_process_output_is_bounded(self):
        import sys
        with self.assertRaises(Hold):
            bounded_process([sys.executable,"-c","print(chr(120)*100000)"],{},None,2,(0,))
    def test_fake_process_receives_eof_without_input(self):
        import sys
        code, raw=bounded_process([sys.executable,"-c","import sys;print(len(sys.stdin.read()))"],{},None,2,(0,))
        self.assertEqual((code,raw),(0,b"0\n"))
    def test_transition_to_draining_after_idle_same_incarnation_can_ack(self):
        original=self.cli.__class__.__call__
        def invoke(tool,args,payload=None,allowed=(0,)):
            result=original(self.cli,tool,args,payload,allowed)
            if args[:2]==["hook","--claim"] and self.cli.status=="closed": self.cli.session_state="draining"
            return result
        self.queue.invoke=invoke
        self.cli.idle_reason="drain_pending"
        self.assertEqual(self.invoke(FakeTransport()),"resolved")
        self.assertEqual(self.cli.calls[-1][1],("runtime","drain-ack","--json"))
    def test_stale_session_idle_never_acknowledges(self):
        self.cli.idle_reason="stale_session"
        with self.assertRaises(Hold): self.invoke(FakeTransport())
        self.assertFalse(any(args[:2]==("runtime","drain-ack") for tool,args,payload in self.cli.calls))
    def test_generation_change_after_idle_never_acknowledges(self):
        self.cli.change_generation_on_idle=True
        with self.assertRaises(Hold): self.invoke(FakeTransport())
        self.assertFalse(any(args[:2]==("runtime","drain-ack") for tool,args,payload in self.cli.calls))
    def test_wait_time_fence_mutations_prevent_run(self):
        for mutation in (lambda:setattr(self.cli,"editor_sid","other"), lambda:setattr(self.cli,"lease_id","changed"), lambda:setattr(self.cli,"lease_stale",True), lambda:self.cli.metadata.update({"repairer.accepted_sha256":"0"*64}), lambda:self.cli.comments.clear(), lambda:setattr(self.queue,"pin_check",lambda:(_ for _ in ()).throw(Hold("sop_changed")))):
            with self.subTest(mutation=mutation):
                self.cli=FakeCLI()
                self.queue=NativeQueue(self.cli,self.session,FIXTURE,self.artifact)
                transport=FakeTransport()
                transport.await_ready=lambda receipt:mutation()
                directory=self.root/str(len(list(self.root.iterdir())))
                directory.mkdir(mode=0o700)
                with self.assertRaises(Hold): coordinate(self.queue,OncePermit(directory),transport,self.accepted)
                self.assertEqual(transport.runs,0)
    def test_discovery_other_work_refuses_before_claim(self):
        self.cli.discovery_ids=[FIXTURE,"gc-other"]
        transport=FakeTransport()
        with self.assertRaises(Hold): self.invoke(transport)
        self.assertFalse(any(args[:2]==("hook","--claim") for tool,args,payload in self.cli.calls))
        self.assertEqual(transport.runs,0)
    def test_initial_wrong_fixture_has_no_accepted_or_vm(self):
        self.cli.claimed_id="gc-other"
        transport=FakeTransport()
        with self.assertRaises(Hold): self.invoke(transport)
        self.assertEqual(self.cli.comments,[])
        self.assertEqual(transport.runs,0)
    def test_close_applied_lost_reply_preserves_permit_and_no_ack(self):
        self.cli.close_lost=True
        with self.assertRaises(Hold): self.invoke(FakeTransport())
        self.assertEqual(self.cli.status,"closed")
        self.assertTrue((self.root/"once.json").exists())
        self.assertFalse(any(args[:2]==("runtime","drain-ack") for tool,args,payload in self.cli.calls))
    def test_unexpected_work_after_close_never_acknowledges(self):
        self.cli.new_work_after_close=True
        with self.assertRaises(Hold): self.invoke(FakeTransport())
        self.assertFalse(any(args[:2]==("runtime","drain-ack") for tool,args,payload in self.cli.calls))
    def test_drain_ack_failure_is_not_resolved(self):
        self.cli.ack_failure=True
        with self.assertRaises(Hold): self.invoke(FakeTransport())
        self.assertEqual(self.cli.status,"closed")
    def test_terminal_shape_refuses_extra_instructions(self):
        transport=FakeTransport()
        transport.run_once=lambda: {"state":"resolved","evidence_sha256":"a"*64,"summary":"ok","command":"rm something"}
        with self.assertRaises(Hold): self.invoke(transport)
        self.assertEqual(self.cli.status,"in_progress")
    def test_symlink_permit_is_not_followed_or_reset(self):
        target=self.root/"retained"
        target.write_text("retained")
        (self.root/"once.json").symlink_to(target)
        with self.assertRaises(Hold): self.invoke(FakeTransport())
        self.assertEqual(target.read_text(),"retained")



class TransportTests(unittest.TestCase):
    def setUp(self):
        self.time=0
        self.calls=[]
        self.accepted={"receipt":{"incident_id":FIXTURE,"owner_session":SID,"generation":"2","incarnation_sha256":"b"*64},"comment_sha256":"c"*64}
    def frame(self,state):
        pending=state=="pending_profile"
        return {"schema_version":1,"state":state,"incident_id":FIXTURE,"owner_session":None if pending else SID,"generation":None if pending else "2","incarnation_sha256":None if pending else "b"*64,"accepted_comment_sha256":None if pending else "c"*64,"evidence_sha256":"d"*64 if state=="resolved" else None,"summary":"root fixture receipt"}
    def client(self,frames):
        def invoke(command,environment,payload,timeout,allowed):
            self.calls.append((command,environment,payload))
            value=frames.pop(0)
            if isinstance(value,Exception): raise value
            return 0,json.dumps(value).encode()
        def sleep(seconds): self.time+=seconds
        return SSHTransport(invoke=invoke,clock=lambda:self.time,sleep=sleep)
    def test_pending_waits_then_exact_ready_and_single_run(self):
        client=self.client([self.frame("pending_profile"),self.frame("ready"),self.frame("resolved")])
        client.await_ready(self.accepted)
        result=client.run_once()
        self.assertEqual(result["state"],"resolved")
        self.assertEqual([call[0][-1] for call in self.calls],["repairer-limited208 status","repairer-limited208 status","repairer-limited208 run"])
        self.assertTrue(all(call[2]==b"" and TOKEN not in json.dumps(call[1]) for call in self.calls))
        with self.assertRaises(Hold): client.run_once()
    def test_lost_run_uses_status_only(self):
        client=self.client([self.frame("ready"),Hold("lost"),self.frame("running"),self.frame("resolved")])
        client.await_ready(self.accepted)
        self.assertEqual(client.run_once()["state"],"resolved")
        self.assertEqual(sum(call[0][-1]=="repairer-limited208 run" for call in self.calls),1)
    def test_other_incarnation_refuses_before_run(self):
        wrong=self.frame("ready");wrong["generation"]="3"
        client=self.client([wrong])
        with self.assertRaises(Hold): client.await_ready(self.accepted)
        self.assertFalse(any(call[0][-1].endswith(" run") for call in self.calls))
    def test_pending_identity_fields_must_be_null(self):
        wrong=self.frame("pending_profile");wrong["owner_session"]=SID
        client=self.client([wrong])
        with self.assertRaises(Hold): client.await_ready(self.accepted)
    def test_extra_response_field_refused(self):
        wrong=self.frame("ready");wrong["command"]="anything"
        client=self.client([wrong])
        with self.assertRaises(Hold): client.await_ready(self.accepted)
    def test_ready_cannot_be_successful_run_response(self):
        client=self.client([self.frame("ready"),self.frame("ready"),self.frame("ready")])
        client.await_ready(self.accepted)
        with self.assertRaises(Hold): client.run_once()
    def test_pending_deadline_never_starts_model(self):
        client=self.client([self.frame("pending_profile") for _ in range(70)])
        with self.assertRaises(Hold): client.await_ready(self.accepted)
        self.assertLessEqual(self.time,120)
        self.assertFalse(any(call[0][-1].endswith(" run") for call in self.calls))


    def test_ready_received_after_phase_deadline_never_permits_run(self):
        client=self.client([self.frame("pending_profile"),self.frame("ready")])
        invoke=client.invoke
        def slow(command,environment,payload,timeout,allowed):
            self.time+=117 if not self.calls else 15
            return invoke(command,environment,payload,timeout,allowed)
        client.invoke=slow
        with self.assertRaises(Hold): client.await_ready(self.accepted)
        self.assertFalse(any(call[0][-1].endswith(" run") for call in self.calls))

    def test_late_natural_cycle_at_210_seconds_can_resolve_without_retry(self):
        client=self.client([self.frame("ready"),self.frame("running"),self.frame("resolved")])
        invoke=client.invoke
        def late(command,environment,payload,timeout,allowed):
            if len(self.calls)>=2: self.time=210
            return invoke(command,environment,payload,timeout,allowed)
        client.invoke=late
        client.await_ready(self.accepted)
        self.assertEqual(client.run_once()["state"],"resolved")
        self.assertEqual(sum(call[0][-1].endswith(" run") for call in self.calls),1)
    def test_running_reconciliation_has_fixed_240_second_ceiling(self):
        client=self.client([self.frame("ready")]+[self.frame("running") for _ in range(125)])
        client.await_ready(self.accepted)
        with self.assertRaises(Hold): client.run_once()
        self.assertEqual(self.time,240)
        self.assertEqual(sum(call[0][-1].endswith(" run") for call in self.calls),1)

    def test_run_timeout_60_status_timeout_15_are_exact_bounded_args(self):
        client=self.client([self.frame("ready"),self.frame("running"),self.frame("resolved")])
        invoke=client.invoke
        timeouts=[]
        def record(command,environment,payload,timeout,allowed):
            timeouts.append((command[-1],timeout))
            return invoke(command,environment,payload,timeout,allowed)
        client.invoke=record
        client.await_ready(self.accepted)
        self.assertEqual(client.run_once()["state"],"resolved")
        self.assertEqual(timeouts,[("repairer-limited208 status",15),("repairer-limited208 run",60),("repairer-limited208 status",15)])
    def test_lost_60_second_run_keeps_total_240_and_never_retries_run(self):
        client=self.client([self.frame("ready"),Hold("lost")]+[self.frame("running") for _ in range(125)])
        invoke=client.invoke
        timeouts=[]
        def record(command,environment,payload,timeout,allowed):
            timeouts.append((command[-1],timeout))
            if command[-1].endswith(" run"): self.time+=timeout
            return invoke(command,environment,payload,timeout,allowed)
        client.invoke=record
        client.await_ready(self.accepted)
        with self.assertRaises(Hold): client.run_once()
        self.assertEqual(self.time,240)
        self.assertEqual([timeout for command,timeout in timeouts if command.endswith(" run")],[60])
        self.assertTrue(all(timeout<=15 for command,timeout in timeouts if command.endswith(" status")))

if __name__ == "__main__":
    unittest.main()
