#ifdef CURRENT_TRIPLET_PROTECTED
#define CENSUS_KERNEL_MAIN census_previous_kernel_main
#include "census-v3-kernel-test.c"
#include "census-continuation.h"
#include "census-json.h"
struct mixed_fixture {struct owned_tree tree;unsigned scans;pid_t spare;int spare_control;bool spare_retired;};
static bool mixed_capture(void *context,uint32_t pid,struct census_budget *b,
    struct census_owned_identity *out,struct census_capture_fault *fault) {
  (void)context;return census_capture_bounded_evidenced(pid,b,out,fault);
}
#else
/* Current-triplet reconciliation Source test; real owned child, no live helper/RPC/DB. */
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
int standalone_previous_main(int argc,char **argv) {
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
  if(mode==4) assert(j.selected_first==2 && j.selected_second==3 && j.selected_seal==4 && s->history->n==4);
  bool selected=result==CENSUS_ROUNDS_SELECTED_PENDING_PROOF && r->resolutions_n==1 && r->proofs_n==(no_prior?1:2) && !r->certificates_n;
  if(selected) {
#ifdef GC_CENSUS_CLOSING_RECONCILE_H
    assert(census_current_closings_equal(s,r));
    if(s->history->scans[j.selected_first-1].receipt.classified==s->history->scans[j.selected_seal-1].receipt.classified) {
      char *digest=s->history->scans[j.selected_first-1].receipt.live_digest;char saved=digest[0];
      digest[0]=saved=='a'?'b':'a';assert(!census_current_closings_equal(s,r));digest[0]=saved;
    }
    struct census_history_row *survivor=census_history_find(&s->history->scans[j.selected_seal-1],(uint32_t)getpid());
    assert(survivor);survivor->identity.uids[1]++;assert(!census_current_closings_equal(s,r));survivor->identity.uids[1]--;
    assert(census_current_closings_equal(s,r));
#endif
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

#endif

/* Structural negative classifier inputs, never fabricated kernel witnesses. */
static struct census_owned_identity diagnostic_identity(void) {
  return (struct census_owned_identity){.pid=777,.ppid=1,.pgid=777,.start=123,
    .sid="",.city="/diagnostic-fixture",.template="",.name="fixture",
    .stat_revalidated=true,.pidfd_bound=true,.uids_revalidated=true,
    .environment_revalidated=true,.no_gc_environment=false};
}
static void diagnostic_error(struct census_owned_identity *p,const char *expected,bool emit) {
  struct census_history_row row={.pid=p->pid,.failure=CENSUS_ROW_CLASSIFICATION_FAILED,.identity=*p};
#ifdef GC_CENSUS_CLOSING_RECONCILE_H
  assert(census_classify_diagnosed(p,&row.diagnostic)==CENSUS_CLASS_INVALID);
#else
  assert(census_classify_owned(p)==CENSUS_CLASS_INVALID);
#endif
  struct census_history history={.n=1};history.scans[0].rows=&row;history.scans[0].allocated_rows=1;
  struct census_budget budget={.limit=16777216};
  struct census_global_source source={.budget=&budget,.history=&history,.errors_n=1,.errors_total=1};
  source.errors[0]=(struct census_global_fault){.scan_index=1,.reason="process_unavailable",
    .raw={.pid=p->pid,.start=p->start,.operation="classification",.error=EINVAL}};
  struct census_global_fault before=source.errors[0];
  struct census_json *out=census_json_new(&budget);assert(out);census_json_errors(out,&source);
  char needle[96];assert(snprintf(needle,sizeof needle,"\"reason\":\"%s\"",expected)>0);
  assert(!out->failed && out->n<4096 && strstr(out->data,needle));
  assert(strstr(out->data,"\"operation\":\"classification\"") && strstr(out->data,"\"errno\":22"));
  assert(!memcmp(&before,&source.errors[0],sizeof before) && !census_absence_raw_eligible(&source.errors[0]));
  if(emit) printf("%.*s\n",(int)out->n,out->data);
#ifdef GC_CENSUS_CLOSING_RECONCILE_H
  unsigned good=row.diagnostic.category;
  for(unsigned invalid=5;invalid<=6;invalid++) {
    row.diagnostic.category=invalid;
    struct census_json *fallback=census_json_new(&budget);assert(fallback);census_json_errors(fallback,&source);
    assert(!fallback->failed && strstr(fallback->data,"\"reason\":\"process_unavailable\""));
    census_json_free(fallback);
  }
  row.diagnostic.category=good;row.diagnostic.nonempty=16;
  struct census_json *fallback=census_json_new(&budget);assert(fallback);census_json_errors(fallback,&source);
  assert(!fallback->failed && strstr(fallback->data,"\"reason\":\"process_unavailable\""));census_json_free(fallback);
  row.diagnostic.nonempty=0;row.diagnostic.owner_present=2;
  fallback=census_json_new(&budget);assert(fallback);census_json_errors(fallback,&source);
  assert(!fallback->failed && strstr(fallback->data,"\"reason\":\"process_unavailable\""));census_json_free(fallback);
  row.diagnostic.owner_present=0;row.diagnostic.newline=2;
  fallback=census_json_new(&budget);assert(fallback);census_json_errors(fallback,&source);
  assert(!fallback->failed && strstr(fallback->data,"\"reason\":\"process_unavailable\""));census_json_free(fallback);
#endif
  census_json_free(out);assert(!budget.used);
}
static int diagnostic_tests(void) {
  assert(!clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start));ev.complete=false;
  for(unsigned mask=0;mask<16;mask++) {
    struct census_owned_identity p=diagnostic_identity();p.sid=mask&1?"fixture-sid":"";
    p.template=mask&2?"fixture-template":"";p.epoch=mask&4?1:0;
    if(mask&8) {memset(p.token,'a',64);p.token[64]=0;}
    if(mask==15) {assert(census_classify_owned(&p)==CENSUS_CLASS_MANAGED);continue;}
    char reason[64];snprintf(reason,sizeof reason,"process_unavailable:c4:o1:n%02x:x0",mask);
    diagnostic_error(&p,reason,true);
  }
  struct census_owned_identity p=diagnostic_identity();p.no_gc_environment=true;
  assert(census_classify_owned(&p)==CENSUS_CLASS_NONMANAGED);
  p.city="/fixture\ncontext";diagnostic_error(&p,"process_unavailable:c3:o0:n00:x1",true);
  p.city="/fixture\rcontext";diagnostic_error(&p,"process_unavailable:c3:o0:n00:x1",true);
  p=diagnostic_identity();ev.complete=true;diagnostic_error(&p,"process_unavailable",false);ev.complete=false;
  p.stat_revalidated=false;diagnostic_error(&p,"process_unavailable:c1:o1:n00:x0",true);
  p=diagnostic_identity();p.pgid=0;diagnostic_error(&p,"process_unavailable:c2:o1:n00:x0",true);
  puts("PASS finite classifier categories, ANY PRESENT vs FOUR NONEMPTY, seven-key FD3 UNKNOWN only, invalid-tag fallback, raw ledger unchanged");
  return 0;
}

#ifdef CURRENT_TRIPLET_PROTECTED
struct current_fixture {struct mixed_fixture base;bool late;const char *self;};
static bool current_enumeration(void *context,struct census_budget *b,struct census_pid_list *pids) {
  struct current_fixture *f=context;struct mixed_fixture *v=&f->base;v->scans++;
  if(v->scans==4) retire_leaf(&v->tree);
  if(f->late && v->scans==9) {
    int ready[2],control[2];assert(!pipe(ready));assert(!pipe(control));v->spare=fork();assert(v->spare>=0);
    if(!v->spare) {
      close(ready[0]);close(control[1]);char a[32],c[32];snprintf(a,sizeof a,"%d",ready[1]);snprintf(c,sizeof c,"%d",control[0]);
      char *args[]={(char *)f->self,"owned-leaf",a,c,NULL};char *env[]={"GC_CITY_PATH=/late-fixture",NULL};
      execve(f->self,args,env);_exit(2);
    }
    close(ready[1]);close(control[0]);char ack;assert(read(ready[0],&ack,1)==1);close(ready[0]);
    v->spare_control=control[1];v->spare_retired=false;
  }
  if(!census_pid_append(b,pids,(uint32_t)getpid())) return false;
  uint32_t root=(uint32_t)v->tree.root,leaf=(uint32_t)v->tree.leaf;
  if(v->scans<=4 && leaf<root && !census_pid_append(b,pids,leaf)) return false;
  if(!census_pid_append(b,pids,root)) return false;
  if(v->scans<=4 && leaf>root && !census_pid_append(b,pids,leaf)) return false;
  if(v->scans==1) {
    assert(write(v->spare_control,"x",1)==1);close(v->spare_control);int status;
    assert(waitpid(v->spare,&status,0)==v->spare && WIFEXITED(status) && !WEXITSTATUS(status));v->spare_retired=true;
    if(!census_pid_append(b,pids,(uint32_t)v->spare)) return false;
  } else if(f->late && v->scans>=9 && !census_pid_append(b,pids,(uint32_t)v->spare)) return false;
  return true;
}
int main(int argc,char **argv) {
  if(argc==4) return census_previous_kernel_main(argc,argv);
  assert(argc==2);unsigned mode=(unsigned)atoi(argv[1]);if(mode==7) return diagnostic_tests();assert(mode==9 || mode==10);
  struct rlimit limit={32,32};assert(!setrlimit(RLIMIT_NOFILE,&limit));init_evidence();
  ssize_t ns=readlink("/proc/self/ns/pid",ev.ns,sizeof ev.ns-1);assert(ns>0);ev.ns[ns]=0;strcpy(ev.binding.ns,ev.ns);
  struct current_fixture f={.base={.tree=start_tree(argv[0])},.late=mode==10,.self=argv[0]};
  int ready[2],control[2];assert(!pipe(ready));assert(!pipe(control));f.base.spare=fork();assert(f.base.spare>=0);
  if(!f.base.spare) {
    close(ready[0]);close(control[1]);char a[32],b[32];snprintf(a,sizeof a,"%d",ready[1]);snprintf(b,sizeof b,"%d",control[0]);
    char *args[]={argv[0],"owned-leaf",a,b,NULL};char *env[]={"GC_CITY_PATH=/spare-fixture",NULL};execve(argv[0],args,env);_exit(2);
  }
  close(ready[1]);close(control[0]);char ack;assert(read(ready[0],&ack,1)==1);close(ready[0]);f.base.spare_control=control[1];
  struct census_budget budget={.limit=16777216};struct census_global_source *s=census_global_new(&budget,&f,current_enumeration,mixed_capture);assert(s);
  struct census_resolution_ledger *r=census_resolution_new(&budget);assert(r);struct census_round_journal j={0};bool pass=false;
  if(mode==9) {
    enum census_round_result result=census_choose_proven_continuation(s,r,&j);
    pass=result==CENSUS_ROUNDS_SELECTED_PENDING_PROOF && s->history->n==4 && j.selected_first==2 && j.selected_second==3 && j.selected_seal==4 &&
      r->proofs_n==3 && r->certificates_n==1 && r->resolutions_n==2 && census_final_resolutions_valid(s,r,&j);
#ifdef GC_CENSUS_CLOSING_RECONCILE_H
    if(pass) {
      assert(census_current_closings_equal(s,r));
      uint64_t start=r->proofs[2].source.start;r->proofs[2].source.start++;assert(!census_current_closings_equal(s,r));r->proofs[2].source.start=start;
      r->proofs[2].protected_identity=false;assert(!census_current_closings_equal(s,r));r->proofs[2].protected_identity=true;
      r->certificates[0].retirement_proof=2;assert(!census_current_closings_equal(s,r));r->certificates[0].retirement_proof=3;
      unsigned count=r->resolutions_n;r->resolutions_n=0;assert(!census_current_closings_equal(s,r));r->resolutions_n=count;
      struct census_history_row *root=census_history_find(&s->history->scans[3],(uint32_t)f.base.tree.root);
      root->identity.uids[1]++;assert(!census_current_closings_equal(s,r));root->identity.uids[1]--;
      root->identity.declared_root=false;assert(!census_current_closings_equal(s,r));root->identity.declared_root=true;
      bool trusted=ev.trusted_kernel;ev.trusted_kernel=false;assert(!census_current_closings_equal(s,r));ev.trusted_kernel=trusted;
      bool old_fixture=fixture;fixture=true;assert(!census_current_closings_equal(s,r));fixture=old_fixture;
      assert(census_current_closings_equal(s,r));
      /* Raw capture flags/latches/counts still describe the failed seal. */
      assert(s->history->failed && s->fatal && !s->history->scans[3].receipt.seal_revalidated &&
        s->history->scans[1].receipt.classified==s->history->scans[3].receipt.classified+1);
      /* Actual owned C codec vector for the existing pure reader predicates;
       * never an authenticated helper FULL, provider join or host observation. */
      struct census_json *wire=census_json_new(&budget);assert(wire);
      census_json_text(wire,"{\"schema\":\"source-proc-census-vector/v1\",\"census\":");
      census_json_census(wire,s,r,&j);census_json_text(wire,",\"errors\":");census_json_errors(wire,s);
      uint64_t start_ms=(uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u;
      census_json_printf(wire,",\"errors_total\":%u,\"errors_truncated\":false,\"duration_ms\":%" PRIu64 ",\"controller_pid\":%u,\"certificate_disposition\":\"provisional\"}",
        s->errors_total,mono_ms()-start_ms,ev.binding.pid);
      assert(census_json_finalize(wire));printf("%.*s\n",(int)wire->n,wire->data);census_json_free(wire);
    }
#endif
  } else {
    /* Record ten genuine OWNED captures explicitly for a source guard test.
     * Earlier stable rounds may not be substituted for this final live birth. */
    for(unsigned scan=1;scan<=10;scan++) {
      unsigned round=scan==1?0:(scan+1)/3;enum census_scan_kind kind=scan==1?CENSUS_INITIAL:((scan-1)%3==0?CENSUS_SEAL:CENSUS_CLOSING);
      struct census_round_scan raw={0};(void)census_global_scan_record(s,scan,round,kind,&raw);
      assert(census_continuation_gate(s,r));
    }
#ifdef GC_CENSUS_CLOSING_RECONCILE_H
    pass=s->history->n==10 && !census_current_closings_equal(s,r) && !census_history_find(&s->history->scans[7],(uint32_t)f.base.spare) &&
      census_row_verified(census_history_find(&s->history->scans[8],(uint32_t)f.base.spare)) && census_row_verified(census_history_find(&s->history->scans[9],(uint32_t)f.base.spare));
#endif
  }
  if(!f.base.spare_retired) {assert(write(f.base.spare_control,"x",1)==1);close(f.base.spare_control);int status;assert(waitpid(f.base.spare,&status,0)==f.base.spare);}
  retire_tree(&f.base.tree);census_resolution_free(&budget,r);census_global_free(s);assert(!budget.used && !budget.fd_failed);close(ev.procfd);
  printf("%s Source protected CURRENT triplet mode%u; no retrospective seal, raw faults/latches retained, strict proof/cert/root/UID/namespace negatives\n",pass?"PASS":"RED",mode);return pass?0:1;
}
#else
int main(int argc,char **argv) {
  if(argc==2 && !strcmp(argv[1],"7")) return diagnostic_tests();
  return standalone_previous_main(argc,argv);
}
#endif
