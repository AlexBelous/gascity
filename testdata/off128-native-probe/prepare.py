"""Pinned native bd probe: isolated HOME/store/network, never a city database."""
import hashlib,json,os,pathlib,subprocess
root=pathlib.Path('/srv/gc-build/workspaces/off128-pinned-claim-20261022')
repo=root/'repo'
repo.mkdir()
(root/'home').mkdir()
binary=pathlib.Path('/srv/gc-build/inputs/off128-bd-da08f2739-20261022')
assert hashlib.sha256(binary.read_bytes()).hexdigest()=='9371d74153170dcd5046edb61dbce8924221ef623b8739f5b0c4cc1b885fac97'
env={'HOME':str(root/'home'),'PATH':'/usr/bin:/bin','GOMAXPROCS':'2','BD_SYNC_DISABLE':'1','BEADS_ACTOR':'off128-isolated-worker','CI':'true'}
records=[]
def run(args,timeout=60):
    p=subprocess.run(args,cwd=repo,env=env,text=True,capture_output=True,timeout=timeout)
    entry={'argv':args,'exit':p.returncode,'stdout':p.stdout,'stderr':p.stderr}
    records.append(entry)
    (root/'receipt.json').write_text(json.dumps(records,indent=2)+'\n')
    print(json.dumps(entry),flush=True)
    return p
def bd(*args):return run([str(binary),'--sandbox',*args])
assert run(['/usr/bin/git','init']).returncode==0
assert run(['/usr/bin/git','config','user.name','off128 isolated fixture']).returncode==0
assert run(['/usr/bin/git','config','user.email','fixture@invalid']).returncode==0
assert bd('version').returncode==0
p=bd('init','--prefix','off128','--non-interactive','--skip-hooks','--skip-agents')
if p.returncode:
    raise SystemExit('Init unavailable: retained receipt; no claim conclusion.')
for status in ('open','blocked','deferred'):
    p=bd('create','--title','isolated '+status,'--status',status,'--json')
    assert p.returncode==0
    created=json.loads(p.stdout)
    if isinstance(created,list):created=created[0]
    identity=created['id']
    before=bd('show',identity,'--json')
    claim=bd('update',identity,'--claim','--json')
    after=bd('show',identity,'--json')
    assert before.returncode==0 and after.returncode==0
    row=json.loads(after.stdout)
    if isinstance(row,list):row=row[0]
    print(json.dumps({'status_requested':status,'id':identity,'claim_exit':claim.returncode,'after_status':row['status'],'after_assignee':row.get('assignee')}),flush=True)
print('Probe commands complete; classify observed native behavior without assuming a refusal.',flush=True)
