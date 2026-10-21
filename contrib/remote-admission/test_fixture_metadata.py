import importlib.util
import pathlib
import unittest
from unittest import mock
import stat
from types import SimpleNamespace

spec=importlib.util.spec_from_file_location("fixture_metadata",pathlib.Path(__file__).with_name("fixture_metadata.py"))
m=importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)

class MetadataParserTests(unittest.TestCase):
 def test_exact_native_metadata_argv(self):
  digest="a"*64
  for raw,want in [
   ("'tmux' 'show-environment' '-t' '=queue-fixture-runtime' 'GC_DRAIN_ACK'",["show-environment","-t","=queue-fixture-runtime","GC_DRAIN_ACK"]),
   ("tmux set-environment -t =queue-fixture-runtime -u GC_DRAIN_REASON",["set-environment","-t","=queue-fixture-runtime","-u","GC_DRAIN_REASON"]),
   ("tmux set-environment -t =queue-fixture-runtime -u GC_DRAIN_GENERATION",["set-environment","-t","=queue-fixture-runtime","-u","GC_DRAIN_GENERATION"])]:
   self.assertEqual(m.parse(raw),want)
 def test_no_shell_options_other_sessions_or_unreviewed_values(self):
  for raw in ["tmux set-environment -t =queue-fixture-runtime GC_DRAIN_ACK 1", "", "sh -c id","tmux list-sessions","tmux kill-server",
   "tmux set-environment -g GC_DRAIN_ACK 1",
   "tmux set-environment -t =queue-fixture-runtime:0 GC_DRAIN_ACK 1",
   "tmux set-environment -t =queue-fixture-runtime2 GC_DRAIN_ACK 1",
   "tmux set-environment -t =queue-fixture-runtime PATH /bin",
   "tmux set-environment -t =queue-fixture-runtime GC_INSTANCE_TOKEN aaaa",
   "tmux set-environment -t =queue-fixture-runtime GC_DRAIN_ACK 0",
   "tmux set-environment -t =queue-fixture-runtime GC_DRAIN_ACK_SOURCE controller",
   "tmux set-environment -t =queue-fixture-runtime GC_DRAIN_ACK_REQUESTER_INSTANCE_TOKEN raw-token",
   "tmux set-environment -t =queue-fixture-runtime GC_DRAIN_ACK 1;id",
   "tmux show-environment -t =queue-fixture-runtime GC_DRAIN_ACK; id",
   "tmux show-environment -t =queue-fixture-runtime GC_DRAIN_ACK\n",
   "tmux set-environment -t =queue-fixture-runtime GC_DRAIN_ACK '$(id)'",
   "tmux show-environment -t =queue-fixture-runtime GC_DRAIN_ACK extra",
   "tmux source-file /tmp/a"]:
   with self.subTest(raw=raw),self.assertRaises(ValueError):m.parse(raw)

 def test_transport_owned_socket_parent_refused(self):
  def info(p):
   if str(p)=="/fixture/private/tmux.sock":return SimpleNamespace(st_mode=stat.S_IFSOCK|0o600,st_uid=987)
   uid=987 if str(p)=="/fixture/private" else 0
   return SimpleNamespace(st_mode=stat.S_IFDIR|0o700,st_uid=uid)
  with mock.patch.object(pathlib.Path,"lstat",info),self.assertRaises(ValueError):
   m.socket_path("/fixture/private/tmux.sock")
 def test_root_protected_socket_accepted(self):
  def info(p):
   return SimpleNamespace(st_mode=(stat.S_IFSOCK|0o600) if p.name=="tmux.sock" else (stat.S_IFDIR|0o750),st_uid=987 if p.name=="tmux.sock" else 0)
  with mock.patch.object(pathlib.Path,"lstat",info):
   self.assertEqual(m.socket_path("/fixture/private/tmux.sock"),"/fixture/private/tmux.sock")

if __name__=="__main__":unittest.main()
