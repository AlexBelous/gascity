/* Real owned standalone + protected descendant in one indexed ledger. */
#define CENSUS_KERNEL_MAIN census_previous_kernel_main
#include "census-v3-kernel-test.c"
#include "census-continuation.h"
struct mixed_fixture {struct owned_tree tree;unsigned scans;pid_t spare;int spare_control;bool spare_retired;};
static bool mixed_enumeration(void *context,struct census_budget *b,struct census_pid_list *pids) {
  struct mixed_fixture *f=context;
  f->scans++;
  if(f->scans==2) retire_leaf(&f->tree);
  if(!census_pid_append(b,pids,(uint32_t)getpid())) return false;
  uint32_t root=(uint32_t)f->tree.root,leaf=(uint32_t)f->tree.leaf;
  if(f->scans<=2 && leaf<root && !census_pid_append(b,pids,leaf)) return false;
  if(!census_pid_append(b,pids,root)) return false;
  if(f->scans<=2 && leaf>root && !census_pid_append(b,pids,leaf)) return false;
  if(f->scans==1) {
    assert(write(f->spare_control,"x",1)==1);close(f->spare_control);int status;
    assert(waitpid(f->spare,&status,0)==f->spare && WIFEXITED(status) && !WEXITSTATUS(status));f->spare_retired=true;
    assert(pids->n && pids->p[pids->n-1]<(uint32_t)f->spare);
    if(!census_pid_append(b,pids,(uint32_t)f->spare)) return false;
  }
  return true;
}
static bool mixed_capture(void *context,uint32_t pid,struct census_budget *b,
    struct census_owned_identity *out,struct census_capture_fault *fault) {
  (void)context;return census_capture_bounded_evidenced(pid,b,out,fault);
}
int main(int argc,char **argv) {
  if(argc==4) return census_previous_kernel_main(argc,argv);
  struct rlimit limit={32,32};assert(!setrlimit(RLIMIT_NOFILE,&limit));init_evidence();
  ssize_t ns=readlink("/proc/self/ns/pid",ev.ns,sizeof ev.ns-1);assert(ns>0);ev.ns[ns]=0;strcpy(ev.binding.ns,ev.ns);
  struct mixed_fixture f={.tree=start_tree(argv[0])};
  int ready[2],control[2];assert(!pipe(ready));assert(!pipe(control));f.spare=fork();assert(f.spare>=0);
  if(!f.spare) {
    close(ready[0]);close(control[1]);char a[32],b[32];snprintf(a,sizeof a,"%d",ready[1]);snprintf(b,sizeof b,"%d",control[0]);
    char *args[]={argv[0],"owned-leaf",a,b,NULL};char *env[]={"GC_CITY_PATH=/fixture-context",NULL};execve(argv[0],args,env);_exit(2);
  }
  close(ready[1]);close(control[0]);char ack;assert(read(ready[0],&ack,1)==1);close(ready[0]);f.spare_control=control[1];
  struct census_budget b={.limit=16777216};struct census_global_source *s=census_global_new(&b,&f,mixed_enumeration,mixed_capture);assert(s);
  struct census_resolution_ledger *r=census_resolution_new(&b);assert(r);struct census_round_journal j={0};
  enum census_round_result result=census_choose_proven_continuation(s,r,&j);
  bool pass=result==CENSUS_ROUNDS_SELECTED_PENDING_PROOF && r->proofs_n==3 && r->certificates_n==1 && r->resolutions_n==2;
  if(pass) {
    assert(r->certificates[0].absence_proof==2 && r->certificates[0].retirement_proof==3 &&
      r->resolutions[0].error_index==1 && r->resolutions[0].proof_index==1 &&
      r->resolutions[1].error_index==2 && r->resolutions[1].proof_index==2 && census_final_resolutions_valid(s,r,&j));
    r->certificates[0].absence_proof=1;assert(!census_final_resolutions_valid(s,r,&j));r->certificates[0].absence_proof=2;
    r->proofs[2].certificate_id=0;assert(!census_final_resolutions_valid(s,r,&j));r->proofs[2].certificate_id=1;
    r->resolutions[0].proof_index=2;assert(!census_final_resolutions_valid(s,r,&j));r->resolutions[0].proof_index=1;
    assert(census_final_resolutions_valid(s,r,&j));
  }
  printf("%s mixed genuine standaloneNULL index1 then protectedmanaged pair2/3 cert1; originals/errors preserved; wrong certificate/ref/downgrade DENY\n",pass?"PASS":"RED");
  if(!f.spare_retired) {assert(write(f.spare_control,"x",1)==1);close(f.spare_control);int status;assert(waitpid(f.spare,&status,0)==f.spare);}
  retire_tree(&f.tree);census_resolution_free(&b,r);census_global_free(s);assert(!b.used && !b.fd_failed);close(ev.procfd);return pass?0:1;
}
