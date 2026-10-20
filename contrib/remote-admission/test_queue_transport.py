import copy
import datetime
from unittest import mock
import importlib.util
import io
import json
import pathlib
import tempfile
import unittest
import sys

HERE=pathlib.Path(__file__).parent
spec=importlib.util.spec_from_file_location("queue_proxy",HERE/"queue_proxy.py")
q=importlib.util.module_from_spec(spec)
sys.modules["queue_proxy"]=q
try: spec.loader.exec_module(q)
except FileNotFoundError: raise

class QueueTransportTests(unittest.TestCase):
 def frame(self):
  return {"schema_version":1,"profile_id":"fixture-profile","identity":{"sid":"session-1","template":"worker","runtime_handle":"worker-session1","generation":"1","instance_token":"a"*32,"attempt":"b"*32},"observe":{"receipt_id":"fixture-receipt","revision":"4","boot_id":"boot-1","unit":"fixture.service","cgroup":"/fixture/unit","invocation_id":"c"*32,"model_uid":1201,"pid":40,"start":"60","phase":"entered","source_sha":"d"*40,"manifest_sha":"e"*64,"observed_at":"2026-10-20T22:00:00Z"}}
 def test_strict_json_rejects_duplicates_unknown_size(self):
  for data in [b'{"x":1,"x":2}',b'{} {}',b'x'*65537]:
   with self.assertRaises(q.Denied):q.strict_json(data)
 def test_request_allowlist_rejects_shell_flags_ids_and_unscoped_mutations(self):
  valid={"request_id":"req1","op":"result","bead_id":"fixture-1","text":"checked","evidence_sha":"f"*64,"outcome":"no-op"}
  q.validate_request(valid)
  for mutate in [lambda d:d.update(op="exec"),lambda d:d.update(command="gc bd close"),lambda d:d.update(bead_id="../x"),lambda d:d.update(evidence_sha=""),lambda d:d.update(text="x"*16385),lambda d:d.update(city="other")]:
   d=copy.deepcopy(valid);mutate(d)
   with self.assertRaises(q.Denied):q.validate_request(d)
 def test_observe_is_bound_to_profile_exact_incarnation_and_peer(self):
  f=self.frame();policy={"profile_id":"fixture-profile","model_uid":1201,"source_sha":"d"*40,"manifest_sha":"e"*64}
  peer={"pid":44,"uid":1201,"gid":1201,"start":"80","cgroup":"/fixture/unit"}
  q.validate_observation(f,policy,peer)
  for mutate in [lambda x:x['identity'].update(sid="other"),lambda x:x['identity'].update(attempt="0"*32),lambda x:x['observe'].update(phase="exited"),lambda x:x['observe'].update(cgroup="/other"),lambda x:x['observe'].update(model_uid=0),lambda x:x['observe'].update(source_sha="0"*40)]:
   got=copy.deepcopy(f);mutate(got)
   with self.assertRaises(q.Denied):q.validate_observation(got,policy,peer,expected=f)
 def test_peer_start_and_cgroup_change_deny(self):
  a={"pid":44,"uid":1201,"gid":1201,"start":"80","cgroup":"/fixture/unit"}
  q.same_peer(a,dict(a))
  for k,v in [('pid',45),('uid',0),('start','81'),('cgroup','/other')]:
   b=dict(a);b[k]=v
   with self.assertRaises(q.Denied):q.same_peer(a,b)
 def test_intake_intent_never_replays_unknown_native_effect(self):
  with tempfile.TemporaryDirectory() as tmp:
   j=q.RequestJournal(pathlib.Path(tmp),owner_uid=None)
   req={"request_id":"r1","op":"claim"};scope="fixture"
   self.assertIsNone(j.load(scope,req))
   j.intent(scope,req)
   self.assertEqual(j.load(scope,req),{"state":"uncertain"})
   j.finish(scope,req,{"schema_version":1,"request_id":"r1","state":"claimed","bead_id":"fixture-1"})
   self.assertEqual(j.load(scope,req)['state'],'claimed')
   with self.assertRaises(q.Denied):j.load(scope,{"request_id":"r1","op":"self_drain"})
 def test_controller_passes_only_private_proof_and_drops_to_city_uid(self):
  spec=importlib.util.spec_from_file_location("controller_relay",HERE/"controller_relay.py")
  c=importlib.util.module_from_spec(spec);spec.loader.exec_module(c)
  f=self.frame();f['request']={"request_id":"r1","op":"current"};f['peer']={"pid":44,"uid":1201,"gid":1201,"start":"80","cgroup":"/fixture/unit"}
  captured=[]
  out=c.process(f,{"profile_id":"fixture-profile","model_uid":1201,"source_sha":"d"*40,"manifest_sha":"e"*64},observe=lambda expected: self.frame(),execute=lambda proof:captured.append(proof) or {"schema_version":1,"request_id":"r1","state":"claimed","bead_id":"fixture-1"})
  self.assertEqual(out['request_id'],'r1');self.assertNotIn('peer',captured[0]);self.assertEqual(captured[0]['identity'],f['identity'])
  wrong=self.frame();wrong['identity']['attempt']='0'*32
  with self.assertRaises(q.Denied):c.process(f,{"profile_id":"fixture-profile","model_uid":1201,"source_sha":"d"*40,"manifest_sha":"e"*64},observe=lambda expected:wrong,execute=lambda proof:self.fail('executed stale proof'))

 def test_proxy_peercredential_and_restart_race_prevent_outbound(self):
  f=self.frame();f['observe']['observed_at']=datetime.datetime.now(datetime.timezone.utc).isoformat()
  peer={"pid":44,"uid":1201,"gid":1201,"start":"80","cgroup":"/fixture/unit"}
  class Socket:
   def __init__(self,uid):self.uid=uid;self.parts=[b'{"request_id":"r1","op":"claim"}',b''];self.sent=[]
   def settimeout(self,n):pass
   def getsockopt(self,*a):return q.struct.pack('3i',44,self.uid,1201)
   def recv(self,n):return self.parts.pop(0)
   def sendall(self,b):self.sent.append(b)
  config={"profile_id":"fixture-profile","model_uid":1201,"source_sha":"d"*40,"manifest_sha":"e"*64,"ssh_key_path":"/private/key"}
  with self.assertRaises(q.Denied):q.proxy_one(Socket(1202),config,observer=lambda c,**kw:self.fail('wrong UID observed'))
  peers=[peer,dict(peer,start='81')]
  with self.assertRaises(q.Denied):q.proxy_one(Socket(1201),config,observer=lambda c,**kw:f,peer_reader=lambda *a:peers.pop(0),runner=lambda *a,**k:self.fail('restart reached relay'))
  sock=Socket(1201);calls=[]
  with mock.patch.object(q,'root_file',return_value=b''),mock.patch.object(q,'command',return_value=['/fixed/ssh']):
   q.proxy_one(sock,config,observer=lambda c,**kw:f,peer_reader=lambda *a:peer,runner=lambda argv,data,**kw:calls.append(q.strict_json(data)) or b'{"schema_version":1,"request_id":"r1","state":"idle"}')
  self.assertEqual(calls[0]['peer'],peer);self.assertEqual(calls[0]['identity'],f['identity']);self.assertEqual(q.strict_json(sock.sent[0])['state'],'idle')

 def test_journal_symlink_and_scope_lock_are_failclosed(self):
  with tempfile.TemporaryDirectory() as tmp:
   j=q.RequestJournal(pathlib.Path(tmp),owner_uid=None);r={"request_id":"r1","op":"current"};scope='s'
   outside=pathlib.Path(tmp)/'outside';outside.write_text('{}')
   (pathlib.Path(tmp)/(j.key(scope,r)+'.intent')).symlink_to(outside)
   with self.assertRaises((q.Denied,OSError)):j.load(scope,r)
   with j.lock(scope):
    with self.assertRaises(q.Denied):
     with j.lock(scope):self.fail('second owner')

 def test_child_fd_remap_and_privilege_drop_keep_parent_lock_untouched(self):
  spec=importlib.util.spec_from_file_location("controller_relay",HERE/"controller_relay.py")
  c=importlib.util.module_from_spec(spec);spec.loader.exec_module(c)
  class OS:
   def __init__(self):self.calls=[];self.uid=0
   def dup2(self,a,b,**kw):self.calls.append(('dup',a,b))
   def close(self,fd):self.calls.append(('close',fd))
   def setgroups(self,groups):self.calls.append(('groups',groups))
   def setgid(self,gid):self.calls.append(('gid',gid))
   def setuid(self,uid):self.calls.append(('uid',uid));self.uid=uid
   def getuid(self):return self.uid
   def geteuid(self):return self.uid
   def _exit(self,n):raise AssertionError('failed UID drop')
  ops=OS();c.child_setup(5,1301,1302,ops)
  self.assertEqual(ops.calls,[('dup',5,3),('close',5),('groups',[]),('gid',1302),('uid',1301)])
  ops=OS();c.child_setup(3,1301,1302,ops)
  self.assertEqual(ops.calls,[('groups',[]),('gid',1302),('uid',1301)])

 def test_observe_command_receives_kernel_peer_and_private_expected_binding(self):
  peer={"pid":44,"uid":1201,"gid":1201,"start":"80","cgroup":"/fixture/unit"};calls=[]
  config={'profile_id':'fixture-profile'};frame=self.frame()
  with mock.patch.object(q,'command',return_value=['/pinned/helper','queue-observe']),mock.patch.object(q,'bounded_command',side_effect=lambda argv,data,**kw:calls.append(q.strict_json(data)) or q.encode(frame)):
   q.observe(config,peer=peer)
   q.observe(config,expected=dict(frame,peer=peer))
   with self.assertRaises(q.Denied):q.observe(config)
  self.assertEqual(calls[0],{'schema_version':1,'profile_id':'fixture-profile','peer':peer})
  self.assertEqual(calls[1]['expected_identity'],frame['identity'])

 def test_parser_depth_limit_refuses_without_uncaught_recursion(self):
  for n in (32,16000):
   data=b'{"extra":'+b'['*n+b'0'+b']'*n+b'}'
   with self.assertRaises(q.Denied):q.strict_json(data)
  self.assertEqual(q.strict_json(b'{"request_id":"good","op":"claim"}')['op'],'claim')
 def test_total_peer_frame_deadline_not_per_byte_idle_timeout(self):
  class Socket:
   def __init__(self):self.timeout=[]
   def settimeout(self,n):self.timeout.append(n)
   def getsockopt(self,*a):return q.struct.pack('3i',44,1201,1201)
   def recv(self,n):return b'x'
  peer={"pid":44,"uid":1201,"gid":1201,"start":"80","cgroup":"/fixture/unit"};sock=Socket()
  with mock.patch.object(q.time,'monotonic',side_effect=[0,1,3,6]):
   with self.assertRaises(q.Denied):q.proxy_one(sock,{'model_uid':1201},peer_reader=lambda *a:peer,observer=lambda *a,**k:self.fail('slow frame observed'))
  self.assertEqual(sock.timeout,[5,4,2])
 def test_new_request_cannot_escape_pending_scope(self):
  with tempfile.TemporaryDirectory() as tmp:
   j=q.RequestJournal(pathlib.Path(tmp),owner_uid=None);old={'request_id':'first','op':'claim'};j.intent('scope',old)
   with self.assertRaises(q.Denied):j.load('scope',{'request_id':'new','op':'claim'})
   with self.assertRaises(q.Denied):j.intent('scope',{'request_id':'new','op':'claim'})
   self.assertEqual(j.load('scope',old),{'state':'uncertain'})
   with self.assertRaises(q.Denied):j.finish('scope',old,{'schema_version':1,'request_id':'first','state':'invented'})
   with self.assertRaises(q.Denied):j.load('scope',{'request_id':'new','op':'claim'})
   j.finish('scope',old,{'schema_version':1,'request_id':'first','state':'idle'})
   self.assertIsNone(j.load('scope',{'request_id':'new','op':'claim'}))
 def test_intake_does_not_finish_malformed_sdk_response(self):
  spec=importlib.util.spec_from_file_location('controller_relay',HERE/'controller_relay.py');c=importlib.util.module_from_spec(spec);spec.loader.exec_module(c)
  frame=self.frame();frame['request']={'request_id':'first','op':'claim'};frame['peer']={'pid':44,'uid':1201,'gid':1201,'start':'80','cgroup':'/fixture/unit'}
  config={'profile_id':'fixture-profile','model_uid':1201,'source_sha':'d'*40,'manifest_sha':'e'*64,'max_requests':64}
  with tempfile.TemporaryDirectory() as tmp:
   config['journal_dir']=tmp;j=q.RequestJournal(pathlib.Path(tmp),owner_uid=None)
   with mock.patch.object(q,'RequestJournal',return_value=j),mock.patch.object(q,'observe',return_value=self.frame()),mock.patch.object(q,'validate_observation'),mock.patch.object(c,'run_sdk',return_value={'schema_version':1,'request_id':'first','state':'invented'}):
    with self.assertRaises(q.Denied):c.execute_envelope(frame,config)
   scope='fixture-profile/session-1/'+'b'*32
   self.assertEqual(j.load(scope,frame['request']),{'state':'uncertain'})
   with self.assertRaises(q.Denied):j.load(scope,{'request_id':'next','op':'claim'})

 def test_malformed_completed_record_does_not_release_scope(self):
  with tempfile.TemporaryDirectory() as tmp:
   j=q.RequestJournal(pathlib.Path(tmp),owner_uid=None);r={'request_id':'first','op':'claim'};j.intent('s',r)
   forged={'digest':j.digest(r),'request_id':'first','op':'claim','bead_id':'','response':{'schema_version':1,'request_id':'first','state':'invented'}}
   j.write(pathlib.Path(tmp)/(j.key('s',r)+'.receipt'),forged)
   with self.assertRaises(q.Denied):j.load('s',{'request_id':'second','op':'claim'})
   with self.assertRaises(q.Denied):j.load('s',r)


 def test_socket_parent_rejects_writable_or_nonroot_ancestor(self):
  # No root setup effects: fake open descriptors model root->unsafe->root750.
  for uid,mode in [(1201,0o40755),(0,0o40777)]:
   info=type('Stat',(),{'st_uid':uid,'st_gid':1201,'st_mode':mode})()
   with mock.patch.object(q.os,'open',side_effect=[10,11]),mock.patch.object(q.os,'close'),mock.patch.object(q.os,'fstat',return_value=info):
    with self.assertRaises(q.Denied):q.socket_parent('/unsafe/socket',1201)
 def test_socket_mode_refuses_symlink_before_any_ownership_effect(self):
  info=type('Stat',(),{'st_uid':0,'st_mode':0o120777})()
  with mock.patch.object(q.os,'open',return_value=20),mock.patch.object(q.os,'close'),mock.patch.object(q.os,'fstat',return_value=info),mock.patch.object(q.os,'chown') as chown,mock.patch.object(q.os,'chmod') as chmod:
   with self.assertRaises(q.Denied):q.secure_socket_mode(10,'queue.sock',1201)
   chown.assert_not_called();chmod.assert_not_called()

if __name__=='__main__':unittest.main()
