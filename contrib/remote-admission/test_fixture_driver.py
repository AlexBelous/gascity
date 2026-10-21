import importlib.util
import io
import json
import os
import signal
import select
import subprocess
import sys
import time
import pathlib
from types import SimpleNamespace
from unittest import mock
import unittest

spec=importlib.util.spec_from_file_location("fixture_driver",pathlib.Path(__file__).with_name("fixture_driver.py"))
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)

class DriverTests(unittest.TestCase):
 def setUp(self):
  clock=mock.patch.object(m,"time",time,create=True);clock.start();self.addCleanup(clock.stop)
 def test_entry_barrier_requires_root_sender_and_zero_timeout_consume(self):
  for uid in (0,os.geteuid(),None):
   with mock.patch.object(m.signal,"pthread_sigmask"),mock.patch.object(m.signal,"sigpending",return_value={signal.SIGUSR1}),mock.patch.object(m.signal,"sigtimedwait",return_value=SimpleNamespace(si_uid=uid) if uid is not None else None) as consume:
    if uid==0:m.await_entry()
    else:
     with self.assertRaisesRegex(ValueError,"entry barrier"):m.await_entry()
    consume.assert_called_once_with({signal.SIGUSR1},0)
 def test_pending_signal_after_deadline_is_not_consumed(self):
  for clock in ([0,31],[0,0,31]):
   with self.subTest(clock=clock),mock.patch.object(m.signal,"pthread_sigmask"),mock.patch.object(m.time,"monotonic",side_effect=clock),mock.patch.object(m.signal,"sigpending",return_value={signal.SIGUSR1}),mock.patch.object(m.signal,"sigtimedwait",return_value=SimpleNamespace(si_uid=0)) as consume:
    with self.assertRaisesRegex(ValueError,"entry barrier"):m.await_entry()
    consume.assert_not_called()
 def test_deadline_after_consume_refuses_before_queue(self):
  with mock.patch.object(m,"assert_auth_inaccessible"),mock.patch.object(m.signal,"pthread_sigmask"),mock.patch.object(m.time,"monotonic",side_effect=[0,0,0,31]),mock.patch.object(m.signal,"sigpending",return_value={signal.SIGUSR1}),mock.patch.object(m.signal,"sigtimedwait",return_value=SimpleNamespace(si_uid=0)) as consume,mock.patch.object(m,"cycle") as queue:
   with self.assertRaisesRegex(ValueError,"entry barrier"):m.guarded_cycle("/immutable/gc")
   consume.assert_called_once_with({signal.SIGUSR1},0);queue.assert_not_called()
 def test_wait_timeout_never_consumes_or_enters_queue(self):
  events=[]
  with mock.patch.object(m,"assert_auth_inaccessible"),mock.patch.object(m.signal,"pthread_sigmask"),mock.patch.object(m.signal,"sigpending",return_value=set()),mock.patch.object(m.signal,"sigtimedwait") as consume,mock.patch.object(m.time,"monotonic",side_effect=[0,0,0,31]),mock.patch.object(m.time,"sleep",side_effect=events.append),mock.patch.object(m,"cycle") as queue:
   with self.assertRaisesRegex(ValueError,"entry barrier"):m.guarded_cycle("/immutable/gc")
   consume.assert_not_called();queue.assert_not_called()
   self.assertEqual(events,[.01])
 def native_wait(self,send):
  if os.geteuid()==0:self.skipTest("native wrong-sender proof needs an unprivileged owner")
  child=r"""
import importlib.util,json,pathlib,signal,sys,time
spec=importlib.util.spec_from_file_location("driver",sys.argv[1])
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
m.time=time
real_clock=m.time.monotonic
m.time.monotonic=lambda:real_clock()*60
real_block=m.signal.pthread_sigmask
def blocked(*a):
 value=real_block(*a)
 print("READY",flush=True)
 return value
m.signal.pthread_sigmask=blocked
try:
 m.await_entry()
 result="accepted"
except ValueError:
 result="refused"
mask=real_block(signal.SIG_BLOCK,set())
print(json.dumps({"result":result,"blocked":signal.SIGUSR1 in mask}),flush=True)
"""
  p=subprocess.Popen([sys.executable,"-I","-B","-c",child,str(pathlib.Path(m.__file__))],stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
  samples=[]
  try:
   self.assertTrue(select.select([p.stdout],[],[],2)[0])
   self.assertEqual(p.stdout.readline(),"READY\n")
   proc=pathlib.Path("/proc")/str(p.pid)
   start=(proc/"stat").read_text().rsplit(")",1)[1].split()[19]
   sent=False
   for unused in range(150):
    if p.poll() is not None:break
    try:
     status=dict(x.split(":",1) for x in (proc/"status").read_text().splitlines() if ":" in x)
     self.assertEqual(status["Uid"].split(),[str(os.geteuid())]*4)
     self.assertEqual((proc/"stat").read_text().rsplit(")",1)[1].split()[19],start)
     samples.append(bool(int(status["SigBlk"].strip(),16)&(1<<(signal.SIGUSR1-1))))
    except FileNotFoundError:
     self.assertIsNotNone(p.poll());break
    if send and len(samples)==12:
     os.kill(p.pid,signal.SIGUSR1);sent=True # own unprivileged child only
    time.sleep(.01)
   self.assertGreaterEqual(len(samples),12)
   self.assertEqual(sent,send)
   out,err=p.communicate(timeout=2)
   self.assertEqual((p.returncode,err),(0,""))
   self.assertEqual(json.loads(out),{"result":"refused","blocked":True})
   self.assertTrue(all(samples),samples)
  finally:
   if p.poll() is None:p.terminate();p.communicate(timeout=2)
   p.stdout.close();p.stderr.close()
 def test_native_mask_visible_and_nonroot_signal_refused(self):
  self.native_wait(True)
 def test_native_mask_visible_and_timeout_refused(self):
  self.native_wait(False)
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
