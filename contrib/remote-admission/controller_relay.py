#!/usr/bin/python3
"""Forced-command root intake for a single immutable queue relay profile.

Root verifies a fresh VM kernel observation, then runs only a pinned SDK
receiver as the existing city UID. fd3 is a root-created anonymous pipe;
root never opens a city database and private frames never enter public logs.
"""
import argparse
import datetime
import importlib.util
import json
import os
import pathlib
import stat
import subprocess
import sys

# An isolated Python invocation does not add script directories to sys.path.
# Load the exact sibling from the reviewed root-owned release, not ambient cwd.
if 'queue_proxy' in sys.modules:
    q=sys.modules['queue_proxy']
else:
    module=pathlib.Path(__file__).absolute().with_name('queue_proxy.py')
    if os.geteuid()==0:
        for path in (module,*module.parents):
            st=path.lstat()
            if st.st_uid!=0 or st.st_mode&0o022 or stat.S_ISLNK(st.st_mode):
                raise SystemExit('queue intake unavailable')
    spec=importlib.util.spec_from_file_location('queue_proxy',module)
    q=importlib.util.module_from_spec(spec);sys.modules['queue_proxy']=q;spec.loader.exec_module(q)


def process(envelope,config,observe,execute):
    q.exact(envelope,('schema_version','profile_id','identity','observe','request','peer'))
    request=envelope['request'];q.validate_request(request)
    initial={k:envelope[k] for k in ('schema_version','profile_id','identity','observe')}
    q.validate_observation(initial,config,envelope['peer'])
    fresh=observe(envelope)
    q.validate_observation(fresh,config,envelope['peer'],expected=initial)
    proof=dict(fresh);proof['request']=request
    out=execute(proof)
    q.validate_response(out,request)
    return out


def child_setup(readfd,city_uid,city_gid,ops=os):
    # Executed only after fork. All queue/DB code runs after UID/GID/group drop.
    if readfd!=3:
        ops.dup2(readfd,3,inheritable=True);ops.close(readfd)
    ops.setgroups([]);ops.setgid(city_gid);ops.setuid(city_uid)
    if ops.getuid()!=city_uid or ops.geteuid()!=city_uid:ops._exit(126)


def run_sdk(config,proof):
    if os.geteuid()!=0:raise q.Denied()
    city_uid=config['city_uid'];city_gid=config['city_gid']
    if type(city_uid) is not int or type(city_gid) is not int or city_uid<=0 or city_gid<=0:raise q.Denied()
    binary=config['sdk_path'];q.root_file(binary,256<<20,expected_sha=config['sdk_sha256'])
    # The policy is public/nonsecret, root owned and fixed at the SDK path.
    public=q.strict_json(q.root_file('/etc/gascity/remote-work-policy.json'))
    if public.get('city_uid')!=city_uid or public.get('helper_path')!=str(pathlib.Path(__file__).absolute()):raise q.Denied()
    q.root_file(__file__,1<<20,expected_sha=public['helper_sha256'])
    if public.get('parent_argv')!=sys.orig_argv:raise q.Denied()
    frame=q.encode(proof)
    readfd,writefd=os.pipe2(os.O_CLOEXEC)
    try:
        # This is a one-request root process: FD3 is private and contains no
        # preexisting caller descriptor. Never accept a descriptor from SSH.
        # Remap only in the forked child. Parent fd3 may be the durable
        # request lock; replacing it would silently release serialization.
        inherited=tuple(sorted({3,readfd}))
        def drop():child_setup(readfd,city_uid,city_gid)
        p=subprocess.Popen([binary,'remote-work','--proof-fd','3'],stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.PIPE,pass_fds=inherited,preexec_fn=drop,start_new_session=True,cwd=config['city_path'],env={'PATH':config['city_path_env'],'HOME':config['city_home'],'LANG':'C.UTF-8'})
        os.close(readfd);readfd=-1
        writer=os.fdopen(writefd,'wb',buffering=0);writefd=-1
        out=q.collect_process(p,frame,writer,15)
        return q.strict_json(out)
    finally:
        if readfd>=0:os.close(readfd)
        if writefd>=0:os.close(writefd)


def execute_envelope(envelope,config):
    def fresh(expected):
        out=q.observe(config,expected)
        q.validate_observation(out,config,expected['peer'],expected={k:expected[k] for k in ('schema_version','profile_id','identity','observe')},now=datetime.datetime.now(datetime.timezone.utc))
        return out
    # Root journal holds only request digests/public SDK receipts, never tokens.
    journal=q.RequestJournal(config['journal_dir'],max_records=config['max_requests'])
    identity=envelope.get('identity',{})
    scope=config['profile_id']+'/'+str(identity.get('sid',''))+'/'+str(identity.get('attempt',''))
    def execute(proof):
        request=proof['request']
        with journal.lock(scope):
            previous=journal.load(scope,request)
            if previous is not None and previous.get('state')!='uncertain':return previous
            if previous is None:journal.intent(scope,request)
            # Reusing an uncertain request reaches the same SDK durable intent;
            # SDK can reconcile its exact receipt but cannot blindly replay it.
            out=run_sdk(config,proof)
            q.validate_response(out,request)
            journal.finish(scope,request,out)
            return out
    return process(envelope,config,fresh,execute)


def main():
    p=argparse.ArgumentParser();p.add_argument('--config',required=True);a=p.parse_args()
    try:
        config=q.load_config(a.config,kind='intake')
        own=str(pathlib.Path(__file__).absolute())
        if own not in config['code_pins']:raise q.Denied()
        q.root_file(own,4<<20,expected_sha=config['code_pins'][own])
        q.root_file(config['observe_key_path'],private=True)
        data=sys.stdin.buffer.read(q.LIMIT+1)
        envelope=q.strict_json(data)
        out=execute_envelope(envelope,config)
        sys.stdout.buffer.write(q.encode(out)+b'\n');sys.stdout.buffer.flush()
    except (q.Denied,OSError,ValueError,TypeError,KeyError):
        print('queue intake unavailable',file=sys.stderr);return 1
    return 0
if __name__=='__main__':sys.exit(main())
