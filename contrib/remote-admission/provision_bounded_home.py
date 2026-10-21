#!/usr/bin/python3
"""One explicit operator transaction for a bounded persistent native OAuth HOME.

Default CLI is read-only plan. Never execute mutable development sources as root.
No automatic resume, overwrite, rollback, deletion or authentication disclosure.
"""
import argparse
import contextlib
import ctypes
import fcntl
import hashlib
import json
import os
import pathlib
import re
import shutil
import stat
import subprocess
import sys
import uuid

IMAGE_BYTES = 2 * 1024**3
PROFILE = pathlib.Path("/etc/gascity/remote-admission/profile.json")
LIMIT = 65536

class Refusal(Exception):
    """Content-free refusal; private source data is never exception output."""

def need(condition):
    if not condition:raise Refusal("refused")

def validate(m):
    fields="schema_version base_dir model_uid model_gid transport_uid build_uid home image image_bytes archive staging state_dir unit_name auth_relative home_device home_inode auth_device auth_inode reserve_bytes source_sha script_sha256 freeze_receipt freeze_receipt_sha256".split()
    need(type(m) is dict and set(m)==set(fields) and type(m["schema_version"]) is int and m["schema_version"]==1)
    for k in ("model_uid","model_gid","transport_uid","build_uid","image_bytes","home_device","home_inode","auth_device","auth_inode","reserve_bytes"):
        need(type(m[k]) is int and m[k]>0)
    need(type(m["base_dir"]) is str and re.fullmatch(r"/srv/[a-z0-9][a-z0-9-]{1,63}",m["base_dir"]) is not None)
    base=pathlib.Path(m["base_dir"])
    need(base.name not in ("ci","gc-build") and not base.name.startswith("ci-") and m["model_uid"] not in (m["transport_uid"],m["build_uid"]))
    need(m["image_bytes"]==IMAGE_BYTES and m["home"]==str(base/"worker") and m["image"]==str(base/"volumes/worker-home-2g.ext4") and m["auth_relative"]==".codex/auth.json")
    for k,pattern in (("archive",re.escape(str(base))+r"/Archive/worker-home-[0-9]{8}T[0-9]{6}Z"),("staging",re.escape(str(base))+r"/home-staging-[0-9]{8}T[0-9]{6}Z"),("state_dir",re.escape(str(base))+r"/home-provision-[0-9]{8}T[0-9]{6}Z")):
        need(type(m[k]) is str and re.fullmatch(pattern,m[k]) is not None)
    need(len({m[k].rsplit("-",1)[1] for k in ("archive","staging","state_dir")})==1)
    need(type(m["unit_name"]) is str and re.fullmatch(r"[A-Za-z0-9_.\\-]+\.mount",m["unit_name"]) is not None)
    need(re.fullmatch(r"[0-9a-f]{40}",m["source_sha"]) is not None and re.fullmatch(r"[0-9a-f]{64}",m["script_sha256"]) is not None)
    need(m["freeze_receipt"]==str(base/"operator-freeze.json") and re.fullmatch(r"[0-9a-f]{64}",m["freeze_receipt_sha256"]) is not None)
    return m

def plan(m,host):
    validate(m)
    host.preflight(m)
    return {"status":"plan","home":m["home"],"image":m["image"],"image_bytes":IMAGE_BYTES,"archive":m["archive"],"unit":m["unit_name"]}

def apply(m,host,journal):
    validate(m)
    need(not journal.exists())
    host.preflight(m)
    state={"status":"unknown","phase":"intent","manifest":m,"completed":{}}
    journal.write(state)
    try:
        for step in ("allocate","copy_verify","cutover","install_verify"):
            state["phase"]=step+"_intent"
            journal.write(state)
            state["completed"][step]=getattr(host,step)(m)
            state["phase"]=step+"_done"
            journal.write(state)
        state.update(phase="complete",status="complete")
        journal.write(state)
        return {"status":"complete","home":m["home"],"home_mount":state["completed"]["install_verify"]["home_mount"]} if "home_mount" in state["completed"]["install_verify"] else {"status":"complete"}
    except Exception:
        state["status"]="unknown"
        # Any failure, including journal fsync uncertainty, retains every object.
        with contextlib.suppress(Exception):journal.write(state)
        raise Refusal("unknown_reconcile_required") from None

def chain(path,owner=0,private=False,ancestor_owner=0):
    path=pathlib.Path(path)
    need(path.is_absolute())
    for item in [path,*path.parents]:
        info=item.lstat()
        need(not stat.S_ISLNK(info.st_mode) and (info.st_uid==owner if item==path else info.st_uid in (0,ancestor_owner)) and info.st_mode&0o022==0)
    info=path.lstat()
    if private:need(info.st_mode&0o077==0)
    return info

def fsync_dir(path):
    fd=os.open(path,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
    try:os.fsync(fd)
    finally:os.close(fd)

def fresh_file(path,raw,mode=0o600,owner=0):
    chain(pathlib.Path(path).parent,owner=owner,ancestor_owner=owner)
    fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,mode)
    try:
        with os.fdopen(fd,"wb",closefd=False) as stream:
            stream.write(raw);stream.flush();os.fsync(fd)
    finally:os.close(fd)
    fsync_dir(pathlib.Path(path).parent)

def signature(info):
    return (info.st_dev,info.st_ino,info.st_size,info.st_mtime_ns,info.st_ctime_ns,info.st_uid,info.st_gid,stat.S_IMODE(info.st_mode),info.st_nlink)

def auth_fd(home,m):
    # Each path component is opened relative to a pinned nofollow directory fd.
    root=os.open(home,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
    codex=None
    try:
        homeinfo=os.fstat(root)
        need(homeinfo.st_uid==m["model_uid"] and stat.S_IMODE(homeinfo.st_mode)==0o700)
        codex=os.open(".codex",os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=root)
        info=os.fstat(codex)
        need(info.st_uid==m["model_uid"] and stat.S_IMODE(info.st_mode)==0o700)
        fd=os.open("auth.json",os.O_RDONLY|os.O_NOFOLLOW,dir_fd=codex)
        info=os.fstat(fd)
        need(stat.S_ISREG(info.st_mode) and info.st_nlink==1 and info.st_uid==m["model_uid"] and stat.S_IMODE(info.st_mode)==0o600)
        return fd
    finally:
        if codex is not None:os.close(codex)
        os.close(root)

def compare_auth(left,right,m):
    """Return only success; auth bytes and hashes never leave this function."""
    a,b=auth_fd(left,m),None
    try:
        b=auth_fd(right,m)
        before_a,before_b=signature(os.fstat(a)),signature(os.fstat(b))
        while True:
            x,y=os.read(a,65536),os.read(b,65536)
            need(x==y)
            if not x:break
        need(before_a==signature(os.fstat(a)) and before_b==signature(os.fstat(b)))
    finally:
        os.close(a)
        if b is not None:os.close(b)

def rename_new(source,destination):
    libc=ctypes.CDLL(None,use_errno=True)
    fn=libc.renameat2
    fn.argtypes=[ctypes.c_int,ctypes.c_char_p,ctypes.c_int,ctypes.c_char_p,ctypes.c_uint]
    fn.restype=ctypes.c_int
    result=fn(-100,os.fsencode(source),-100,os.fsencode(destination),1)
    if result:raise OSError(ctypes.get_errno(),"rename_refused")
    fsync_dir(pathlib.Path(source).parent);fsync_dir(pathlib.Path(destination).parent)

class Journal:
    def __init__(self,path,owner=0):self.path=pathlib.Path(path);self.owner=owner
    def exists(self):return os.path.lexists(self.path)
    def write(self,value):
        if not self.path.exists():
            chain(self.path.parent,owner=self.owner,ancestor_owner=self.owner)
            os.mkdir(self.path,0o700);fsync_dir(self.path.parent)
        chain(self.path,owner=self.owner,private=True,ancestor_owner=self.owner)
        raw=json.dumps(value,sort_keys=True,separators=(",",":")).encode()
        temporary=self.path/("receipt.new-"+str(len(list(self.path.iterdir()))))
        fresh_file(temporary,raw,owner=self.owner)
        os.replace(temporary,self.path/"receipt.json");fsync_dir(self.path)

class Host:
    def __init__(self):self.source_tree=None;self.auth_signature=None;self.image_identity=None;self.freeze_signature=None
    def command(self,argv):
        result=subprocess.run(argv,stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,env={"PATH":"/usr/sbin:/usr/bin:/sbin:/bin","LANG":"C"},timeout=180,check=True)
        need(len(result.stdout)<=LIMIT)
        return result.stdout.decode().strip()
    def absent(self,path):need(not os.path.lexists(path))
    def freeze(self,m):
        info=chain(m["freeze_receipt"],private=True)
        need(stat.S_ISREG(info.st_mode) and info.st_nlink==1 and info.st_size<=LIMIT)
        fd=os.open(m["freeze_receipt"],os.O_RDONLY|os.O_NOFOLLOW)
        try:
            need(signature(os.fstat(fd))==signature(info))
            raw=os.read(fd,LIMIT+1);need(len(raw)<=LIMIT and hashlib.sha256(raw).hexdigest()==m["freeze_receipt_sha256"])
            need(signature(os.fstat(fd))==signature(info))
        finally:os.close(fd)
        def pairs(items):
            value={}
            for key,item in items:need(key not in value);value[key]=item
            return value
        receipt=json.loads(raw.decode("utf-8"),object_pairs_hook=pairs)
        need(type(receipt) is dict and set(receipt)=={"schema_version","model_uid","boot_id","controller_hold_reference","all_uid_launchers_held"})
        need(type(receipt["schema_version"]) is int and receipt["schema_version"]==1 and type(receipt["model_uid"]) is int and receipt["model_uid"]==m["model_uid"] and receipt["all_uid_launchers_held"] is True)
        need(receipt["boot_id"]==uuid.UUID(pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip()).hex)
        need(type(receipt["controller_hold_reference"]) is str and 0<len(receipt["controller_hold_reference"])<=512 and "\n" not in receipt["controller_hold_reference"])
        need(self.freeze_signature is None or self.freeze_signature==signature(info))
        self.freeze_signature=signature(info)
    def no_model(self,m):
        # This is an operator attestation, not proof against an adversarial root actor.
        self.freeze(m)
        self.absent(PROFILE)
        for entry in pathlib.Path("/proc").iterdir():
            if not entry.name.isdigit():continue
            try:raw=(entry/"status").read_text()
            except FileNotFoundError:continue
            values=next(line for line in raw.splitlines() if line.startswith("Uid:")).split()[1:]
            need(str(m["model_uid"]) not in values)
        state=self.command(["/usr/bin/systemctl","show","user@"+str(m["model_uid"])+".service","--property=ActiveState","--value"])
        need(state in ("inactive","failed"))
        import pwd
        user=pwd.getpwuid(m["model_uid"])
        need(user.pw_shell in ("/usr/sbin/nologin","/sbin/nologin","/bin/false"))
        self.absent(pathlib.Path("/var/lib/systemd/linger")/user.pw_name)
    def mounts(self):
        result=[]
        for line in pathlib.Path("/proc/self/mountinfo").read_text().splitlines():
            left,right=line.split(" - ",1);result.append((left.split(),right.split()))
        return result
    def tree(self,path,m):
        result={}
        for directory,dirs,files in os.walk(path,followlinks=False):
            for name in [*dirs,*files]:
                item=pathlib.Path(directory)/name;info=item.lstat()
                need(not stat.S_ISLNK(info.st_mode) and info.st_dev==m["home_device"] and info.st_uid==m["model_uid"])
                need(stat.S_ISDIR(info.st_mode) or (stat.S_ISREG(info.st_mode) and info.st_nlink==1))
                result[str(item.relative_to(path))]=signature(info)
        return result
    def new_unit(self,m):
        unit=pathlib.Path("/etc/systemd/system")/m["unit_name"]
        for path in (unit,pathlib.Path(str(unit)+".d"),pathlib.Path("/etc/systemd/system/local-fs.target.wants")/m["unit_name"]):self.absent(path)
        need(self.command(["/usr/bin/systemd-escape","--path","--suffix=mount",m["home"]])==m["unit_name"])
        shown=self.command(["/usr/bin/systemctl","show",m["unit_name"],"--property=LoadState,FragmentPath,DropInPaths"])
        need(dict(line.split("=",1) for line in shown.splitlines())=={"LoadState":"not-found","FragmentPath":"","DropInPaths":""})
    def preflight(self,m):
        validate(m);self.no_model(m)
        base=pathlib.Path(m["base_dir"])
        chain(base)
        info=chain(m["home"],m["model_uid"],True)
        need(stat.S_ISDIR(info.st_mode) and info.st_gid==m["model_gid"] and (info.st_dev,info.st_ino)==(m["home_device"],m["home_inode"]))
        need(not any(columns[4]==m["home"] for columns,_ in self.mounts()))
        fd=auth_fd(m["home"],m)
        try:
            auth=os.fstat(fd);need((auth.st_dev,auth.st_ino)==(m["auth_device"],m["auth_inode"]))
            self.auth_signature=signature(auth)
        finally:os.close(fd)
        for name in ("image","staging","archive","state_dir"):self.absent(m[name])
        for parent in (base/"volumes",base/"Archive"):
            if parent.exists():chain(parent,private=True)
            else:self.absent(parent)
        self.new_unit(m)
        disk=os.statvfs(base);need(disk.f_bavail*disk.f_frsize>=IMAGE_BYTES+m["reserve_bytes"])
        need(base.stat().st_dev==pathlib.Path(m["home"]).stat().st_dev)
        if (base/"Archive").exists():need((base/"Archive").stat().st_dev==info.st_dev)
        volume=(base/"volumes") if (base/"volumes").exists() else base
        available=os.statvfs(volume)
        need(available.f_bavail*available.f_frsize>=IMAGE_BYTES+m["reserve_bytes"])
        self.source_tree=self.tree(m["home"],m)
        need(sum(item[2] for item in self.source_tree.values())<IMAGE_BYTES//2)
        return {"source_files":len(self.source_tree)}
    def check_source(self,m):
        self.no_model(m)
        need(self.tree(m["home"],m)==self.source_tree)
        fd=auth_fd(m["home"],m)
        try:need(signature(os.fstat(fd))==self.auth_signature)
        finally:os.close(fd)
    def image(self,m):
        info=chain(m["image"],private=True)
        need(stat.S_ISREG(info.st_mode) and info.st_nlink==1 and info.st_size==IMAGE_BYTES and info.st_blocks*512>=IMAGE_BYTES)
        identity=(info.st_dev,info.st_ino)
        need(self.image_identity is None or self.image_identity==identity)
        self.image_identity=identity
        return info
    def allocate(self,m):
        base=pathlib.Path(m["base_dir"])
        self.check_source(m)
        for parent in (base/"volumes",base/"Archive"):
            if not parent.exists():os.mkdir(parent,0o700);fsync_dir(base)
            chain(parent,private=True)
        fresh_file(m["image"],b"")
        self.command(["/usr/bin/fallocate","--length",str(IMAGE_BYTES),m["image"]])
        self.image(m)
        self.command(["/usr/sbin/mkfs.ext4","-F","-q","-E","nodiscard,lazy_itable_init=0,lazy_journal_init=0",m["image"]])
        info=self.image(m)
        fd=os.open(m["image"],os.O_RDONLY|os.O_NOFOLLOW)
        try:os.fsync(fd)
        finally:os.close(fd)
        return {"device":info.st_dev,"inode":info.st_ino,"bytes":info.st_size}
    def copy_verify(self,m):
        base=pathlib.Path(m["base_dir"])
        self.check_source(m);self.image(m)
        os.mkdir(m["staging"],0o700);fsync_dir(base)
        self.command(["/usr/bin/mount","--types","ext4","--options","loop,nodev,nosuid,nodiscard",m["image"],m["staging"]])
        self.verify_mount(m["staging"],m)
        # Every source entry was inventoried; refusal retains mounts on partial copy.
        for name in pathlib.Path(m["home"]).iterdir():
            target=pathlib.Path(m["staging"])/name.name
            if name.is_dir():shutil.copytree(name,target,symlinks=False,copy_function=shutil.copy2)
            else:shutil.copy2(name,target)
        for directory,dirs,files in os.walk(m["home"],topdown=False):
            relative=pathlib.Path(directory).relative_to(m["home"])
            for name in files:
                source=pathlib.Path(directory)/name;target=pathlib.Path(m["staging"])/relative/name
                before=source.stat();os.chown(target,before.st_uid,before.st_gid)
                fd=os.open(target,os.O_RDONLY|os.O_NOFOLLOW)
                try:os.fsync(fd)
                finally:os.close(fd)
            source=pathlib.Path(directory);target=pathlib.Path(m["staging"])/relative
            info=source.stat();os.chown(target,info.st_uid,info.st_gid);os.chmod(target,stat.S_IMODE(info.st_mode));fsync_dir(target)
        compare_auth(m["home"],m["staging"],m)
        self.check_source(m);self.image(m)
        self.command(["/usr/bin/umount","--",m["staging"]])
        need(not any(c[4]==m["staging"] for c,_ in self.mounts()))
        self.image(m)
        return {"auth_equal":True,"staging_unmounted":True}
    def cutover(self,m):
        base=pathlib.Path(m["base_dir"])
        self.check_source(m);self.image(m)
        need(not any(c[4]==m["staging"] for c,_ in self.mounts()))
        need(pathlib.Path(m["archive"]).parent.stat().st_dev==pathlib.Path(m["home"]).stat().st_dev)
        rename_new(m["home"],m["archive"])
        os.mkdir(m["home"],0o700);fsync_dir(base)
        return {"archive_retained":True}
    def verify_mount(self,target,m):
        found=[(c,r) for c,r in self.mounts() if c[4]==target]
        need(len(found)==1)
        columns,extra=found[0]
        need(columns[3]=="/" and extra[0]=="ext4" and extra[1].startswith("/dev/loop"))
        backing=pathlib.Path("/sys/dev/block/"+columns[2]+"/loop/backing_file").read_text().strip()
        need("/"+backing.lstrip("/")==m["image"])
        space=os.statvfs(target);need(space.f_blocks*space.f_frsize<=IMAGE_BYTES)
        return columns,extra
    def install_verify(self,m):
        self.no_model(m);self.image(m);self.new_unit(m)
        unit=pathlib.Path("/etc/systemd/system")/m["unit_name"]
        raw=("[Unit]\nDescription=Bounded persistent model HOME\nBefore=local-fs.target\n[Mount]\nWhat="+m["image"]+"\nWhere="+m["home"]+"\nType=ext4\nOptions=loop,nodev,nosuid,nodiscard\n[Install]\nWantedBy=local-fs.target\n").encode()
        fresh_file(unit,raw,0o644)
        self.command(["/usr/bin/systemctl","daemon-reload"])
        effective=self.command(["/usr/bin/systemctl","show",m["unit_name"],"--property=FragmentPath,DropInPaths"])
        need(dict(line.split("=",1) for line in effective.splitlines())=={"FragmentPath":str(unit),"DropInPaths":""})
        self.command(["/usr/bin/systemctl","start",m["unit_name"]])
        columns,extra=self.verify_mount(m["home"],m)
        info=self.image(m)
        compare_auth(m["archive"],m["home"],m)
        home=chain(m["home"],m["model_uid"],True);need(home.st_gid==m["model_gid"] and stat.S_IMODE(home.st_mode)==0o700)
        wants=pathlib.Path("/etc/systemd/system/local-fs.target.wants")
        if not wants.exists():os.mkdir(wants,0o755);fsync_dir(wants.parent)
        chain(wants)
        link=wants/m["unit_name"];os.symlink("../"+m["unit_name"],link);fsync_dir(wants)
        need(os.readlink(link)=="../"+m["unit_name"] and unit.read_bytes()==raw)
        self.no_model(m)
        return {"home_mount":{"source":extra[1],"device_major":int(columns[2].split(":")[0]),"device_minor":int(columns[2].split(":")[1]),"max_bytes":IMAGE_BYTES,"backing_file":m["image"],"backing_device":info.st_dev,"backing_inode":info.st_ino},"auth_equal":True}

@contextlib.contextmanager
def lock():
    path="/run/gc-repairer-home-provision.lock"
    fd=os.open(path,os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW,0o600)
    try:
        info=os.fstat(fd);need(info.st_uid==0 and stat.S_ISREG(info.st_mode) and info.st_nlink==1 and info.st_mode&0o077==0)
        fcntl.flock(fd,fcntl.LOCK_EX)
        need((os.stat(path,follow_symlinks=False).st_dev,os.stat(path,follow_symlinks=False).st_ino)==(info.st_dev,info.st_ino))
        yield
    finally:os.close(fd)

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest",required=True)
    parser.add_argument("--apply",action="store_true")
    args=parser.parse_args()
    need(os.geteuid()==0)
    info=chain(args.manifest,private=True);need(stat.S_ISREG(info.st_mode) and info.st_nlink==1 and info.st_size<=LIMIT)
    def pairs(items):
        result={}
        for key,value in items:need(key not in result);result[key]=value
        return result
    fd=os.open(args.manifest,os.O_RDONLY|os.O_NOFOLLOW)
    try:
        need(signature(os.fstat(fd))==signature(info))
        raw=os.read(fd,LIMIT+1);need(len(raw)<=LIMIT)
        m=json.loads(raw.decode("utf-8"),object_pairs_hook=pairs)
    finally:os.close(fd)
    validate(m)
    script=chain(pathlib.Path(__file__).absolute(),private=True)
    need(stat.S_ISREG(script.st_mode) and hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest()==m["script_sha256"])
    if args.apply:
        with lock():result=apply(m,Host(),Journal(m["state_dir"]))
    else:result=plan(m,Host())
    print(json.dumps(result,sort_keys=True))

if __name__=="__main__":
    try:main()
    except Exception:
        print('{"status":"refused_or_unknown","reconcile_required":true}')
        sys.exit(1)
