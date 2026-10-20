"""One finite no-model queue lifecycle; only the immutable gc shim is used."""
import hashlib
import json
import os
import pathlib
import shutil
import signal
import subprocess
import sys

PROMPT = "isolated queue fixture: execute exactly one no-op lifecycle\n"

def invoke(gc, stage, args, expected=0):
    p=subprocess.run([gc,"--request-id","fixture."+stage,*args],
      stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.PIPE,
      timeout=25,check=False)
    if p.returncode!=expected or len(p.stdout)>65536:
        raise ValueError(stage)
    return p.stdout

def cycle(gc, call=invoke):
    claim=json.loads(call(gc,"claim",["hook","--claim","--json"]))
    if claim.get("action")!="work" or claim.get("reason")!="claimed" or not claim.get("assignee"):
        raise ValueError("claim")
    bead=claim["bead_id"]
    current=call(gc,"current",["hook","current","--id-only"]).decode().strip()
    if current!=bead:raise ValueError("current")
    raw=call(gc,"read",["bd","show",bead,"--json"])
    shown=json.loads(raw)
    if len(shown)!=1 or shown[0].get("id")!=bead or shown[0].get("status")!="in_progress" or shown[0].get("assignee")!=claim["assignee"]:
        raise ValueError("read")
    evidence=hashlib.sha256(raw).hexdigest()
    text="isolated no-model control fixture only; no business repair"
    result=json.loads(call(gc,"result",["remote-work","result",bead,"--outcome","no-op","--text",text,"--evidence-sha",evidence]))
    if result.get("state")!="recorded":raise ValueError("result")
    closed=json.loads(call(gc,"close",["bd","close",bead,"--reason",text,"--evidence-sha",evidence]))
    if closed.get("state")!="closed":raise ValueError("close")
    idle=json.loads(call(gc,"idle",["hook","--claim","--json"],expected=1))
    if idle.get("action")!="drain" or idle.get("reason")!="no_work":raise ValueError("idle")
    drain=json.loads(call(gc,"drain",["runtime","drain-ack"]))
    if drain.get("state")!="drain_acknowledged":raise ValueError("drain")
    return {"ok":True,"bead_id":bead,"evidence_sha":evidence,
      "completed":["claim","current","read","result","atomic-close","idle","drain-handler"],
      "limits":"root must independently verify SQLite and real SSH tmux ack; controller Start RPC/model not tested"}

def await_entry():
    # Root sends only after its actual entered receipt and fresh pidfd identity
    # check. No queue request is retried while service entry is being attested.
    signal.pthread_sigmask(signal.SIG_BLOCK,{signal.SIGUSR1})
    info=signal.sigtimedwait({signal.SIGUSR1},30)
    if info is None or info.si_uid!=0:
        raise ValueError("entry barrier")

def assert_auth_inaccessible():
    # Metadata only: never open, read or hash OAuth files in the reused HOME.
    auth=pathlib.Path("/srv/gc-repairer/worker/.codex")
    if any(os.access(auth,mode) for mode in (os.R_OK,os.W_OK,os.X_OK)):
        raise ValueError("OAuth path accessible")
    try:
        os.stat(auth/"auth.json")
    except PermissionError:
        return
    except OSError:
        raise ValueError("OAuth denial unproved") from None
    raise ValueError("OAuth denial unproved")

def guarded_cycle(gc):
    assert_auth_inaccessible()
    await_entry()
    result=cycle(gc)
    result["oauth_denied"]=True
    return result

def main():
    try:
        if os.getuid()!=988 or os.geteuid()!=988 or len(sys.argv)!=2:
            raise ValueError("identity")
        expected=pathlib.Path(sys.argv[1])
        resolved=shutil.which("gc")
        if not expected.is_absolute() or resolved!=str(expected):
            raise ValueError("path")
        if sys.stdin.read(65537)!=PROMPT:raise ValueError("literal")
        print(json.dumps(guarded_cycle(resolved),sort_keys=True))
        return 0
    except (ValueError,OSError,KeyError,TypeError,subprocess.SubprocessError):
        print('{"ok":false,"reason":"fixture lifecycle unproved; retain journals"}')
        return 1

if __name__=="__main__":sys.exit(main())
