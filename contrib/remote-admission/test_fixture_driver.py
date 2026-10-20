import importlib.util
import io
import pathlib
from types import SimpleNamespace
from unittest import mock
import unittest

spec=importlib.util.spec_from_file_location("fixture_driver",pathlib.Path(__file__).with_name("fixture_driver.py"))
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)

class DriverTests(unittest.TestCase):
 def test_entry_barrier_requires_root_sender(self):
  with mock.patch.object(m.signal,"pthread_sigmask"),mock.patch.object(m.signal,"sigtimedwait",return_value=SimpleNamespace(si_uid=0)):
   m.await_entry()
  for result in [None,SimpleNamespace(si_uid=988)]:
   with mock.patch.object(m.signal,"pthread_sigmask"),mock.patch.object(m.signal,"sigtimedwait",return_value=result),self.assertRaises(ValueError):
    m.await_entry()
 def test_no_queue_request_before_auth_denial_and_entry(self):
  events=[]
  with mock.patch.object(m,"assert_auth_inaccessible",side_effect=lambda:events.append("auth")),mock.patch.object(m,"await_entry",side_effect=lambda:events.append("entry")),mock.patch.object(m,"cycle",side_effect=lambda gc:events.append("queue") or {"ok":True}):
   self.assertEqual(m.guarded_cycle("/immutable/gc")["oauth_denied"],True)
  self.assertEqual(events,["auth","entry","queue"])
 def test_accessible_auth_refuses_before_queue_or_entry(self):
  with mock.patch.object(m,"assert_auth_inaccessible",side_effect=ValueError("denial unproved")),mock.patch.object(m,"await_entry") as enter,mock.patch.object(m,"cycle") as queue:
   with self.assertRaises(ValueError):m.guarded_cycle("/immutable/gc")
   enter.assert_not_called();queue.assert_not_called()
 def test_auth_denial_uses_only_access_and_stat(self):
  with mock.patch.object(m.os,"access",return_value=False),mock.patch.object(m.os,"stat",side_effect=PermissionError):
   m.assert_auth_inaccessible()
  for access,error in [(True,PermissionError),(False,FileNotFoundError),(False,None)]:
   with mock.patch.object(m.os,"access",return_value=access),mock.patch.object(m.os,"stat",side_effect=error),self.assertRaises(ValueError):
    m.assert_auth_inaccessible()
 def test_transport_error_is_not_retried_or_exposed(self):
  with mock.patch.object(m.subprocess,"run",return_value=SimpleNamespace(returncode=1,stdout=b"private",stderr=b"private")) as run:
   with self.assertRaisesRegex(ValueError,"^claim$"):m.invoke("/immutable/gc","claim",["hook","--claim","--json"])
   self.assertEqual(run.call_count,1)

if __name__=="__main__":unittest.main()
