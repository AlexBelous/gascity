/* Controlled SAME-child full-census producer-to-reader fixture. Synthetic ENV
 * failures are explicitly inputs; exit/FD/UID/stat witnesses are real Linux.
 * No host scan, RPC, provider authority or production caller is exercised. */
#define GC_HELPER_TEST
#define HELPER_SOURCE_REVISION "691129d3f90a600bc647641efad5ca2c9e84e277"
#define main observer_fixture_main
#include "observer.c"
#undef main
#include "census-v3-reader.h"
#include "census-json.h"
#include <assert.h>
#include <sys/resource.h>
#include <sys/prctl.h>
#include <sys/wait.h>
static pid_t child[5];static int gates[5];static bool visible[5];static unsigned scanning;
static bool automatic;static bool injected[5],released[5];
static void terminal(unsigned,bool);static void reap(unsigned);
static int inject_env(uint32_t pid) {
 if(scanning==2 && pid==(uint32_t)child[0]) {injected[0]=true;return ESRCH;}
 if(scanning==5 && pid==(uint32_t)child[2]) {injected[2]=true;return ESRCH;}
 return 0;
}
static bool enumerate_fixture(void *unused,struct census_budget *budget,struct census_pid_list *out) {
 (void)unused;
 if(automatic) {
  scanning++;
  if(scanning==3) reap(0);
  if(scanning==4) {terminal(3,true);visible[2]=true;}
  if(scanning==6) reap(2);
  if(scanning==7) terminal(1,false);
  if(scanning==8) reap(1);
 }
 uint32_t pids[6];size_t n=0;pids[n++]=getpid();
 for(unsigned i=0;i<5;i++) if(visible[i]) pids[n++]=child[i];
 for(size_t i=0;i<n;i++) for(size_t j=i+1;j<n;j++) if(pids[j]<pids[i]) {uint32_t v=pids[i];pids[i]=pids[j];pids[j]=v;}
 for(size_t i=0;i<n;i++) if(!census_pid_append(budget,out,pids[i])) return false;
 return true;
}
static void terminal(unsigned i,bool reap) {
 assert(write(gates[i],"x",1)==1);close(gates[i]);gates[i]=-1;
 siginfo_t info;assert(!waitid(P_PID,child[i],&info,WEXITED|WNOWAIT));
 if(reap) {int status;assert(waitpid(child[i],&status,0)==child[i]);visible[i]=false;}
}
static void reap(unsigned i) {int status;assert(waitpid(child[i],&status,0)==child[i]);visible[i]=false;}
static void schedule_after_error(uint32_t pid) {
 if(!automatic || pid!=(uint32_t)child[4]) return;
 for(unsigned i=0;i<3;i+=2) if(injected[i] && !released[i]) {terminal(i,false);released[i]=true;}
}
int main(int argc,char **argv) {
 automatic=argc==2 && !strcmp(argv[1],"automatic");
 struct rlimit limit={32,32};assert(!setrlimit(RLIMIT_NOFILE,&limit));
 struct census_budget budget={.limit=16*1024*1024};memset(&ev,0,sizeof ev);
 ev.procfd=open("/proc",O_RDONLY|O_DIRECTORY|O_CLOEXEC);assert(ev.procfd>=0);
 ev.binding.pid=getpid();ev.trusted_kernel=true;assert(!clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start));
 ssize_t n=readlink("/proc/self/ns/pid",ev.ns,sizeof ev.ns-1);assert(n>0);ev.ns[n]=0;strcpy(ev.binding.ns,ev.ns);
 for(unsigned i=0;i<5;i++) {
  int gate[2],ready[2];assert(!pipe(gate)&&!pipe(ready));child[i]=fork();assert(child[i]>=0);
  if(!child[i]) {close(gate[1]);close(ready[0]);
   for(unsigned j=0;j<i;j++) close(gates[j]);
   assert(!prctl(PR_SET_NAME,i==1?"gc":"bd"));assert(write(ready[1],"r",1)==1);close(ready[1]);
   char x;ssize_t got=read(gate[0],&x,1);close(gate[0]);_exit(got==1?0:2);
  }
  close(gate[0]);close(ready[1]);char x;assert(read(ready[0],&x,1)==1);close(ready[0]);gates[i]=gate[1];visible[i]=i!=2;
 }
 struct census_global_source *s=census_global_new(&budget,NULL,enumerate_fixture,census_fixed_capture);
 struct census_resolution_ledger *r=census_resolution_new(&budget);assert(s&&r);
 struct census_round_journal journal={0};census_test_environ_error=inject_env;
 if(automatic) {
  census_test_before_status=schedule_after_error;
  assert(census_choose_proven_continuation(s,r,&journal)==CENSUS_ROUNDS_SELECTED_PENDING_PROOF);
  assert(journal.n==10 && journal.selected_first==8 && journal.selected_second==9 && journal.selected_seal==10);
  fprintf(stderr,"actual_production_selector_mixed_ABCD_8_9_10_BOTH_live_survivor=PASS\n");
 } else for(scanning=1;scanning<=10;scanning++) {
  if(scanning==4) {terminal(3,true);visible[2]=true;}
  if(scanning==7) terminal(1,false);
  struct census_round_scan receipt;
  (void)census_global_scan_record(s,scanning,scanning==1?0:(scanning+1)/3,
    scanning==1?CENSUS_INITIAL:((scanning-1)%3==0?CENSUS_SEAL:CENSUS_CLOSING),&receipt);
  journal.scans[journal.n++]=receipt;
  if(scanning==2) terminal(0,false);
  if(scanning==5) terminal(2,false);
  if(scanning==2 || scanning==7) {
    struct census_resolution_ledger before={0};unsigned target=0;
    for(unsigned i=0;i<s->errors_n;i++) if(s->errors[i].raw.pid==(uint32_t)child[scanning==2?0:1]) target=i+1;
    assert(target && !census_resolve_standalone(s,&before,target));
    assert(!before.proofs_n && !before.resolutions_n);
    fprintf(stderr,"existing_grammar_%s=RED exact_owned_raw_kept\n",scanning==2?"A":"B");
  }
  if(scanning==4) {
    const struct census_history_row *prior=census_history_find(&s->history->scans[2],child[3]);
    assert(prior && !census_closing_retired(s,r,&s->history->scans[2],prior));
    fprintf(stderr,"existing_missing_row_without_indexed_D_proof=RED\n");
  }
  if(!census_continuation_gate(s,r)) {
   fprintf(stderr,"gate failed scan=%u proofs=%u res=%u errors=%u denied=%d\n",scanning,r->proofs_n,r->resolutions_n,s->errors_n,r->denied);
   for(unsigned i=0;i<s->errors_n;i++) fprintf(stderr,"raw %u %s/%s/%d pid=%u start=%" PRIu64 "\n",s->errors[i].scan_index,s->errors[i].reason,s->errors[i].raw.operation,s->errors[i].raw.error,s->errors[i].raw.pid,s->errors[i].raw.start);
   return 2;
  }
  if(scanning==2) reap(0);
  if(scanning==5) reap(2);
  if(scanning==7) reap(1);
  if(scanning==4 || scanning==7) assert(!census_current_closings_equal(s,r));
 }
 assert(census_current_closings_equal(s,r));
 journal.selected_first=8;journal.selected_second=9;journal.selected_seal=10;
 for(unsigned i=0;i<r->resolutions_n;i++) if(r->resolutions[i].kind==CENSUS_RESOLUTION_KNOWN_TERMINAL || r->resolutions[i].kind==CENSUS_RESOLUTION_BOUND_BIRTH) r->resolutions[i].selected_seal=10;
 for(unsigned i=0;i<r->coverage_n;i++) r->coverage[i].selected_seal=10;
 assert(census_finish_births(s,r,&journal));assert(census_final_resolutions_valid(s,r,&journal));
 assert(!census_finish_retired_birth(s,r,&journal,2));
 fprintf(stderr,"legacy_C_laterFULL_pair_not_waived=RED distinct_bound_birth_branch=GREEN\n");
 assert(s->errors_n==4 && r->resolutions_n==4 && r->coverage_n==1 && r->proofs_n==5);
 /* Same immutable whole ledger: meaningful authority/time/group mutations. */
 bool trusted=ev.trusted_kernel;ev.trusted_kernel=false;assert(!census_final_resolutions_valid(s,r,&journal));ev.trusted_kernel=trusted;
 fixture=true;assert(!census_final_resolutions_valid(s,r,&journal));fixture=false;
 char namespace[80];strcpy(namespace,ev.binding.ns);ev.binding.ns[0]=0;
 assert(!census_final_resolutions_valid(s,r,&journal));strcpy(ev.binding.ns,namespace);
 bool fd_failed=budget.fd_failed;budget.fd_failed=true;assert(!census_final_resolutions_valid(s,r,&journal));budget.fd_failed=fd_failed;
 struct census_typed_proof saved_proof=r->proofs[0];r->proofs[0].exit_witness.uids_after[0]++;
 assert(!census_final_resolutions_valid(s,r,&journal));r->proofs[0]=saved_proof;
 r->proofs[0].exit_witness.stage=0;assert(!census_final_resolutions_valid(s,r,&journal));r->proofs[0]=saved_proof;
 r->proofs[0].source.offset_ms=10000;assert(!census_final_resolutions_valid(s,r,&journal));r->proofs[0]=saved_proof;
 struct census_typed_resolution saved_link=r->resolutions[0];r->resolutions[0].error_index=0;
 assert(!census_final_resolutions_valid(s,r,&journal));r->resolutions[0]=saved_link;
 r->resolutions[0].selected_seal=7;assert(!census_final_resolutions_valid(s,r,&journal));r->resolutions[0]=saved_link;
 uint32_t pid=r->coverage[0].pid;r->coverage[0].pid++;
 assert(!census_final_resolutions_valid(s,r,&journal));r->coverage[0].pid=pid;
 char digest=s->history->scans[3].receipt.raw_digest[0];s->history->scans[3].receipt.raw_digest[0]=digest=='0'?'1':'0';
 assert(!census_final_resolutions_valid(s,r,&journal));s->history->scans[3].receipt.raw_digest[0]=digest;
 struct census_history_row *original=census_history_find(&s->history->scans[0],child[0]);assert(original);
 bool declared=original->identity.declared_root;original->identity.declared_root=true;
 assert(!census_final_resolutions_valid(s,r,&journal));original->identity.declared_root=declared;
 enum census_row_class klass=original->classification;original->classification=CENSUS_CLASS_MANAGED;
 assert(!census_final_resolutions_valid(s,r,&journal));original->classification=klass;
 char *name=original->identity.name;original->identity.name="tmux";
 assert(!census_final_resolutions_valid(s,r,&journal));original->identity.name=name;
 assert(census_final_resolutions_valid(s,r,&journal));
 fprintf(stderr,"mixed_whole_ledger_negative_guards=14 PASS source_namespace_budget_UID_missing_witness_time_error_use_selected_seal_numeric_protection\n");
 struct census_json *json=census_json_new(&budget);assert(json);ev.complete=true;
 census_json_text(json,"{\"census\":");census_json_census(json,s,r,&journal);
 census_json_text(json,",\"errors\":");census_json_errors(json,s);
 uint64_t start=(uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u;
 census_json_printf(json,",\"errors_total\":%u,\"errors_truncated\":false,\"duration_ms\":%" PRIu64 ",\"controller_pid\":%u}",s->errors_total,mono_ms()-start,(unsigned)getpid());
 assert(!json->failed);assert(fwrite(json->data,1,json->n,stdout)==json->n);putchar('\n');
 terminal(4,true);
 census_json_free(json);census_resolution_free(&budget,r);census_global_free(s);assert(!budget.used&&!budget.fd_failed);close(ev.procfd);
 return 0;
}
