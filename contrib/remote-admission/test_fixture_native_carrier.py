import copy
import importlib.util
import json
import pathlib
import tempfile
import unittest
from unittest import mock

HERE=pathlib.Path(__file__).parent
spec=importlib.util.spec_from_file_location("fixture_metadata",HERE/"fixture_metadata.py")
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
TRACE=HERE.parents[1]/"internal/runtime/ssh/testdata/fixture-metadata-carrier.json"

class NativeCarrierTests(unittest.TestCase):
 def setUp(self):
  self.trace=json.loads(TRACE.read_text())
  self.carrier=m.NativeCarrier(self.trace)
 def test_real_go_provider_stage_and_all_nonce_variants(self):
  for rec in self.trace["records"]:
   for tag in ["0123456789ABCDEF","AAAAAAAAAAAAAAAA"]:
    script=rec["calls"][0]["stdin"].replace("GC_EOF_FIXTURE","GC_EOF_"+tag)
    key,value,env,tmux=self.carrier.decode_stage(script)
    self.assertEqual((key,value),(rec["key"],rec["value"]))
    self.assertIn("export "+key,env)
    self.assertIn("'=queue-fixture-runtime'",tmux)
 def test_script_changes_unknown_keys_payload_injection_and_lengths_refuse(self):
  src=self.trace["records"][0]["calls"][0]["stdin"].replace("GC_EOF_FIXTURE","GC_EOF_0123456789ABCDEF")
  for script in [src+"id\n",src.replace("umask 077","umask 000"),src.replace("GC_DRAIN_ACK","PATH"),src.replace("'1'","'0'"),src.replace("'1'","'$(id)'"),src.replace("-eq ","-eq 9"),src[:10],src+"x"*16384]:
   with self.subTest(script=script[:25]),self.assertRaises(ValueError):self.carrier.decode_stage(script)
 def test_digest_is_only_64_lowerhex_data(self):
  src=self.trace["records"][2]["calls"][0]["stdin"].replace("GC_EOF_FIXTURE","GC_EOF_0123456789ABCDEF")
  self.assertEqual(self.carrier.decode_stage(src.replace("a"*64,"b"*64))[1],"b"*64)
  for bad in ["B"*64,"token","a"*63,"$(id)"]:
   with self.assertRaises(ValueError):self.carrier.decode_stage(src.replace("a"*64,bad))
 def test_actual_trace_stage_apply_retains_cleanup_and_refuses_repeat(self):
  with tempfile.TemporaryDirectory() as tmp:
   root=pathlib.Path(tmp)
   (root/"stage.lock").write_bytes(b"");(root/"stage.lock").chmod(0o600)
   for slot in m.SLOTS.values():
    d=root/slot;d.mkdir()
    for name in ("env.sh","session.tmux"):(d/name).write_bytes(b"");(d/name).chmod(0o600)
   calls=[]
   # Tests use real owned files; production ownership/path gates are not disabled
   # by any CLI flag and are independently covered by socket/path tests.
   with mock.patch.object(m,"root_path",side_effect=lambda p:pathlib.Path(p)),mock.patch.object(m,"TRANSPORT_UID",m.os.geteuid()):
    for rec in self.trace["records"]:
     script=rec["calls"][0]["stdin"].replace("GC_EOF_FIXTURE","GC_EOF_0123456789ABCDEF")
     path=self.carrier.stage(root,script)
     want=root/m.SLOTS[rec["key"]]
     self.assertEqual(path,str(want))
     args=self.carrier.source(root,str(want/"session.tmux"))
     self.assertEqual(args,["set-environment","-t",m.TARGET,rec["key"],rec["value"]])
     calls.append(args)
     with self.assertRaises(ValueError):self.carrier.stage(root,script)
     with self.assertRaises(ValueError):self.carrier.cleanup(root,str(want))
     self.assertTrue((want/"session.tmux").is_file())
    self.assertEqual(len(calls),3)
    with self.assertRaises(ValueError):self.carrier.source(root,"/tmp/arbitrary/session.tmux")
 def test_changed_source_file_never_becomes_tmux_code(self):
  with tempfile.TemporaryDirectory() as tmp:
   root=pathlib.Path(tmp);d=root/m.SLOTS["GC_DRAIN_ACK"];d.mkdir()
   (root/"stage.lock").write_bytes(b"");(root/"stage.lock").chmod(0o600)
   (d/"env.sh").write_text("GC_DRAIN_ACK='1'\nexport GC_DRAIN_ACK\n")
   (d/"session.tmux").write_text("run-shell id\n")
   for p in [root/"stage.lock",d/"env.sh",d/"session.tmux"]:p.chmod(0o600)
   with mock.patch.object(m,"root_path",side_effect=lambda p:pathlib.Path(p)),mock.patch.object(m,"TRANSPORT_UID",m.os.geteuid()),self.assertRaises(ValueError):
    self.carrier.source(root,str(d/"session.tmux"))

if __name__=="__main__":unittest.main()
