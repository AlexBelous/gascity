import hashlib,json,pathlib,subprocess
root=pathlib.Path('/srv/gc-build/workspaces/off128-pinned-claim-20261022')
repo=root/'repo'
binary='/srv/gc-build/inputs/off128-bd-da08f2739-20261022'
env={'HOME':str(root/'home'),'PATH':'/usr/bin:/bin','GOMAXPROCS':'2','BD_SYNC_DISABLE':'1','BEADS_ACTOR':'off128-isolated-worker','CI':'true'}
assert hashlib.sha256(pathlib.Path(binary).read_bytes()).hexdigest()=='9371d74153170dcd5046edb61dbce8924221ef623b8739f5b0c4cc1b885fac97'
previous=json.loads((root/'receipt.json').read_text())
blocked=next(json.loads(x['stdout'])['id'] for x in previous if 'create' in x['argv'] and 'isolated blocked' in x['argv'])
records=[]
def run(*args):
    p=subprocess.run([binary,'--sandbox',*args],cwd=repo,env=env,text=True,capture_output=True,timeout=45)
    records.append({'argv':list(args),'exit':p.returncode,'stdout':p.stdout,'stderr':p.stderr})
    (root/'recovery-receipt.json').write_text(json.dumps(records,indent=2)+'\n')
    print(json.dumps(records[-1]),flush=True)
    return p
def show(identity):
    p=run('show',identity,'--json');assert p.returncode==0
    return json.loads(p.stdout)[0]
def create(title,status):
    p=run('create','--title',title,'--status',status,'--json');assert p.returncode==0
    return json.loads(p.stdout)['id']
assert show(blocked)['status']=='blocked'
assert run('update',blocked,'--assignee','off128-isolated-worker','--set-metadata','gc.routed_to=deal-executor','--json').returncode==0
p=run('update',blocked,'--claim','--json')
assert p.returncode!=0 and 'status blocked' in p.stderr
assert show(blocked)['status']=='blocked'
control=create('positive discovery control','open')
p=run('ready','--json');assert p.returncode==0
ids={r['id'] for r in json.loads(p.stdout)}
assert control in ids and blocked not in ids
own=create('own persistent reservation hold','blocked')
other=create('unrelated existing hold','blocked')
for hold in (own,other):assert run('dep','add',blocked,hold,'--json').returncode==0
assert run('dep','remove',blocked,own,'--json').returncode==0
p=run('dep','list',blocked,'--json');assert p.returncode==0
assert other in p.stdout and own not in p.stdout
p=run('update',blocked,'--if-status','blocked','--if-assignee','off128-isolated-worker','--status','open','--json')
assert p.returncode==0
claim=run('update',blocked,'--claim','--json')
row=show(blocked)
print(json.dumps({'released_own_dependency_only':True,'unrelated_dependency':other,'unrelated_dependency_retained':True,'after_status_open_claim_exit':claim.returncode,'after_claim_status':row['status'],'after_claim_assignee':row.get('assignee')}),flush=True)
# This probes support only; a refused unknown flag must not mutate the control.
before=show(control)
p=run('update',control,'--if-revision',str(before['revision']),'--status','blocked','--json')
after=show(control)
print(json.dumps({'if_revision_exit':p.returncode,'if_revision_supported':p.returncode==0,'control_after_status':after['status']}),flush=True)
