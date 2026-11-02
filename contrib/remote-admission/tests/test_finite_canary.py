"""Pure finite endpoint controls; no services, sockets or model invocation."""
import importlib.util
import pathlib
import unittest
from unittest import mock
P = pathlib.Path(__file__).resolve().parents[1] / "finite_canary.py"
spec = importlib.util.spec_from_file_location("finite_canary", P)
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)

class Controls(unittest.TestCase):
    def test_dispatch_rejects_before_spawn(self):
        for command in ("", "repairer-limited208 run x", "repairer-limited208 status\n", "sh"):
            with self.subTest(command=command), mock.patch.object(m.subprocess, "run") as run:
                with self.assertRaises(m.Refusal):
                    m.dispatch(command, b"")
                run.assert_not_called()

    def test_dispatch_empty_stdin_fixed_argv(self):
        with mock.patch.object(m.subprocess, "run") as run:
            m.dispatch("repairer-limited208 status", b"")
            self.assertEqual(run.call_args.args[0], ["/usr/bin/sudo", "-n", m.ROOT_ENTRY, "status"])
        with mock.patch.object(m.subprocess, "run") as run:
            with self.assertRaises(m.Refusal):
                m.dispatch("repairer-limited208 run", b"x")
            run.assert_not_called()

    def test_public_projection_exact_fields(self):
        result = m.projection("pending_profile", "fixture-208", None)
        self.assertEqual(set(result), set("schema_version state incident_id owner_session generation incarnation_sha256 accepted_comment_sha256 evidence_sha256 summary".split()))
        self.assertIsNone(result["generation"])
        self.assertIsNone(result["evidence_sha256"])

    def test_resolved_requires_evidence(self):
        for evidence in (None, "", "g"*64):
            with self.subTest(evidence=evidence), self.assertRaises(m.Refusal):
                m.projection("resolved", "fixture-208", {}, evidence)

    def test_same_native_generation_not_attempt(self):
        request = dict(session_id="sid", generation="2", instance_token="private")
        accepted = dict(incident_id="incident", owner_session="sid", generation="2",
                        incarnation_sha256=m.sha(b"private"), executor_host="vm328699",
                        resource=m.UNIT, target_host="vm328699")
        m.validate_owner(request, accepted, "incident")
        accepted["generation"] = "1"
        with self.assertRaises(m.Refusal):
            m.validate_owner(request, accepted, "incident")

    def test_natural_recovery_requires_same_invocation(self):
        active = dict(invocation="a"*32, boot="b"*32, pid=42, start="5", cgroup="/test", started="10")
        ended = dict(active, state="inactive", result="success", code="1", status="0",
                     pid=0, start="", cgroup="/test", started="10", exited="90000010")
        self.assertTrue(m.recovered(active, ended))
        self.assertTrue(m.recovered(active, dict(ended, cgroup="")))
        for change in ({"invocation":"c"*32}, {"result":"timeout"}, {"status":"1"},
                       {"state":"active"}, {"exited":"11"}, {"boot":"d"*32}):
            with self.subTest(change=change):
                self.assertFalse(m.recovered(active, dict(ended, **change)))


class StateGuards(unittest.TestCase):
    def canary(self, phase="staged"):
        c = object.__new__(m.Canary)
        c.context = {"prompt_sha256": m.sha(b"prompt")}
        c.incident = "incident"
        c.accepted = {}
        c.request = {"attempt": "a"*32}
        c.authority = mock.Mock()
        c.authority.directory = pathlib.Path("/state")
        self.row = dict(phase=phase, ever_consumed=phase != "staged", attested=phase == "entered",
                        revoked=False, scope={"empty": True})
        c.row = lambda: self.row
        c.authority.host.scope.return_value = {"empty": True}
        return c

    def test_ready_checks_resources_prompt_and_no_intent(self):
        c = self.canary()
        with mock.patch.object(m, "CONFIG", pathlib.Path("/missing-limited208-test")), mock.patch.object(m, "read", return_value=b"prompt"):
            self.assertEqual(c.status()["state"], "ready")
            c.authority.host.resources.assert_called_once()
            c.authority.host.resources.side_effect = m.Refusal("pressure")
            with self.assertRaises(m.Refusal):
                c.status()

    def test_unknown_never_resolves_or_runs(self):
        c = self.canary("unknown")
        with mock.patch.object(m, "exclusive") as write:
            self.assertEqual(c.status()["state"], "hold")
            with self.assertRaises(m.Refusal):
                c.run()
            write.assert_not_called()
            c.authority.enter.assert_not_called()

    def test_run_intent_durable_before_enter(self):
        c = self.canary()
        c.status = mock.Mock(side_effect=[{"state":"ready"}, {"state":"running"}])
        order = []
        with mock.patch.object(m, "socket_ready", side_effect=lambda *a:order.append("socket")), mock.patch.object(m, "read", return_value=b"context"), mock.patch.object(m, "exclusive", side_effect=lambda *a:order.append("intent")):
            c.authority.enter.side_effect = lambda *a:order.append("enter")
            self.assertEqual(c.run()["state"], "running")
        self.assertEqual(order, ["socket", "intent", "enter"])

    def test_intent_failure_never_starts(self):
        c = self.canary()
        c.status = lambda: {"state":"ready"}
        with mock.patch.object(m, "socket_ready"), mock.patch.object(m, "read", return_value=b"context"), mock.patch.object(m, "exclusive", side_effect=OSError("fsync")):
            with self.assertRaises(OSError):
                c.run()
        c.authority.enter.assert_not_called()

    def test_model_exit_without_action_is_not_resolved(self):
        c = self.canary("exited")
        self.row.update(attested=True, empty_scope={"empty":True})
        c.terminal_ok = lambda row:True
        c.action_complete = lambda: None
        self.assertEqual(c.status()["state"], "hold")

    def test_unentered_peer_no_action(self):
        c = self.canary("unknown")
        c.profile = mock.Mock(data={"profile_id":"test"})
        c.authority.queue_observe.side_effect = m.Refusal("not_entered")
        with mock.patch.object(m.subprocess, "run") as run:
            with self.assertRaises(m.Refusal):
                c.action({"pid":42}, "restart")
            run.assert_not_called()

    def test_exclusive_collision_and_fsync_failure(self):
        import tempfile
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(m, "check_directory"):
            path = pathlib.Path(directory) / "intent"
            m.exclusive(path, {"one":1})
            with self.assertRaises(FileExistsError):
                m.exclusive(path, {"two":2})
            self.assertEqual(path.read_bytes(), b'{"one":1}')
            path2 = pathlib.Path(directory) / "partial"
            with mock.patch.object(m.os, "fsync", side_effect=OSError("disk")):
                with self.assertRaises(OSError):
                    m.exclusive(path2, {"one":1})
            self.assertTrue(path2.exists())
            with self.assertRaises(FileExistsError):
                m.exclusive(path2, {"one":1})

    def test_exclusive_symlink_never_overwrites(self):
        import tempfile
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(m, "check_directory"):
            target = pathlib.Path(directory) / "target"
            target.write_bytes(b"preserve")
            alias = pathlib.Path(directory) / "intent"
            alias.symlink_to(target)
            with self.assertRaises(FileExistsError):
                m.exclusive(alias, {"bad":True})
            self.assertEqual(target.read_bytes(), b"preserve")

class ReuseContracts(unittest.TestCase):
    def test_actual_helper_profile_and_coordinator_accepted_shape(self):
        import test_helper as fixture
        g = fixture.g
        profile = fixture.configuration("/srv/limited-guard")
        profile["max_consumed_attempts"] = 1
        profile["resources"] = dict(cpu_quota_percent=100, memory_max_bytes=2*1024**3,
            memory_swap_max_bytes=0,tasks_max=128,runtime_max_seconds=120,
            output_max_bytes=1024**2,tmp_max_bytes=64*1024**2,work_max_bytes=64*1024**2)
        profile["helper_path"] = str(m.pathlib.Path(m.__file__).with_name("helper.py"))
        profile["helper_sha256"] = m.HELPER_SHA
        request = dict(city="city", session_id="sid", template="role", runtime_handle="runtime",
                       generation="2", instance_token="private", attempt="1"*32, binding=profile["binding"])
        pins = {"/home/dev/Projects/gascity-kb/common/infrastructure/repairer-operations.md":"a"*64,
                "/home/dev/Projects/gascity-kb/common/infrastructure/agent-repair-handoff.md":"b"*64,
                "/home/dev/Projects/gascity-kb/roles/repairer.md":"c"*64}
        accepted = dict(incident_id="incident", owner_session="sid", generation="2",
                        generation_source="native_session", incarnation_sha256=m.sha(b"private"),
                        executor_host="vm328699", target_host="vm328699", resource=m.UNIT,
                        scope="one fixed canary", source_sha=profile["source_sha"], sop_pins=pins,
                        next_checkpoint="native terminal")
        raw_accepted = g.canonical(accepted).decode()
        witness = {k:accepted[k] for k in ("incident_id","owner_session","generation","incarnation_sha256")}
        witness["accepted_comment_sha256"] = m.sha(raw_accepted.encode())
        context = dict(request=request, accepted_comment=raw_accepted, native_witness=witness,
                       profile_sha256=m.sha(g.canonical(profile)), prompt_sha256="d"*64,
                       sop_pins=pins, fixture_unit_sha256="e"*64)
        files = {m.CONFIG/"manifest.json":g.canonical(dict(schema_version=1,incident_id="incident",source_sha=profile["source_sha"])),
                 m.CONFIG/"context.json":g.canonical(context), pathlib.Path(g.PROFILE):g.canonical(profile)}
        with mock.patch.object(m, "read", side_effect=lambda p: files[pathlib.Path(p)]), \
             mock.patch.object(g, "Authority"), mock.patch.object(g, "LinuxHost", fixture.FakeHost):
            c = m.Canary(g)
        self.assertIsInstance(c.profile, g.Profile)
        self.assertEqual(c.accepted["generation"], "2")
        import copy
        variants=[]
        bad=copy.deepcopy(context);bad["sop_pins"].pop(next(iter(pins)));variants.append(bad)
        bad=copy.deepcopy(context);bad["sop_pins"][next(iter(pins))]="f"*64;variants.append(bad)
        bad=copy.deepcopy(context);bad["request"]["generation"]=2;variants.append(bad)
        bad=copy.deepcopy(context);bad["native_witness"]["owner_session"]="other";variants.append(bad)
        bad=copy.deepcopy(context);bad["native_witness"]["accepted_comment_sha256"]="f"*64;variants.append(bad)
        for index,bad in enumerate(variants):
            changed=dict(files);changed[m.CONFIG/"context.json"]=g.canonical(bad)
            with self.subTest(variant=index),mock.patch.object(m,"read",side_effect=lambda p:changed[pathlib.Path(p)]), \
                 mock.patch.object(g,"Authority") as authority,mock.patch.object(g,"LinuxHost",fixture.FakeHost):
                with self.assertRaises((m.Refusal,g.Refusal)):
                    m.Canary(g)
                authority.return_value.observe.assert_not_called()
                authority.return_value.enter.assert_not_called()


    def test_model_terminal_waits_for_owned_natural_cycle(self):
        c = StateGuards().canary("exited")
        c.row().update(attested=True, empty_scope={"empty":True})
        c.terminal_ok = lambda row:True
        c.action_complete = lambda: "pending"
        self.assertEqual(c.status()["state"], "running")

class ActionGuards(unittest.TestCase):
    def test_one_fixed_action_intent_before_effect_and_no_replay(self):
        import tempfile
        from types import SimpleNamespace
        c = StateGuards().canary("entered")
        c.profile = mock.Mock(data={"profile_id":"test"})
        c.request = dict(generation="2",instance_token="private",attempt="a"*32,session_id="sid")
        c.context["fixture_unit_sha256"] = m.sha(b"unit")
        c.authority.queue_observe.return_value = {"identity":dict(c.request,sid="sid")}
        c.authority.host.peer.return_value = {"pid":42}
        before = dict(state="inactive",pid=0)
        active = dict(state="active",pid=42,invocation="b"*32,start="15")
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory); (root/"lock").write_bytes(b"")
            calls = []
            original_fstat = m.os.fstat
            def fstat(fd):
                info = original_fstat(fd)
                return SimpleNamespace(st_mode=0o100600,st_uid=0,st_nlink=1) if info.st_size == 0 else info
            with mock.patch.object(m,"ACTION",root), mock.patch.object(m,"check_directory"), mock.patch.object(m,"fixture_empty",return_value=True), mock.patch.object(m,"effective_unit"), \
                 mock.patch.object(m.os,"fstat",side_effect=fstat), \
                 mock.patch.object(m,"read",return_value=b"context"), \
                 mock.patch.object(m,"protected_unit",return_value=b"unit"), \
                 mock.patch.object(m,"fixture_view",side_effect=[before,active,active]), \
                 mock.patch.object(m.time,"sleep"), \
                 mock.patch.object(m.subprocess,"run",side_effect=lambda *a,**k:calls.append(a[0])):
                self.assertEqual(c.action({"pid":42},"restart"), {"restarted":True})
                self.assertTrue((root/"intent.json").exists())
                self.assertTrue((root/"active.json").exists())
                self.assertEqual(calls, [["/usr/bin/systemctl","restart",m.UNIT]])
                with self.assertRaises(m.Refusal):
                    c.action({"pid":42},"restart")
                self.assertEqual(len(calls),1)

    def test_socket_parent_exact_model_traverse_mode(self):
        from types import SimpleNamespace
        root = pathlib.Path("/run/gc-repairer-limited208")
        def metadata(path):
            return SimpleNamespace(st_mode=stat_mode[0] if path == root else 0o40755,
                                   st_uid=0,st_gid=982 if path == root else 0)
        stat_mode = [0o40710]
        with mock.patch.object(pathlib.Path,"lstat",metadata):
            m.socket_parent(root)
            stat_mode[0] = 0o40700
            with self.assertRaises(m.Refusal):
                m.socket_parent(root)
            stat_mode[0] = 0o40770
            with self.assertRaises(m.Refusal):
                m.socket_parent(root)

    def test_bad_server_prevents_consumption(self):
        c = StateGuards().canary()
        c.status = lambda:{"state":"ready"}
        with mock.patch.object(m,"socket_ready",side_effect=m.Refusal("socket")), mock.patch.object(m,"exclusive") as write:
            with self.assertRaises(m.Refusal):
                c.run()
            write.assert_not_called()
            c.authority.enter.assert_not_called()

    def test_root_caller_denied_before_helper_import(self):
        with mock.patch.object(m.os,"geteuid",return_value=0), \
             mock.patch.object(m.sys,"argv",["entry","run"]), \
             mock.patch.dict(m.os.environ,{"SUDO_UID":"988"},clear=True), \
             mock.patch.object(m,"guard_module") as load:
            with self.assertRaises(m.Refusal):
                m.main()
            load.assert_not_called()

class ClientAndTerminal(unittest.TestCase):
    def test_client_lost_restart_response_never_retries(self):
        spec = importlib.util.spec_from_file_location("fixed_client", P.with_name("finite_action_client.py"))
        client = importlib.util.module_from_spec(spec); spec.loader.exec_module(client)
        calls=[]
        def request(op):
            calls.append(op)
            if op == "status":
                return {"entered":True,"unit":m.UNIT,"state":"inactive","main_pid":0,"invocation_id":"","result":"success"}
            raise TimeoutError()
        with mock.patch.object(client.sys,"argv",["client","restart"]), mock.patch.object(client,"request",side_effect=request):
            self.assertEqual(client.main(), 1)
        self.assertEqual(calls,["status","restart"])

    def test_model_terminal_failure_or_new_parent_never_resolves(self):
        c = StateGuards().canary("exited")
        row = dict(identity={"unit":"unit","invocation_id":"a"*32},
                   empty_scope={"boot":"b","empty":True})
        view = dict(MainPID="0",InvocationID="a"*32,ExecMainCode="1",ExecMainStatus="0",Result="success")
        c.authority.host.show.return_value=view
        c.authority.host.scope.return_value=row["empty_scope"]
        self.assertTrue(c.terminal_ok(row))
        for changes in ({"ExecMainStatus":"1"},{"Result":"timeout"},{"InvocationID":"c"*32},{"MainPID":"42"}):
            c.authority.host.show.return_value=dict(view,**changes)
            self.assertFalse(c.terminal_ok(row))
        c.authority.host.show.return_value=view
        c.authority.host.scope.return_value={"boot":"different","empty":True}
        self.assertFalse(c.terminal_ok(row))
