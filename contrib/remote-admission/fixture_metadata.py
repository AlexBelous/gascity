"""Fixture-only forced command: exact native SSH metadata verbs, no shell."""
import contextlib
import fcntl
import json
import os
import select
import time
import pathlib
import re
import shlex
import stat
import sys

TRANSPORT_UID = 987
LIMIT = 16384
SLOTS = {"GC_DRAIN_ACK":"gc-session-ack", "GC_DRAIN_ACK_SOURCE":"gc-session-source", "GC_DRAIN_ACK_REQUESTER_INSTANCE_TOKEN":"gc-session-digest"}
TARGET = "=queue-fixture-runtime"
READ_KEYS = frozenset(("GC_DRAIN_ACK", "GC_DRAIN_ACK_SOURCE",
 "GC_DRAIN_ACK_REQUESTER_INSTANCE_TOKEN", "GC_DRAIN_REASON", "GC_DRAIN_GENERATION"))
REMOVE_KEYS = frozenset(("GC_DRAIN_REASON", "GC_DRAIN_GENERATION"))

def parse(command):
    if not isinstance(command,str) or not command or len(command)>4096 or any(ord(c)<32 or ord(c)==127 for c in command):
        raise ValueError("refused")
    args=shlex.split(command,posix=True)
    if len(args)<5 or args[0]!="tmux" or args[2:4]!=["-t",TARGET]:
        raise ValueError("refused")
    if args[1]=="show-environment" and len(args)==5 and args[4] in READ_KEYS:
        return args[1:]
    if args[1]!="set-environment" or len(args)!=6:
        raise ValueError("refused")
    key,value=args[4:]
    if key=="-u" and value in REMOVE_KEYS:
        return args[1:]
    # Real SetMeta stages these values over stdin; direct writes would bypass
    # the fixed three-slot fixture budget.
    raise ValueError("refused")

def root_path(raw):
    p=pathlib.Path(raw)
    if not p.is_absolute() or str(p)!=os.path.normpath(str(p)) or str(p)=="/":
        raise ValueError("refused")
    for node in (p,*p.parents):
        info=node.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid!=0 or info.st_mode&0o022:
            raise ValueError("refused")
    return p

def checked_file(path, flags, empty=False):
    root_path(path.parent)
    fd=os.open(path,flags|os.O_NOFOLLOW|os.O_CLOEXEC)
    try:
        info=os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_uid!=TRANSPORT_UID or stat.S_IMODE(info.st_mode)!=0o600 or info.st_nlink!=1 or info.st_size>4096 or (empty and info.st_size!=0):
            raise ValueError("refused")
        return fd
    except BaseException:
        os.close(fd)
        raise

@contextlib.contextmanager
def stage_lock(root):
    fd=checked_file(root/"stage.lock",os.O_RDWR)
    try:
        fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        yield
    finally:os.close(fd)

def read_owned(path):
    fd=checked_file(path,os.O_RDONLY)
    try:
        data=os.read(fd,4097)
        if len(data)>4096:raise ValueError("refused")
        return data.decode("utf-8")
    finally:os.close(fd)

class NativeCarrier:
    """Interprets only the exact golden produced by the real Go SetMeta."""
    def __init__(self,trace):
        if set(trace)!={"schema_version","records"} or trace["schema_version"]!=1 or len(trace["records"])!=3:
            raise ValueError("refused")
        self.records={}
        for rec in trace["records"]:
            key=rec["key"]
            if key not in SLOTS or key in self.records or len(rec["calls"])!=3 or rec["calls"][0]["argv"]!=["sh"]:
                raise ValueError("refused")
            self.records[key]=rec
        if set(self.records)!=set(SLOTS):raise ValueError("refused")

    @staticmethod
    def body(script,name):
        prefix = 'cat > "$d/' + name + '"' + " <<'GC_EOF_FIXTURE' || fail\n"
        start=script.index(prefix)+len(prefix)
        end=script.index("GC_EOF_FIXTURE\n",start)
        return script[start:end]

    @staticmethod
    def value(key,env):
        if key=="GC_DRAIN_ACK":value="1"
        elif key=="GC_DRAIN_ACK_SOURCE":value="agent"
        else:
            match=re.fullmatch(re.escape(key)+r"='([0-9a-f]{64})'\nexport "+re.escape(key)+r"\n",env)
            if match is None:raise ValueError("refused")
            value=match.group(1)
        if env!=key+"='"+value+"'\nexport "+key+"\n":
            raise ValueError("refused")
        return value

    def expected(self,key,value):
        rec=self.records[key]
        template=rec["calls"][0]["stdin"]
        if key=="GC_DRAIN_ACK_REQUESTER_INSTANCE_TOKEN":
            template=template.replace(rec["value"],value)
        elif value!=rec["value"]:raise ValueError("refused")
        return template,self.body(template,"env.sh"),self.body(template,"session.tmux")

    def decode_stage(self,script):
        if not isinstance(script,str) or len(script.encode())>LIMIT:
            raise ValueError("refused")
        if len(re.findall(r"GC_EOF_[0-9A-F]{16}",script))!=4:
            raise ValueError("refused")
        normalized=re.sub(r"GC_EOF_[0-9A-F]{16}","GC_EOF_FIXTURE",script)
        env=self.body(normalized,"env.sh")
        for key in SLOTS:
            try:value=self.value(key,env)
            except ValueError:continue
            expected,env_body,tmux_body=self.expected(key,value)
            if normalized!=expected:raise ValueError("refused")
            return key,value,env_body,tmux_body
        raise ValueError("refused")

    def stage(self,root,script):
        root=root_path(root)
        key,_,env,tmux=self.decode_stage(script)
        slot=root/SLOTS[key]
        with stage_lock(root):
            descriptors=[]
            try:
                for name in ("env.sh","session.tmux"):
                    descriptors.append(checked_file(slot/name,os.O_WRONLY,empty=True))
                for fd,body in zip(descriptors,(env,tmux)):
                    data=body.encode()
                    if len(data)>4096:raise ValueError("refused")
                    view=memoryview(data)
                    while view:
                        n=os.write(fd,view)
                        if n<=0:raise ValueError("refused")
                        view=view[n:]
                    os.fsync(fd)
            finally:
                for fd in descriptors:os.close(fd)
        return str(slot)

    def source(self,root,path):
        root=root_path(root)
        keys=[key for key,slot in SLOTS.items() if path==str(root/slot/"session.tmux")]
        if len(keys)!=1:raise ValueError("refused")
        key=keys[0]
        with stage_lock(root):
            env=read_owned(root/SLOTS[key]/"env.sh")
            value=self.value(key,env)
            _,expected_env,expected_tmux=self.expected(key,value)
            if env!=expected_env or read_owned(pathlib.Path(path))!=expected_tmux:
                raise ValueError("refused")
        # Never execute the supplied config file. Only these typed nonsecret
        # markers become an actual tmux metadata argv; no token is accepted.
        return ["set-environment","-t",TARGET,key,value]

    def cleanup(self,root,path):
        root=root_path(root)
        if path not in [str(root/slot) for slot in SLOTS.values()]:
            raise ValueError("refused")
        # Native cleanup is best effort. Retain all files; only root archives
        # exact recorded inodes after terminal/empty and no transport writers.
        raise ValueError("fixture stage retained")

def read_stage_stdin(fd):
    deadline=time.monotonic()+5
    data=bytearray()
    while True:
        left=deadline-time.monotonic()
        if left<=0 or not select.select([fd],[],[],left)[0]:raise ValueError("refused")
        part=os.read(fd,min(4096,LIMIT-len(data)+1))
        if not part:break
        data.extend(part)
        if len(data)>LIMIT:raise ValueError("refused")
    return data.decode("utf-8")

def load_trace(raw):
    p=pathlib.Path(raw)
    root_path(p.parent)
    fd=os.open(p,os.O_RDONLY|os.O_NOFOLLOW|os.O_CLOEXEC)
    try:
        info=os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_uid!=0 or info.st_mode&0o022 or info.st_nlink!=1 or info.st_size>LIMIT:
            raise ValueError("refused")
        data=os.read(fd,LIMIT+1)
        if len(data)>LIMIT:raise ValueError("refused")
        return NativeCarrier(json.loads(data))
    finally:os.close(fd)

def socket_path(raw):
    p=pathlib.Path(raw)
    if not p.is_absolute() or str(p)!=os.path.normpath(str(p)) or p.name!="tmux.sock":
        raise ValueError("refused")
    # Frozen ForceCommand supplies this exact fixture path; caller supplies no
    # path. Refuse a writable ancestor or a substituted transport socket.
    for parent in p.parents:
        s=parent.lstat()
        if not stat.S_ISDIR(s.st_mode) or s.st_uid!=0 or s.st_mode&0o022 or stat.S_ISLNK(s.st_mode):
            raise ValueError("refused")
    s=p.lstat()
    if not stat.S_ISSOCK(s.st_mode) or s.st_uid!=987:
        raise ValueError("refused")
    return str(p)

def main():
    try:
        if len(sys.argv)!=4 or os.getuid()!=TRANSPORT_UID or os.geteuid()!=TRANSPORT_UID:
            raise ValueError("refused")
        path=socket_path(sys.argv[1])
        root=root_path(sys.argv[2])
        carrier=load_trace(sys.argv[3])
        command=os.environ.get("SSH_ORIGINAL_COMMAND","")
        if len(command)>4096 or any(ord(c)<32 or ord(c)==127 for c in command):
            raise ValueError("refused")
        argv=shlex.split(command,posix=True)
        if argv==["sh"]:
            print(carrier.stage(root,read_stage_stdin(sys.stdin.fileno())))
            return 0
        if len(argv)==3 and argv[:2]==["tmux","source-file"]:
            args=carrier.source(root,argv[2])
        elif len(argv)==3 and argv[:2]==["rm","-rf"]:
            carrier.cleanup(root,argv[2])
            return 126
        else:args=parse(command)
        os.execve("/usr/bin/tmux",["/usr/bin/tmux","-S",path,*args],
          {"PATH":"/usr/bin:/bin","LANG":"C.UTF-8","HOME":"/nonexistent"})
    except (ValueError,OSError,KeyError,TypeError):
        print("fixture metadata refused",file=sys.stderr)
        return 126
    return 126

if __name__=="__main__":sys.exit(main())
