import importlib.util
import io
import os
import pathlib
import sys
import unittest
from unittest import mock
HERE=pathlib.Path(__file__).parent
spec=importlib.util.spec_from_file_location('queue_client',HERE/'queue_client.py');c=importlib.util.module_from_spec(spec);spec.loader.exec_module(c)
class ClientTests(unittest.TestCase):
 def test_exact_command_mapping_and_no_authority_selectors(self):
  cases=[(['gc','hook','--claim','--json'],'claim'),(['gc','hook','current','--id-only'],'current'),(['gc','bd','show','fixture-1','--json'],'read'),(['bd','close','fixture-1','--reason','verified','--evidence-sha','a'*64],'close'),(['gc','remote-work','result','fixture-1','--outcome','no-op','--text','checked','--evidence-sha','a'*64],'result'),(['gc','runtime','drain-ack'],'self_drain')]
  for argv,op in cases:
   r,_,_=c.parse_command(argv,request_id='test');self.assertEqual(r['op'],op);self.assertNotIn('sid',r)
  for argv in [['gc','session','new','worker'],['gc','--city','other','hook'],['bd','delete','fixture-1'],['gc','bd','show','fixture-1','--json','--rig','foreign'],['gc','hook','--inject']]:
   with mock.patch('sys.stderr',io.StringIO()):
    with self.assertRaises((SystemExit,c.q.Denied)):c.parse_command(argv)
 def test_no_retry_or_drain_after_unknown_claim(self):
  r,mode,drain=c.parse_command(['gc','hook','--claim','--json','--drain-ack'],request_id='r1');seen=[]
  def fail(req):seen.append(req);raise c.q.Denied()
  with self.assertRaises(c.q.Denied):c.invoke(r,mode,drain,fail,io.StringIO())
  self.assertEqual(len(seen),1)
 def test_idle_and_work_hook_protocol_are_truthful(self):
  r,mode,drain=c.parse_command(['gc','hook','--claim','--json','--drain-ack'],request_id='r1');seen=[];out=io.StringIO()
  def send(req):
   seen.append(req)
   return {'schema_version':1,'request_id':req['request_id'],'state':'idle' if req['op']=='claim' else 'drain_acknowledged'}
  self.assertEqual(c.invoke(r,mode,drain,send,out),0);self.assertEqual(seen[1],{'request_id':'r1.drain','op':'self_drain'});self.assertTrue(c.q.strict_json(out.getvalue().encode())['drain_acknowledged'])
 def test_callback_lane_never_claims(self):
  r,mode,drain=c.parse_command(['gc','hook','--claim','--json'],request_id='r')
  with mock.patch.dict(os.environ,{'GC_MANAGED_SESSION_HOOK':'1'}):
   with self.assertRaises(c.q.Denied):c.invoke(r,mode,drain,lambda r:self.fail('callback reached queue'),io.StringIO())
 def test_explicit_same_request_id_preserved(self):
  r,_,_=c.parse_command(['gc','--request-id','same-id','bd','show','fixture-1','--json']);self.assertEqual(r['request_id'],'same-id')
if __name__=='__main__':unittest.main()
