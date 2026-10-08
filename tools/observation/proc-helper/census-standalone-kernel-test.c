/* Genuine owned reaped child only: no errno injection/host/helper/RPC/DB. */
#define GC_HELPER_TEST
#define main observer_fixture_main
#include "observer.c"
#undef main
#include "census-v3-reader.h"
#include "census-continuation.h"
#include "census-json.h"
#include <assert.h>
#include <sys/resource.h>
#include <sys/wait.h>

static void reject_source_guards(struct census_global_source *s) {
  struct census_resolution_ledger fresh={0};bool trusted=ev.trusted_kernel,was_fixture=fixture;
  ev.trusted_kernel=false;assert(!census_resolve_standalone(s,&fresh,1));ev.trusted_kernel=trusted;
  fixture=true;assert(!census_resolve_standalone(s,&fresh,1));fixture=was_fixture;
  char ns[80];strcpy(ns,ev.binding.ns);ev.binding.ns[0]=0;
  assert(!census_resolve_standalone(s,&fresh,1));strcpy(ev.binding.ns,ns);
  int error=s->errors[0].raw.error;s->errors[0].raw.error=EACCES;
  assert(!census_resolve_standalone(s,&fresh,1));s->errors[0].raw.error=error;
  uint32_t pid=s->errors[0].raw.pid;s->errors[0].raw.pid=ev.binding.pid;
  assert(!census_resolve_standalone(s,&fresh,1));s->errors[0].raw.pid=pid;
  assert(!fresh.proofs_n && !fresh.resolutions_n);
}

struct standalone_fixture {pid_t child;int control,ready;unsigned scan,retire_scan;bool retired,omit_prior;};
static void retire_owned(struct standalone_fixture *f) {
  assert(!f->retired && write(f->control,"x",1)==1);close(f->control);close(f->ready);
  int status;assert(waitpid(f->child,&status,0)==f->child && WIFEXITED(status) && !WEXITSTATUS(status));
  f->retired=true;
}
static bool enumerate_owned(void *context,struct census_budget *budget,struct census_pid_list *pids) {
  struct standalone_fixture *f=context;f->scan++;
  if(f->scan==f->retire_scan) retire_owned(f);
  assert((uint32_t)getpid()<(uint32_t)f->child);
  if(!census_pid_append(budget,pids,(uint32_t)getpid())) return false;
  if(f->omit_prior && f->scan<f->retire_scan) return true;
  return f->scan>f->retire_scan || census_pid_append(budget,pids,(uint32_t)f->child);
}
int main(int argc,char **argv) {
  if(argc==4 && !strcmp(argv[1],"owned-child")) {
    assert(write(atoi(argv[2]),"r",1)==1);close(atoi(argv[2]));
    char cmd;assert(read(atoi(argv[3]),&cmd,1)==1);close(atoi(argv[3]));return 0;
  }
  assert(argc==2);unsigned mode=(unsigned)atoi(argv[1]);assert(mode==1 || mode==3 || mode==4 || mode==5 || mode==6);
  struct rlimit limit={32,32};assert(!setrlimit(RLIMIT_NOFILE,&limit));
  int ready[2],control[2];assert(!pipe(ready));assert(!pipe(control));pid_t child=fork();assert(child>=0);
  if(!child) {
    close(ready[0]);close(control[1]);char a[32],b[32];snprintf(a,sizeof a,"%d",ready[1]);snprintf(b,sizeof b,"%d",control[0]);
    char *args[]={argv[0],"owned-child",a,b,NULL};
    char *env[]={"GC_CITY_PATH=/owned-standalone-fixture","GC_CITY=fixture-alias",NULL};
    execve(argv[0],args,env);_exit(2);
  }
  close(ready[1]);close(control[0]);char ack;assert(read(ready[0],&ack,1)==1);
  unsigned retirement=mode==5?4:mode;
  struct standalone_fixture f={.child=child,.control=control[1],.ready=ready[0],.retire_scan=retirement,.omit_prior=mode==5};
  assert(!clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start));
  ev.procfd=open("/proc",O_RDONLY|O_DIRECTORY|O_CLOEXEC);assert(ev.procfd>=0);ev.binding.pid=(uint32_t)getpid();ev.trusted_kernel=true;
  /* Own fork/exec children inherit this actual namespace. This unit fixture
   * does not claim privileged host PID1 visibility or installed bootstrap. */
  ssize_t ns=readlink("/proc/self/ns/pid",ev.ns,sizeof ev.ns-1);assert(ns>0 && (size_t)ns<sizeof ev.ns-1);
  ev.ns[ns]=0;strcpy(ev.binding.ns,ev.ns);
  struct census_budget budget={.limit=16777216};struct census_global_source *s=census_global_new(&budget,&f,enumerate_owned,census_fixed_capture);assert(s);
  struct census_resolution_ledger *r=census_resolution_new(&budget);assert(r);struct census_round_journal j={0};
  if(mode==6) {
    /* Negative only: a forged original errno cannot substitute for the NEW
     * genuine syscall, which binds our still-live child and must reject it. */
    struct census_round_scan scan={0};assert(census_global_scan_record(s,1,0,CENSUS_INITIAL,&scan));
    census_global_error(s,1,"process_unavailable",(struct census_capture_fault){.pid=(uint32_t)child,.operation="pidfd_open",.error=ESRCH});
    assert(!census_resolve_standalone(s,r,1) && !r->proofs_n && !r->resolutions_n);
    retire_owned(&f);census_resolution_free(&budget,r);census_global_free(s);assert(!budget.used && !budget.fd_failed);close(ev.procfd);
    puts("PASS actual live owned PID: forged original ESRCH produces NO witness, proof or resolution");return 0;
  }
  enum census_round_result result=census_choose_proven_continuation(s,r,&j);
  assert(f.retired && s->errors_n==1 && s->errors[0].raw.pid==(uint32_t)child && s->errors[0].scan_index==retirement &&
    !s->errors[0].raw.start && s->errors[0].raw.error==ESRCH && !strcmp(s->errors[0].raw.operation,"pidfd_open"));
  bool no_prior=mode==1 || mode==5;
  bool selected=result==CENSUS_ROUNDS_SELECTED_PENDING_PROOF && r->resolutions_n==1 && r->proofs_n==(no_prior?1:2) && !r->certificates_n;
  if(selected) {
    assert(r->proofs[0].source.kind==CENSUS_ENUMERATED_ABSENT && !r->proofs[0].source.start && !r->proofs[0].protected_identity && !r->proofs[0].certificate_id);
    assert(r->resolutions[0].error_index==1 && r->resolutions[0].proof_index==1);
    if(!no_prior) {
      struct census_history_row *prior=census_history_find(&s->history->scans[retirement-2],(uint32_t)child);
      assert(prior && prior->classification==CENSUS_CLASS_NONMANAGED && prior->identity.uids[1]==geteuid());
      assert(r->proofs[1].source.kind==CENSUS_INCARNATION_RETIRED && r->proofs[1].source.start==prior->identity.start && !r->proofs[1].protected_identity && !r->proofs[1].certificate_id);
    }
    assert(s->fatal && s->history->failed && census_final_resolutions_valid(s,r,&j));
    reject_source_guards(s);
    r->proofs[0].source.pid++;assert(!census_final_resolutions_valid(s,r,&j));r->proofs[0].source.pid--;
    r->proofs[0].source.start=1;assert(!census_final_resolutions_valid(s,r,&j));r->proofs[0].source.start=0;
    r->proofs[0].protected_identity=true;assert(!census_final_resolutions_valid(s,r,&j));r->proofs[0].protected_identity=false;
    r->resolutions[0].error_index=2;assert(!census_final_resolutions_valid(s,r,&j));r->resolutions[0].error_index=1;
    unsigned resolutions=r->resolutions_n;r->resolutions_n=0;
    assert(!census_final_resolutions_valid(s,r,&j));r->resolutions_n=resolutions;
    if(!no_prior) {
      struct census_history_row *prior=census_history_find(&s->history->scans[retirement-2],(uint32_t)child);
      struct census_resolution_ledger fresh={0};
      prior->identity.declared_root=true;assert(!census_resolve_standalone(s,&fresh,1));prior->identity.declared_root=false;
      prior->classification=CENSUS_CLASS_MANAGED;assert(!census_resolve_standalone(s,&fresh,1));prior->classification=CENSUS_CLASS_NONMANAGED;
      prior->classification=CENSUS_CLASS_KERNEL;assert(!census_resolve_standalone(s,&fresh,1));prior->classification=CENSUS_CLASS_NONMANAGED;
      prior->identity.uids_revalidated=false;assert(!census_resolve_standalone(s,&fresh,1));prior->identity.uids_revalidated=true;
      char *name=prior->identity.name;prior->identity.name="tmux";assert(!census_resolve_standalone(s,&fresh,1));prior->identity.name=name;
      prior->identity.uids[1]++;assert(!census_resolve_standalone(s,&fresh,1));prior->identity.uids[1]--;
      assert(!fresh.proofs_n && !fresh.resolutions_n);
      r->proofs[1].source.start++;assert(!census_final_resolutions_valid(s,r,&j));r->proofs[1].source.start--;
      r->proofs[1].certificate_id=1;assert(!census_final_resolutions_valid(s,r,&j));r->proofs[1].certificate_id=0;
    }
    assert(census_final_resolutions_valid(s,r,&j));
  }
  printf("%s genuine owned flags0 pidfdESRCH scan%u: originalNULL/index/clocks kept, %u proof(s), %u resolution(s), %u certificate(s), result%d\n",selected?"PASS":"RED",retirement,r->proofs_n,r->resolutions_n,r->certificates_n,result);
  census_resolution_free(&budget,r);census_global_free(s);assert(!budget.used && !budget.fd_failed);assert(!close(ev.procfd));return selected?0:1;
}
