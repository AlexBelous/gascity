#!/usr/bin/python3
"""Root-owned Unix queue proxy. No model, SSH key, or root DB access is delegated.

Installation is separate from source delivery. Each root-owned private config
pins one profile, active Observe command, transport command and credential path.
The root operator and reviewed pinned helpers are the explicit trust boundary.
"""
import argparse
import contextlib
import datetime
import fcntl
import hashlib
import json
import os
import pathlib
import re
import selectors
import signal
import socket
import stat
import struct
import subprocess
import sys
import time

LIMIT=65536
ID=re.compile(r"[A-Za-z0-9][A-Za-z0-9_.:-]{0,159}\Z")
HEX32=re.compile(r"[0-9a-f]{32}\Z")
class Denied(Exception):
    """A deliberately opaque fail-closed refusal."""

def strict_json(data):
    if not isinstance(data,bytes) or not data or len(data)>LIMIT: raise Denied()
    def pairs(items):
        out={}
        for key,value in items:
            if key in out: raise Denied()
            out[key]=value
        return out
    try:
        value=json.loads(data,object_pairs_hook=pairs,parse_constant=lambda _: (_ for _ in ()).throw(Denied()))
    except (ValueError,UnicodeError,TypeError,RecursionError): raise Denied() from None
    if not isinstance(value,dict): raise Denied()
    stack=[(value,0)]
    while stack:
        item,depth=stack.pop()
        if depth>24:raise Denied()
        if isinstance(item,dict):stack.extend((child,depth+1) for child in item.values() if isinstance(child,(dict,list)))
        elif isinstance(item,list):stack.extend((child,depth+1) for child in item if isinstance(child,(dict,list)))
    return value

def exact(value,keys,optional=()):
    if not isinstance(value,dict) or set(value)-set(keys)-set(optional) or set(keys)-set(value): raise Denied()

def decimal(value):
    return isinstance(value,str) and value.isascii() and value.isdigit() and value==str(int(value)) and int(value)>0

def validate_request(r):
    exact(r,('request_id','op'),('bead_id','text','evidence_sha','outcome'))
    if not isinstance(r['request_id'],str) or not ID.fullmatch(r['request_id']): raise Denied()
    if r['op'] in ('claim','current','self_drain'):
        if set(r)!= {'request_id','op'}: raise Denied()
    elif r['op']=='read':
        if set(r)!= {'request_id','op','bead_id'} or not isinstance(r['bead_id'],str) or not ID.fullmatch(r['bead_id']): raise Denied()
    elif r['op'] in ('result','close'):
        if set(r)!= ({'request_id','op','bead_id','text','evidence_sha','outcome'} if r['op']=='result' else {'request_id','op','bead_id','text','evidence_sha'}): raise Denied()
        if r['op']=='result' and r['outcome'] not in ('shipped','no-op','blocked','abandoned'):raise Denied()
        if not isinstance(r['bead_id'],str) or not ID.fullmatch(r['bead_id']): raise Denied()
        if not isinstance(r['text'],str) or not r['text'].strip() or '\x00' in r['text'] or len(r['text'].encode())>16384: raise Denied()
        if not isinstance(r['evidence_sha'],str) or not re.fullmatch('[0-9a-f]{64}',r['evidence_sha']): raise Denied()
    else: raise Denied()

def validate_response(out,request):
    exact(out,('schema_version','request_id','state'),('bead_id','bead'))
    allowed={'claim':('idle','claimed'),'current':('claimed',),'read':('read',),'result':('recorded',),'close':('closed',),'self_drain':('drain_acknowledged',)}
    if out['schema_version']!=1 or out['request_id']!=request['request_id'] or out['state'] not in allowed.get(request['op'],()):raise Denied()
    if out['state'] in ('idle','drain_acknowledged'):
        if 'bead_id' in out or 'bead' in out:raise Denied()
    else:
        if not isinstance(out.get('bead_id'),str) or not ID.fullmatch(out['bead_id']) or 'bead_id' in request and request['bead_id']!=out['bead_id']:raise Denied()
    if 'bead' in out:
        q=out['bead'];exact(q,('id','title','description','status','assignee'))
        if q['id']!=out.get('bead_id') or not all(isinstance(v,str) for v in q.values()) or len(q['description'].encode())>32768 or len(q['title'].encode())>16384:raise Denied()

def same_peer(before,after):
    if before!=after: raise Denied()

def validate_observation(frame,policy,peer,expected=None,now=None):
    exact(frame,('schema_version','profile_id','identity','observe'))
    if frame['schema_version']!=1 or frame['profile_id']!=policy['profile_id']: raise Denied()
    i=frame['identity'];o=frame['observe']
    exact(i,('sid','template','runtime_handle','generation','instance_token','attempt'))
    exact(o,('receipt_id','revision','boot_id','unit','cgroup','invocation_id','model_uid','pid','start','phase','source_sha','manifest_sha','observed_at'))
    if not isinstance(i['sid'],str) or not ID.fullmatch(i['sid']) or not decimal(i['generation']): raise Denied()
    if not isinstance(i['instance_token'],str) or not HEX32.fullmatch(i['instance_token']) or not isinstance(i['attempt'],str) or not HEX32.fullmatch(i['attempt']): raise Denied()
    if not all(isinstance(i[k],str) and i[k] and not any(c in i[k] for c in '\x00\r\n') for k in ('template','runtime_handle')): raise Denied()
    if o['phase']!='entered' or o['model_uid']!=policy['model_uid'] or o['source_sha']!=policy['source_sha'] or o['manifest_sha']!=policy['manifest_sha']: raise Denied()
    if type(o['model_uid']) is not int or type(o['pid']) is not int or o['pid']<=1 or not decimal(o['start']) or not decimal(o['revision']): raise Denied()
    if not all(isinstance(o[k],str) and o[k] for k in ('receipt_id','boot_id','unit','cgroup','invocation_id','observed_at')): raise Denied()
    if not o['cgroup'].startswith('/') or '..' in o['cgroup'].split('/'): raise Denied()
    exact(peer,('pid','uid','gid','start','cgroup'))
    if type(peer['pid']) is not int or peer['pid']<=1 or type(peer['uid']) is not int or peer['uid']!=policy['model_uid'] or peer['uid']<=0 or not decimal(peer['start']) or peer['cgroup']!=o['cgroup']: raise Denied()
    if expected is not None:
        if i!=expected['identity']: raise Denied()
        for key in ('receipt_id','boot_id','unit','cgroup','invocation_id','model_uid','pid','start','source_sha','manifest_sha'):
            if o[key]!=expected['observe'][key]: raise Denied()
        if int(o['revision'])<int(expected['observe']['revision']): raise Denied()
    if now is not None:
        try: age=(now-datetime.datetime.fromisoformat(o['observed_at'].replace('Z','+00:00'))).total_seconds()
        except (ValueError,TypeError): raise Denied() from None
        if age < -2 or age >20: raise Denied()

def root_file(path,max_bytes=LIMIT,private=False,expected_sha=None):
    p=pathlib.Path(path)
    if not p.is_absolute() or str(p)!=os.path.normpath(str(p)): raise Denied()
    initial=p.lstat()
    for parent in (p,*p.parents):
        s=parent.lstat()
        if stat.S_ISLNK(s.st_mode) or s.st_uid!=0 or s.st_mode&0o022: raise Denied()
        if parent==p:
            if not stat.S_ISREG(s.st_mode) or s.st_nlink!=1 or private and s.st_mode&0o077: raise Denied()
        elif not stat.S_ISDIR(s.st_mode): raise Denied()
    fd=os.open(p,os.O_RDONLY|os.O_NOFOLLOW|os.O_CLOEXEC)
    try:
        s2=os.fstat(fd)
        if (s2.st_dev,s2.st_ino)!=(initial.st_dev,initial.st_ino): raise Denied()
        if s2.st_uid!=0 or not stat.S_ISREG(s2.st_mode) or s2.st_nlink!=1 or s2.st_mode&0o022 or private and s2.st_mode&0o077: raise Denied()
        chunks=[];size=0
        while True:
            chunk=os.read(fd,min(65536,max_bytes-size+1))
            if not chunk: break
            size+=len(chunk)
            if size>max_bytes: raise Denied()
            chunks.append(chunk)
        data=b''.join(chunks)
    finally: os.close(fd)
    if expected_sha and hashlib.sha256(data).hexdigest()!=expected_sha: raise Denied()
    return data

def command(config,key):
    argv=config[key]
    if not isinstance(argv,list) or not argv or not all(isinstance(x,str) and '\x00' not in x for x in argv) or not os.path.isabs(argv[0]): raise Denied()
    pins=config.get('command_pins',{})
    if argv[0] not in pins: raise Denied()
    root_file(argv[0],64<<20,expected_sha=pins[argv[0]])
    return argv

def collect_process(p,data,writer,timeout):
    out=bytearray();err=bytearray();sel=selectors.DefaultSelector();deadline=time.monotonic()+timeout;offset=0
    try:
        if writer is not None:
            os.set_blocking(writer.fileno(),False)
            if data:sel.register(writer,selectors.EVENT_WRITE)
            else:writer.close();writer=None
        for stream in (p.stdout,p.stderr):os.set_blocking(stream.fileno(),False);sel.register(stream,selectors.EVENT_READ)
        while sel.get_map():
            remain=deadline-time.monotonic()
            if remain<=0: raise Denied()
            for key,mask in sel.select(min(remain,0.2)):
                if mask&selectors.EVENT_WRITE:
                    offset+=os.write(key.fileobj.fileno(),memoryview(data)[offset:])
                    if offset==len(data):sel.unregister(key.fileobj);key.fileobj.close();writer=None
                    continue
                chunk=os.read(key.fileobj.fileno(),4096)
                if not chunk:sel.unregister(key.fileobj);continue
                target=out if key.fileobj==p.stdout else err;target.extend(chunk)
                if len(out)>LIMIT or len(err)>4096:raise Denied()
        if p.wait(timeout=max(0.01,deadline-time.monotonic()))!=0:raise Denied()
        return bytes(out)
    except (OSError,ValueError,subprocess.TimeoutExpired,Denied):
        if p.poll() is None:os.killpg(p.pid,signal.SIGKILL)
        p.wait();raise Denied() from None
    finally:
        sel.close()
        for stream in (writer,p.stdout,p.stderr):
            if stream:stream.close()

def bounded_command(argv,data,timeout=15,preexec=None,pass_fds=(),env=None):
    # Only this newly created process group is terminated on timeout/overflow.
    p=subprocess.Popen(argv,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,env=env or {'PATH':'/usr/bin:/bin','LANG':'C.UTF-8'},preexec_fn=preexec,pass_fds=pass_fds,start_new_session=True)
    return collect_process(p,data,p.stdin,timeout)

def encode(value):
    raw=json.dumps(value,separators=(',',':'),ensure_ascii=False,sort_keys=True).encode()
    if len(raw)>LIMIT: raise Denied()
    return raw

def observe(config,expected=None,peer=None):
    # The fixed rootguard command derives active identity from its private state;
    # no model-supplied identity or claimed healthy boolean can select a record.
    request={'schema_version':1,'profile_id':config['profile_id']}
    if expected is not None:request['expected_identity']=expected['identity'];request['peer']=expected['peer']
    elif peer is not None:request['peer']=peer
    else:raise Denied()
    return strict_json(bounded_command(command(config,'observe_command'),encode(request),timeout=10))

def read_peer(pid,uid,gid):
    if pid<=1 or uid<=0: raise Denied()
    base=pathlib.Path('/proc')/str(pid)
    raw=(base/'stat').read_text();at=raw.rfind(')');fields=raw[at+1:].split()
    if at<0 or len(fields)<20 or not decimal(fields[19]): raise Denied()
    values=None
    for line in (base/'status').read_text().splitlines():
        if line.startswith('Uid:'):values=line.split()[1:]
    if values!=[str(uid)]*4:raise Denied()
    groups=(base/'cgroup').read_text().splitlines()
    if len(groups)!=1 or not groups[0].startswith('0::/'):raise Denied()
    again=(base/'stat').read_text();end=again.rfind(')');last=again[end+1:].split()
    if end<0 or len(last)<20 or last[19]!=fields[19]:raise Denied()
    return {'pid':pid,'uid':uid,'gid':gid,'start':fields[19],'cgroup':groups[0][3:]}

def private_directory(path,owner=0):
    p=pathlib.Path(path);s=p.lstat()
    if not stat.S_ISDIR(s.st_mode) or s.st_uid!=owner or stat.S_IMODE(s.st_mode)!=0o700:raise Denied()
    return p

class RequestJournal:
    def __init__(self,path,owner_uid=0,max_records=64):
        self.path=pathlib.Path(path);self.owner_uid=owner_uid;self.max_records=max_records
        if owner_uid is not None:private_directory(self.path,owner_uid)
    def prefix(self,scope):return hashlib.sha256(scope.encode()).hexdigest()+'-'
    def key(self,scope,request):return self.prefix(scope)+hashlib.sha256(request['request_id'].encode()).hexdigest()
    def digest(self,request):return hashlib.sha256(encode(request)).hexdigest()
    @contextlib.contextmanager
    def lock(self,scope):
        p=self.path/(hashlib.sha256(scope.encode()).hexdigest()+'.lock')
        fd=os.open(p,os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW|os.O_CLOEXEC,0o600)
        try:
            s=os.fstat(fd)
            if s.st_nlink!=1 or not stat.S_ISREG(s.st_mode) or stat.S_IMODE(s.st_mode)!=0o600 or self.owner_uid is not None and s.st_uid!=self.owner_uid:raise Denied()
            try:fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
            except BlockingIOError:raise Denied() from None
            yield
        finally:os.close(fd)
    def read(self,path):
        try:fd=os.open(path,os.O_RDONLY|os.O_NOFOLLOW|os.O_CLOEXEC)
        except FileNotFoundError:return None
        try:
            s=os.fstat(fd)
            if not stat.S_ISREG(s.st_mode) or s.st_nlink!=1 or stat.S_IMODE(s.st_mode)!=0o600 or self.owner_uid is not None and s.st_uid!=self.owner_uid:raise Denied()
            raw=os.read(fd,LIMIT+1)
            return strict_json(raw)
        finally:os.close(fd)
    def can_start(self,scope):
        entries=list(self.path.iterdir())
        if len(entries)>self.max_records*3:raise Denied()
        for path in entries:
            if not path.name.startswith(self.prefix(scope)) or not path.name.endswith('.intent'):continue
            intent=self.read(path);done=self.read(path.with_suffix('.receipt'))
            if intent is None or set(intent)!={'digest','request_id','op','bead_id'} or done is None or set(done)!={'digest','request_id','op','bead_id','response'} or any(done[k]!=intent[k] for k in intent):raise Denied()
            response=done['response']
            if not isinstance(response,dict) or response.get('schema_version')!=1 or not isinstance(response.get('request_id'),str) or not ID.fullmatch(response['request_id']):raise Denied()
            prior={'request_id':intent['request_id'],'op':intent['op']}
            if intent['bead_id']:prior['bead_id']=intent['bead_id']
            validate_response(response,prior)
            if self.key(scope,prior)!=path.stem:raise Denied()
    def load(self,scope,request):
        key=self.key(scope,request);intent=self.read(self.path/(key+'.intent'))
        if intent is None:self.can_start(scope);return None
        if intent!={'digest':self.digest(request),'request_id':request['request_id'],'op':request['op'],'bead_id':request.get('bead_id','')}:raise Denied()
        done=self.read(self.path/(key+'.receipt'))
        if done is None:return {'state':'uncertain'}
        exact(done,('digest','request_id','op','bead_id','response'))
        if any(done[k]!=intent[k] for k in intent):raise Denied()
        validate_response(done['response'],request)
        return done['response']
    def write(self,path,value):
        fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW|os.O_CLOEXEC,0o600)
        try:
            raw=encode(value);view=memoryview(raw)
            while view:view=view[os.write(fd,view):]
            os.fsync(fd)
        finally:os.close(fd)
        fd=os.open(self.path,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
        try:os.fsync(fd)
        finally:os.close(fd)
    def intent(self,scope,request):
        self.can_start(scope)
        if sum(1 for x in self.path.iterdir())>=self.max_records*3:raise Denied()
        self.write(self.path/(self.key(scope,request)+'.intent'),{'digest':self.digest(request),'request_id':request['request_id'],'op':request['op'],'bead_id':request.get('bead_id','')})
    def finish(self,scope,request,response):
        validate_response(response,request)
        self.write(self.path/(self.key(scope,request)+'.receipt'),{'digest':self.digest(request),'request_id':request['request_id'],'op':request['op'],'bead_id':request.get('bead_id',''),'response':response})

def load_config(path,kind='proxy'):
    if os.geteuid()!=0:raise Denied()
    c=strict_json(root_file(path,private=True))
    common=('schema_version','profile_id','model_uid','source_sha','manifest_sha','observe_command','command_pins','code_pins')
    if kind=='proxy':extra=('model_gid','socket_path','ssh_key_path','relay_command')
    elif kind=='intake':extra=('observe_key_path','journal_dir','max_requests','sdk_path','sdk_sha256','city_uid','city_gid','city_path','city_path_env','city_home')
    else:raise Denied()
    exact(c,common+extra)
    if c['schema_version']!=1 or not ID.fullmatch(c['profile_id']) or type(c['model_uid']) is not int or c['model_uid']<=0:raise Denied()
    if not re.fullmatch('[0-9a-f]{40}',c['source_sha']) or not re.fullmatch('[0-9a-f]{64}',c['manifest_sha']):raise Denied()
    if not isinstance(c['code_pins'],dict) or not c['code_pins']:raise Denied()
    for source,digest in c['code_pins'].items():
        if not isinstance(source,str) or not isinstance(digest,str) or not re.fullmatch('[0-9a-f]{64}',digest):raise Denied()
        root_file(source,4<<20,expected_sha=digest)
    own=str(pathlib.Path(__file__).absolute())
    if own not in c['code_pins']:raise Denied()
    command(c,'observe_command')
    if kind=='proxy':
        if type(c['model_gid']) is not int or c['model_gid']<=0:raise Denied()
        command(c,'relay_command')
    elif type(c['max_requests']) is not int or not 1<=c['max_requests']<=256:raise Denied()
    return c

def proxy_one(conn,config,observer=observe,runner=bounded_command,peer_reader=read_peer):
    deadline=time.monotonic()+5
    conn.settimeout(5)
    pid,uid,gid=struct.unpack('3i',conn.getsockopt(socket.SOL_SOCKET,socket.SO_PEERCRED,struct.calcsize('3i')))
    if uid!=config['model_uid']:raise Denied()
    before=peer_reader(pid,uid,gid)
    chunks=[];size=0
    while True:
        remain=deadline-time.monotonic()
        if remain<=0:raise Denied()
        conn.settimeout(remain)
        chunk=conn.recv(min(4096,LIMIT-size+1))
        if not chunk:break
        size+=len(chunk)
        if size>LIMIT:raise Denied()
        chunks.append(chunk)
    request=strict_json(b''.join(chunks));validate_request(request)
    frame=observer(config,peer=before);validate_observation(frame,config,before,now=datetime.datetime.now(datetime.timezone.utc))
    same_peer(before,peer_reader(pid,uid,gid))
    envelope=dict(frame);envelope['request']=request;envelope['peer']=before
    root_file(config['ssh_key_path'],private=True)
    response=strict_json(runner(command(config,'relay_command'),encode(envelope),timeout=20))
    validate_response(response,request)
    conn.sendall(encode(response)+b'\n')

def socket_parent(path,gid):
    p=pathlib.Path(path)
    if not p.is_absolute() or str(p)!=os.path.normpath(str(p)):raise Denied()
    fd=os.open('/',os.O_RDONLY|os.O_DIRECTORY|os.O_CLOEXEC)
    try:
        for component in p.parts[1:]:
            child=os.open(component,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW|os.O_CLOEXEC,dir_fd=fd)
            os.close(fd);fd=child
            info=os.fstat(fd)
            if info.st_uid!=0 or info.st_mode&0o022:raise Denied()
        info=os.fstat(fd)
        if info.st_gid!=gid or stat.S_IMODE(info.st_mode)!=0o750:raise Denied()
        return fd
    except BaseException:
        os.close(fd);raise

def secure_socket_mode(parentfd,name,gid):
    # O_PATH pins the filesystem socket inode. /proc/self/fd resolves this
    # already-open inode, never a subsequently substituted pathname/symlink.
    fd=os.open(name,os.O_PATH|os.O_NOFOLLOW|os.O_CLOEXEC,dir_fd=parentfd)
    try:
        before=os.fstat(fd)
        if not stat.S_ISSOCK(before.st_mode) or before.st_uid!=0:raise Denied()
        pinned='/proc/self/fd/'+str(fd)
        os.chown(pinned,0,gid);os.chmod(pinned,0o660)
        after=os.stat(name,dir_fd=parentfd,follow_symlinks=False)
        if (after.st_dev,after.st_ino)!=(before.st_dev,before.st_ino) or not stat.S_ISSOCK(after.st_mode) or after.st_uid!=0 or after.st_gid!=gid or stat.S_IMODE(after.st_mode)!=0o660:raise Denied()
    finally:os.close(fd)

def serve(config):
    path=pathlib.Path(config['socket_path'])
    parentfd=socket_parent(path.parent,config['model_gid'])
    # Never unlink/reuse an existing socket or unknown filesystem object.
    sock=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM)
    try:
        sock.bind(str(path));secure_socket_mode(parentfd,path.name,config['model_gid']);sock.listen(4)
    finally:os.close(parentfd)
    while True:
        conn,_=sock.accept()
        with conn:
            try:proxy_one(conn,config)
            except (Denied,OSError,ValueError,KeyError,TypeError):
                with contextlib.suppress(OSError):conn.sendall(b'{"state":"unavailable"}\n')

def main():
    p=argparse.ArgumentParser();p.add_argument('--config',required=True);args=p.parse_args()
    try:serve(load_config(args.config))
    except (Denied,OSError,KeyError,ValueError,TypeError):print('queue proxy unavailable',file=sys.stderr);return 1
    return 0
if __name__=='__main__':sys.exit(main())
