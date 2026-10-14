/* PREP fixture only: finite late births in an OWN process tree. No host
 * enumeration/helper/RPC/provider acceptance; no fake errno or exit proofs. */
#define CENSUS_KERNEL_MAIN rounds_previous_main
#include "census-v3-kernel-test.c"
#include "census-continuation.h"
#include "census-json.h"
struct burst_fixture {struct owned_tree tree;const char *self;unsigned scan,bursts,n;pid_t pid[10];int gate[2];};
static void birth(struct burst_fixture *f) {
  int ready[2];assert(pipe(ready)==0);
  pid_t pid=fork();assert(pid>=0);
  if(!pid){
    close(ready[0]);close(f->gate[1]);close(f->tree.control);close(f->tree.ready);
    char a[32],b[32];snprintf(a,sizeof a,"%d",ready[1]);snprintf(b,sizeof b,"%d",f->gate[0]);
    char *args[]={(char *)f->self,"owned-leaf",a,b,NULL};char *env[]={"GC_CITY_PATH=/round-burst-fixture",NULL};
    execve(f->self,args,env);_exit(2);
  }
  close(ready[1]);char ack;assert(read(ready[0],&ack,1)==1);close(ready[0]);
  f->pid[f->n++]=pid;
}
static int pid_compare(const void *a,const void *b) {uint32_t x=*(const uint32_t *)a,y=*(const uint32_t *)b;return(x>y)-(x<y);}
static bool enumerate_burst(void *context,struct census_budget *b,struct census_pid_list *out) {
  struct burst_fixture *f=context;f->scan++;
  if(f->scan>1 && (f->scan-1)%3==0 && (f->scan-1)/3<=f->bursts)birth(f);
  uint32_t pid[13]={(uint32_t)getpid(),(uint32_t)f->tree.root,(uint32_t)f->tree.leaf};
  for(unsigned i=0;i<f->n;i++)pid[3+i]=(uint32_t)f->pid[i];
  qsort(pid,3+f->n,sizeof *pid,pid_compare);
  for(unsigned i=0;i<3+f->n;i++)if(!census_pid_append(b,out,pid[i]))return false;
  return true;
}
int main(int argc,char **argv) {
  if(argc==4)return rounds_previous_main(argc,argv);
  assert(argc==2);unsigned bursts=(unsigned)atoi(argv[1]);assert(bursts==3 || bursts==9 || bursts==10);
  struct rlimit limit={32,32};assert(setrlimit(RLIMIT_NOFILE,&limit)==0);init_evidence();
  ssize_t ns=readlink("/proc/self/ns/pid",ev.ns,sizeof ev.ns-1);assert(ns>0);ev.ns[ns]=0;strcpy(ev.binding.ns,ev.ns);
  struct burst_fixture f={.tree=start_tree(argv[0]),.self=argv[0],.bursts=bursts};
  assert(pipe(f.gate)==0);
  struct census_budget budget={.limit=16777216};
  struct census_global_source *s=census_global_new(&budget,&f,enumerate_burst,census_fixed_capture);assert(s);
  struct census_resolution_ledger *r=census_resolution_new(&budget);assert(r);struct census_round_journal j={0};
  enum census_round_result result=census_choose_proven_continuation(s,r,&j);
  bool accepted=result==CENSUS_ROUNDS_SELECTED_PENDING_PROOF && census_final_resolutions_valid(s,r,&j);
  unsigned expected=1+3*(bursts+1);bool pass=bursts==10?!accepted:(accepted && s->history->n==expected && j.selected_seal==expected);
  /* Preserve baseline failure evidence and reap every own child BEFORE return. */
  printf("bursts=%u scans=%u selected_seal=%u accepted=%d peak_fd=%u retained=%zu\n",bursts,s->history->n,j.selected_seal,accepted,budget.fd_peak,budget.peak);
  printf("diagnostic fatal=%d history_failed=%d fd_failed=%d fd_live=%u errors=%u total=%u truncated=%d resolutions=%u proofs=%u errno=%d\n",s->fatal,s->history->failed,budget.fd_failed,budget.fd_live,s->errors_n,s->errors_total,s->truncated,r->resolutions_n,r->proofs_n,errno);
  for(unsigned i=0;i<s->errors_n;i++)printf("fault scan=%u reason=%s operation=%s errno=%d\n",s->errors[i].scan_index,s->errors[i].reason,s->errors[i].raw.operation?s->errors[i].raw.operation:"none",s->errors[i].raw.error);
  if(accepted) {
    struct census_history_row *root=census_history_find(&s->history->scans[j.selected_seal-1],(uint32_t)f.tree.root);assert(root);
    root->identity.uids[1]++;assert(!census_current_closings_equal(s,r));root->identity.uids[1]--;
    root->identity.uids_revalidated=false;assert(!census_current_closings_equal(s,r));root->identity.uids_revalidated=true;
    enum census_row_failure saved=root->failure;root->failure=CENSUS_ROW_CAPTURE_FAILED;assert(!census_current_closings_equal(s,r));root->failure=saved;
    assert(census_current_closings_equal(s,r));
    puts("producer_guard_uid_mutation=DENY uids_not_revalidated=DENY missing_root_capture=DENY restored=ACCEPT");
    struct census_json *wire=census_json_new(&budget);assert(wire);
    census_json_text(wire,"{\"schema\":\"source-proc-census-vector/v1\",\"census\":");
    census_json_census(wire,s,r,&j);census_json_text(wire,",\"errors\":");census_json_errors(wire,s);
    uint64_t origin=(uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u;
    census_json_printf(wire,",\"errors_total\":%u,\"errors_truncated\":false,\"duration_ms\":%" PRIu64 ",\"controller_pid\":%u,\"certificate_disposition\":\"provisional\"}",s->errors_total,mono_ms()-origin,ev.binding.pid);
    assert(census_json_finalize(wire));printf("%.*s\n",(int)wire->n,wire->data);census_json_free(wire);
  }
  close(f.gate[0]);for(unsigned i=0;i<f.n;i++)assert(write(f.gate[1],"x",1)==1);close(f.gate[1]);
  for(unsigned i=0;i<f.n;i++){int status;assert(waitpid(f.pid[i],&status,0)==f.pid[i] && WIFEXITED(status) && !WEXITSTATUS(status));}
  retire_tree(&f.tree);census_resolution_free(&budget,r);census_global_free(s);assert(!budget.used && !budget.fd_failed);close(ev.procfd);
  return pass?0:1;
}
