#!/usr/bin/python3
"""Unprivileged, exact-command gc/bd shim for the root-owned queue socket.

No SSH key, store coordinates or session identity is accepted from arguments or
ambient GC environment. Provider prompts/hooks use this deliberately narrow
subset; it is not a general replacement for the local Gas City CLI.
"""
import argparse
import importlib.util
import json
import os
import pathlib
import socket
import stat
import struct
import sys
import time
import uuid

if 'queue_proxy' in sys.modules:q=sys.modules['queue_proxy']
else:
    source=pathlib.Path(__file__).absolute().with_name('queue_proxy.py')
    spec=importlib.util.spec_from_file_location('queue_proxy',source);q=importlib.util.module_from_spec(spec);sys.modules['queue_proxy']=q;spec.loader.exec_module(q)

CONFIG='/etc/gascity/remote-work-client.json'

def parse_command(argv,request_id=None):
    args=list(argv)
    if args and args[0] in ('gc','bd'):
        frontend=args.pop(0)
        if frontend=='bd':args.insert(0,'bd')
    p=argparse.ArgumentParser(prog='gc',allow_abbrev=False)
    p.add_argument('--request-id',default=request_id or str(uuid.uuid4()))
    sub=p.add_subparsers(dest='command',required=True)
    hook=sub.add_parser('hook',allow_abbrev=False);hook.add_argument('--claim',action='store_true');hook.add_argument('--json',action='store_true');hook.add_argument('--drain-ack',action='store_true');hook.add_argument('current',nargs='?',choices=['current']);hook.add_argument('--id-only',action='store_true')
    bd=sub.add_parser('bd',allow_abbrev=False);bds=bd.add_subparsers(dest='verb',required=True)
    show=bds.add_parser('show',allow_abbrev=False);show.add_argument('bead_id');show.add_argument('--json',action='store_true',required=True)
    close=bds.add_parser('close',allow_abbrev=False);close.add_argument('bead_id');close.add_argument('--reason',required=True);close.add_argument('--evidence-sha',required=True)
    rw=sub.add_parser('remote-work',allow_abbrev=False);rws=rw.add_subparsers(dest='verb',required=True);result=rws.add_parser('result',allow_abbrev=False);result.add_argument('bead_id');result.add_argument('--outcome',choices=['shipped','no-op','blocked','abandoned'],required=True);result.add_argument('--text',required=True);result.add_argument('--evidence-sha',required=True)
    runtime=sub.add_parser('runtime',allow_abbrev=False);runtime.add_argument('verb',choices=['drain-ack'])
    a=p.parse_args(args);r={'request_id':a.request_id};mode='json';drain=False
    if a.command=='hook':
        if a.current=='current' and a.id_only and not a.claim and not a.json and not a.drain_ack:r['op']='current';mode='id'
        elif a.claim and a.json and not a.current and not a.id_only:r['op']='claim';mode='hook';drain=a.drain_ack
        else:raise q.Denied()
    elif a.command=='bd':
        r.update(op='read' if a.verb=='show' else 'close',bead_id=a.bead_id)
        if a.verb=='close':r.update(text=a.reason,evidence_sha=a.evidence_sha)
        else:mode='show'
    elif a.command=='remote-work':r.update(op='result',bead_id=a.bead_id,text=a.text,evidence_sha=a.evidence_sha,outcome=a.outcome)
    elif a.command=='runtime':r['op']='self_drain'
    q.validate_request(r);return r,mode,drain

def load_client_config():
    c=q.strict_json(q.root_file(CONFIG))
    q.exact(c,('schema_version','socket_path','timeout_seconds'))
    if c['schema_version']!=1 or not isinstance(c['socket_path'],str) or not os.path.isabs(c['socket_path']) or type(c['timeout_seconds']) is not int or not 1<=c['timeout_seconds']<=60:raise q.Denied()
    path=pathlib.Path(c['socket_path']);parent=path.parent.lstat();s=path.lstat()
    if not stat.S_ISDIR(parent.st_mode) or parent.st_uid!=0 or parent.st_mode&0o027 or not stat.S_ISSOCK(s.st_mode) or s.st_uid!=0 or stat.S_IMODE(s.st_mode)!=0o660:raise q.Denied()
    return c

def exchange(config,request):
    data=q.encode(request);deadline=time.monotonic()+config['timeout_seconds']
    with socket.socket(socket.AF_UNIX,socket.SOCK_STREAM) as conn:
        conn.settimeout(config['timeout_seconds']);conn.connect(config['socket_path'])
        pid,uid,gid=struct.unpack('3i',conn.getsockopt(socket.SOL_SOCKET,socket.SO_PEERCRED,struct.calcsize('3i')))
        if pid<=1 or uid!=0:raise q.Denied()
        conn.sendall(data);conn.shutdown(socket.SHUT_WR);out=bytearray()
        while True:
            remain=deadline-time.monotonic()
            if remain<=0:raise q.Denied()
            conn.settimeout(remain);chunk=conn.recv(min(4096,q.LIMIT-len(out)+1))
            if not chunk:break
            out.extend(chunk)
            if len(out)>q.LIMIT:raise q.Denied()
    response=q.strict_json(bytes(out));q.validate_response(response,request);return response

def invoke(request,mode,drain,send,stdout):
    if request['op']=='claim':
        for key in ('GC_HOOK_CALLBACK_LANE','GC_MANAGED_SESSION_HOOK','GC_HOOK_EVENT_NAME'):
            if os.environ.get(key,'').lower() not in ('','0','false'):raise q.Denied()
    out=send(request);q.validate_response(out,request)
    if mode=='hook':
        result={'schema_version':'1','ok':True,'command':'hook'}
        if out['state']=='idle':
            result.update(action='drain',reason='no_work')
            if drain:
                ack={'request_id':request['request_id']+'.drain','op':'self_drain'}
                q.validate_request(ack);response=send(ack);q.validate_response(response,ack)
                if response['state']!='drain_acknowledged':raise q.Denied()
                result['drain_acknowledged']=True
            stdout.write(json.dumps(result)+'\n');return 0 if drain else 1
        result.update(action='work',reason='claimed',bead_id=out['bead_id'],assignee=out.get('bead',{}).get('assignee',''))
        if not result['assignee']:raise q.Denied()
        stdout.write(json.dumps(result)+'\n');return 0
    if mode=='id':stdout.write(out['bead_id']+'\n')
    elif mode=='show':
        if 'bead' not in out:raise q.Denied()
        stdout.write(json.dumps([out['bead']])+'\n')
    else:stdout.write(json.dumps(out)+'\n')
    return 0

def main():
    request=None
    try:
        request,mode,drain=parse_command(sys.argv[1:]);config=load_client_config()
        return invoke(request,mode,drain,lambda r:exchange(config,r),sys.stdout)
    except (q.Denied,OSError,ValueError,KeyError,TypeError):
        rid=request['request_id'] if request else 'unparsed'
        print('queue operation unavailable; request_id='+rid+'; preserve this ID for reconciliation, do not retry with a new ID',file=sys.stderr)
        return 1
if __name__=='__main__':sys.exit(main())
