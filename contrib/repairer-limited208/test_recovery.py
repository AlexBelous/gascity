"""Real bounded-output persistence boundary and exact one-shot recovery contract."""
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest

from coordinator import bounded_process


class ProcessPersistenceTests(unittest.TestCase):
    def test_native_persistence_above_stdout_limit_remains_writable(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "native-audit.log"
            path.write_bytes(b"x" * 100000)
            code, output = bounded_process(
                [sys.executable, "-c",
                 "from pathlib import Path;import sys;"
                 "p=Path(sys.argv[1]);"
                 "f=p.open('ab');f.write(b'entry');f.close();print('ok')",
                 str(path)], {}, None, 10, (0,))
            self.assertEqual((code, output), (0, b"ok\n"))
            self.assertEqual(path.stat().st_size, 100005)

class RecoveryPermitTests(unittest.TestCase):
    def setUp(self):
        from coordinator import Session, canonical
        import hashlib
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.path = Path(self.directory.name)
        self.sid = "gcg-534258985155672334"
        self.fixture = "gc-hgpqk5"
        self.session = Session(self.sid, "fixture-private-token", "repairer-coordinator")
        self.original = canonical({"incident_id": self.fixture, "owner_session": self.sid,
                                   "state": "consumed_before_claim"}).encode()
        (self.path / "once.json").write_bytes(self.original)
        (self.path / "once.json").chmod(0o600)
        import coordinator
        self.ticket = dict(schema_version=1, fixture_id=self.fixture, owner_session=self.sid,
                           generation_before="3", generation_after="4",
                           original_once_sha256=hashlib.sha256(self.original).hexdigest(),
                           source_sha="4"*40,
                           coordinator_sha256=hashlib.sha256(Path(coordinator.__file__).read_bytes()).hexdigest(),
                           approval_url="https://github.com/AlexBelous/gascity-kb/issues/208#issuecomment-6576966510")
        self.reads = []
        owner = self
        class Queue:
            session = owner.session
            fixture = owner.fixture
            def show(self, identity):
                return {"id":identity,"status":"open","metadata":{"state":"awake","template":"repairer-coordinator",
                    "instance_token":self.session.token,"generation":"4"}}
            def recovery_preflight(self):
                owner.reads.append("read_only")
                return {"generation": "4",
                        "incarnation_sha256": hashlib.sha256(self.session.token.encode()).hexdigest()}
        self.queue = Queue()
        self.receipt = dict(incident_id=self.fixture, owner_session=self.sid, state="consumed_before_claim")
        self.approved = True
    def loader(self, name):
        from coordinator import canonical
        import hashlib
        if name == "ticket.json":
            return self.ticket
        if not self.approved:
            raise FileNotFoundError(name)
        intent = (self.path / "recovery-intent.json").read_bytes()
        return dict(schema_version=1, ticket_sha256=hashlib.sha256(canonical(self.ticket).encode()).hexdigest(),
                    recovery_intent_sha256=hashlib.sha256(intent).hexdigest(),
                    owner_session=self.sid, generation="4",
                    incarnation_sha256=hashlib.sha256(self.session.token.encode()).hexdigest(),
                    source_sha="4"*40)
    def permit(self):
        from coordinator import RecoveryPermit
        return RecoveryPermit(self.path, self.queue, "4"*40, root_loader=self.loader)
    def test_exact_existing_permit_continues_once_and_preserves_original(self):
        self.permit().reserve(self.receipt)
        self.assertEqual((self.path / "once.json").read_bytes(), self.original)
        self.assertEqual(self.reads, ["read_only", "read_only"])
        from coordinator import Hold
        with self.assertRaises(Hold):
            self.permit().reserve(self.receipt)
        self.assertEqual(self.reads, ["read_only", "read_only"])
    def test_ticket_scope_or_generation_drift_never_creates_intent(self):
        from coordinator import Hold
        for key, value in [("fixture_id", "gc-other"), ("owner_session", "gcg-other"),
                           ("generation_after", "5"), ("generation_before", "2"),
                           ("original_once_sha256", "0"*64), ("source_sha", "0"*40),
                           ("coordinator_sha256", "0"*64)]:
            with self.subTest(key=key):
                prior = self.ticket[key]; self.ticket[key] = value
                with self.assertRaises(Hold):
                    self.permit().reserve(self.receipt)
                self.assertFalse((self.path / "recovery-intent.json").exists())
                self.ticket[key] = prior
    def test_no_root_admission_retains_intent_and_stops_without_retry(self):
        from coordinator import RecoveryPermit, Hold
        self.approved = False
        clock = [0]
        def sleep(seconds): clock[0] += seconds
        p = RecoveryPermit(self.path, self.queue, "4"*40, root_loader=self.loader,
                           clock=lambda: clock[0], sleep=sleep, admission_seconds=2)
        with self.assertRaises(Hold):
            p.reserve(self.receipt)
        self.assertTrue((self.path / "recovery-intent.json").exists())
        self.assertEqual(self.reads, ["read_only"])
        self.assertEqual((self.path / "once.json").read_bytes(), self.original)
    def test_root_admission_must_match_actual_intent_and_incarnation(self):
        from coordinator import Hold
        original_loader = self.loader
        def forged(name):
            result = original_loader(name)
            if name == "admission.json":
                result["incarnation_sha256"] = "0"*64
            return result
        p = self.permit(); p.root_loader = forged
        with self.assertRaises(Hold): p.reserve(self.receipt)
        self.assertEqual(self.reads, ["read_only"])
    def test_untrusted_root_file_is_refused(self):
        from coordinator import root_recovery_json, Hold
        ticket = self.path / "ticket.json"; ticket.write_text("{}"); ticket.chmod(0o644)
        with self.assertRaises(Hold): root_recovery_json(ticket)


class OutputBoundaryTests(unittest.TestCase):
    def test_full_duplex_input_does_not_deadlock(self):
        command = [sys.executable, "-c",
                   "import sys;sys.stdout.write('x'*40000);sys.stdout.flush();"
                   "print(len(sys.stdin.buffer.read()))"]
        code, out = bounded_process(command, {}, b"y"*60000, 10, (0,))
        self.assertEqual(code, 0)
        self.assertTrue(out.endswith(b"60000\n"))
    def test_timeout_reaps_only_its_own_child(self):
        from coordinator import Hold
        with tempfile.TemporaryDirectory() as directory:
            pidfile = Path(directory) / "child.pid"
            with self.assertRaises(Hold):
                bounded_process([sys.executable, "-c",
                                 "import os,sys,signal;from pathlib import Path;"
                                 "Path(sys.argv[1]).write_text(str(os.getpid()));signal.pause()",
                                 str(pidfile)], {}, None, 2, (0,))
            self.assertTrue(pidfile.exists())
            with self.assertRaises(ProcessLookupError): os.kill(int(pidfile.read_text()), 0)
    def test_journal_keeps_only_step_exit_and_size(self):
        from coordinator import NativeCLI, StepJournal, Hold
        with tempfile.TemporaryDirectory() as directory:
            env = dict(GC_SESSION_ID="fixture-session", GC_INSTANCE_TOKEN="fixture-private-token",
                       GC_CITY_PATH="/tmp/city")
            cli = NativeCLI("/usr/bin/false", "/usr/bin/false", "/tmp/city", env,
                            journal=StepJournal(Path(directory)))
            with self.assertRaises(Hold): cli("lease", ["status"])
            raw = (Path(directory) / "recovery-steps.jsonl").read_text()
            self.assertNotIn("fixture-private-token", raw)
            rows = [json.loads(line) for line in raw.splitlines()]
            self.assertEqual(rows[-1], dict(step="editor_status", exit_code=1, stdout_bytes=0, outcome="refused"))

class NativeRecoveryPreflightTests(unittest.TestCase):
    def queue(self, mutation=None, generation="4"):
        import copy
        from coordinator import NativeQueue, Session
        sid = "gcg-534258985155672334"
        token = "fixture-only-token"
        fixture = "gc-hgpqk5"
        state = dict(session=dict(id=sid, status="open", metadata=dict(state="awake", generation=generation,
                     template="repairer-coordinator", instance_token=token,
                     agent_name="repairer-coordinator", session_name="s-fixture-runtime")),
                     work=dict(id=fixture, status="open", assignee="", metadata={"gc.routed_to":"repairer-coordinator"}, comments=[]),
                     leases=[], assigned="", extra_ready=False)
        if mutation: mutation(state)
        calls = []
        def invoke(tool, args, payload=None, allowed=(0,)):
            calls.append((tool, tuple(args)))
            if tool == "lease":
                self.assertEqual(args, ["status"])
                result = {"leases":state["leases"]}
            elif args[:2] == ["bd", "show"]:
                result = [state["session"] if args[2] == sid else state["work"]]
            elif args[0] == "ready":
                if "--metadata-field" in args:
                    result = [{"id":fixture}] + ([{"id":"gc-other"}] if state["extra_ready"] else [])
                else:
                    identities = [args[i+1] for i, arg in enumerate(args) if arg == "--assignee-any"] if "--assignee-any" in args else [args[args.index("--assignee")+1]]
                    result = [{"id":"gc-other"}] if state["assigned"] in identities else []
            else:
                raise AssertionError("unexpected command")
            return 0, json.dumps(result).encode()
        q = NativeQueue(invoke, Session(sid, token, "repairer-coordinator"), fixture,
                        {"commit":"4"*40,"branch":"fixture/approved","work_dir":"/fixture/source"})
        return q, calls
    def test_valid_preflight_is_read_only(self):
        for generation in ("4", "5"):
            with self.subTest(generation=generation):
                q, calls = self.queue(generation=generation)
                self.assertEqual(q.recovery_preflight(expected_generation=generation)["generation"], generation)
                self.assertFalse(any("--claim" in args or args[:2] in (("bd","update"),("bd","comment")) for _,args in calls))
        from coordinator import Hold
        for actual, expected in (("4","5"),("5","4"),("6","6")):
            q, _ = self.queue(generation=actual)
            with self.assertRaises(Hold): q.recovery_preflight(expected_generation=expected)
    def test_native_show_omits_empty_comments(self):
        # Native890 + bd show --include-comments omits an empty comments field.
        # The independent bd comments endpoint returns [] for this fixture.
        q, calls = self.queue(lambda state: state["work"].pop("comments"))
        self.assertEqual(q.recovery_preflight()["generation"], "4")
        self.assertFalse(any("--claim" in args for _, args in calls))

    def test_distinct_unsafe_states_fail_closed(self):
        from coordinator import Hold
        cases = {
          "generation": lambda s:s["session"]["metadata"].update(generation="6"),
          "incarnation": lambda s:s["session"]["metadata"].update(instance_token="other-token"),
          "claim": lambda s:s["session"]["metadata"].update(current_claim_bead_id="gc-other"),
          "work_claimed": lambda s:s["work"].update(status="in_progress",assignee="other"),
          "accepted": lambda s:s["work"]["metadata"].update({"repairer.accepted_sha256":"a"*64}),
          "comments": lambda s:s["work"].update(comments=[{"text":"changed"}]),
          "null_comments": lambda s:s["work"].update(comments=None),
          "extra_ready": lambda s:s.update(extra_ready=True),
          "assigned_sid": lambda s:s.update(assigned="gcg-534258985155672334"),
          "assigned_runtime": lambda s:s.update(assigned="s-fixture-runtime"),
          "foreign_lease": lambda s:s.update(leases=[{"bd":"gc-hgpqk5","session_id":"other","stale":True}]),
          "live_lease": lambda s:s.update(leases=[{"bd":"gc-hgpqk5","session_id":"gcg-534258985155672334","stale":False}]),
        }
        for generation in ("4", "5"):
            for label, mutation in cases.items():
                with self.subTest(generation=generation, label=label):
                    q,calls = self.queue(mutation,generation=generation)
                    with self.assertRaises(Hold): q.recovery_preflight(expected_generation=generation)
                    self.assertFalse(any("--claim" in args for _,args in calls))


class ExactOutputTests(unittest.TestCase):
    def test_stdout_exact_boundary_and_flood_capture(self):
        from coordinator import MAX_FRAME, Hold
        code, output = bounded_process([sys.executable,"-c",f"import sys;sys.stdout.buffer.write(b'x'*{MAX_FRAME-1})"],{},None,10,(0,))
        self.assertEqual((code,len(output)),(0,MAX_FRAME-1))
        for size in (MAX_FRAME,MAX_FRAME*16):
            facts=[]
            with self.assertRaises(Hold):
                bounded_process([sys.executable,"-c",f"import sys;sys.stdout.buffer.write(b'x'*{size})"],{},None,10,(0,),
                                lambda code,count,outcome:facts.append((count,outcome)))
            self.assertLessEqual(facts[0][0],MAX_FRAME+1)
            self.assertEqual(facts[0][1],"overflow")
    def test_invalid_input_is_refused_before_spawn(self):
        from coordinator import Hold, MAX_FRAME
        from unittest.mock import patch
        for payload in ("", "bad", b"x"*(MAX_FRAME+1)):
            with self.subTest(type=type(payload).__name__):
                with patch("subprocess.Popen", side_effect=AssertionError("child must not start")) as spawn:
                    with self.assertRaises(Hold):
                        bounded_process(["unused"],{},payload,10,(0,))
                    spawn.assert_not_called()

class DiagnosticsContinuationTests(unittest.TestCase):
    def setUp(self):
        import hashlib
        from coordinator import Session, canonical
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name)
        self.sid = "gcg-534258985155672334"
        self.fixture = "gc-hgpqk5"
        self.session = Session(self.sid, "new-fixture-token", "repairer-coordinator")
        self.receipt = dict(incident_id=self.fixture, owner_session=self.sid, state="consumed_before_claim")
        self.original = canonical(self.receipt).encode()
        self.prior = b'{"generation":"4","incarnation_sha256":"57eb5808991bbd656f2d811ff2d67ffb80fb8838b4ba150eb771530eb5a64bbb","original_once_sha256":"82e30b462767ff1196760eec5e8677f530f2e7ea7089af8bf94870114503bc20","owner_session":"gcg-534258985155672334","schema_version":1,"source_sha":"3af5366c422fac869be884c98f922099e31c0ef0","ticket_sha256":"9dd2b617a8920f9db77bbdc9da2d1c953995eb2538ea37a959670f5255ad7a9f"}'
        for name, raw in (("once.json", self.original), ("recovery-intent.json", self.prior)):
            (self.path/name).write_bytes(raw); (self.path/name).chmod(0o600)
        import coordinator
        self.ticket = dict(schema_version=1, fixture_id=self.fixture, owner_session=self.sid,
            generation_before="4", generation_after="5",
            original_once_sha256=hashlib.sha256(self.original).hexdigest(),
            prior_recovery_intent_sha256=hashlib.sha256(self.prior).hexdigest(),
            source_sha="5"*40, coordinator_sha256=hashlib.sha256(Path(coordinator.__file__).read_bytes()).hexdigest(),
            approval_url="https://github.com/AlexBelous/gascity-kb/issues/208#issuecomment-6580272449")
        self.reads=[]
        owner=self
        class Queue:
            session=owner.session
            fixture=owner.fixture
            def show(self, identity):
                return dict(id=identity, status="open", metadata=dict(state="awake", template="repairer-coordinator", instance_token=self.session.token, generation="5"))
            def recovery_preflight(self, expected_generation="4"):
                owner.reads.append(expected_generation)
                if expected_generation != "5": raise AssertionError("continuation must require generation5")
        self.queue=Queue()
        self.admitted=True
    def loader(self, name):
        import hashlib
        from coordinator import canonical
        if name=="continuation-ticket.json": return self.ticket
        self.assertEqual(name,"continuation-admission.json")
        if not self.admitted: raise FileNotFoundError(name)
        raw=(self.path/"continuation-intent.json").read_bytes()
        return dict(schema_version=1, ticket_sha256=hashlib.sha256(canonical(self.ticket).encode()).hexdigest(),
            recovery_intent_sha256=hashlib.sha256(raw).hexdigest(), owner_session=self.sid,
            generation="5", incarnation_sha256=hashlib.sha256(self.session.token.encode()).hexdigest(), source_sha="5"*40)
    def permit(self, **kwargs):
        from coordinator import DiagnosticsContinuationPermit
        return DiagnosticsContinuationPermit(self.path,self.queue,"5"*40,root_loader=self.loader,**kwargs)
    def test_one_continuation_preserves_both_consumed_receipts(self):
        from coordinator import Hold
        self.permit().reserve(self.receipt)
        self.assertEqual(self.reads,["5","5"])
        self.assertEqual((self.path/"once.json").read_bytes(),self.original)
        self.assertEqual((self.path/"recovery-intent.json").read_bytes(),self.prior)
        self.assertTrue((self.path/"continuation-diagnostics.json").exists())
        with self.assertRaises(Hold): self.permit().reserve(self.receipt)
        self.assertEqual(self.reads,["5","5"])
    def test_prior_receipt_change_or_symlink_refuses_before_new_intent(self):
        from coordinator import Hold
        target=self.path/"recovery-intent.json"
        target.write_bytes(self.prior+b" ")
        with self.assertRaises(Hold): self.permit().reserve(self.receipt)
        self.assertFalse((self.path/"continuation-intent.json").exists())
        target.rename(self.path/"prior")
        target.symlink_to(self.path/"prior")
        with self.assertRaises((Hold,OSError)): self.permit().reserve(self.receipt)
    def test_ticket_cannot_change_prior_scope_or_approval(self):
        from coordinator import Hold
        for key,value in (("prior_recovery_intent_sha256","0"*64),("generation_before","3"),
                          ("generation_after","6"),("approval_url","unapproved")):
            with self.subTest(key=key):
                old=self.ticket[key];self.ticket[key]=value
                with self.assertRaises(Hold): self.permit().reserve(self.receipt)
                self.assertFalse((self.path/"continuation-intent.json").exists())
                self.ticket[key]=old
    def test_missing_admission_retains_new_intent_without_claim(self):
        from coordinator import Hold
        self.admitted=False;clock=[0]
        def sleep(s): clock[0]+=s
        with self.assertRaises(Hold):
            self.permit(clock=lambda:clock[0],sleep=sleep,admission_seconds=2).reserve(self.receipt)
        self.assertEqual(self.reads,["5"])
        self.assertTrue((self.path/"continuation-intent.json").exists())
    def test_prior_receipt_changed_during_admission_is_refused(self):
        from coordinator import Hold
        normal=self.loader
        def changed(name):
            result=normal(name)
            if name=="continuation-admission.json":
                (self.path/"recovery-intent.json").write_bytes(self.prior+b" ")
            return result
        permit=self.permit();permit.root_loader=changed
        with self.assertRaises(Hold):permit.reserve(self.receipt)
        self.assertEqual(self.reads,["5"])
