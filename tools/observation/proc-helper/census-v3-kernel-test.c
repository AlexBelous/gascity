/* Owned root/descendant only. No host census, signals, capabilities, databases,
 * installed helper, controller RPC or provider authority. */
#define GC_HELPER_TEST
#define main observer_fixture_main
#include "observer.c"
#undef main
#include "census-v3-reader.h"
#include "census-scan-history.h"
#include "census-descendant-witness.h"
#include <assert.h>
#include <sys/resource.h>
#include <sys/wait.h>

struct owned_tree {pid_t root,leaf;int control,ready;};
static bool owned_history_capture(void *context,uint32_t pid,
    struct census_budget *budget,struct census_owned_identity *out,struct census_capture_fault *fault) {
  struct owned_tree *tree=context;
  bool ok=census_capture_bounded_evidenced(pid,budget,out,fault);
  if(ok && pid==(uint32_t)tree->root) out->declared_root=true;
  return ok;
}
static void init_evidence(void) {
  memset(&ev,0,sizeof ev);ev.complete=true;ev.processfd=-1;ev.active_pidfd=-1;
  ev.procfd=open("/proc",O_RDONLY|O_DIRECTORY|O_CLOEXEC);assert(ev.procfd>=0);
  ev.binding.pid=(uint32_t)getpid();ev.trusted_kernel=true;ev.scan_index=1;
  assert(!clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start));
}
static struct owned_tree start_tree(const char *self) {
  int ready[2],control[2];assert(!pipe(ready));assert(!pipe(control));
  pid_t root=fork();assert(root>=0);
  if(!root) {
    close(ready[0]);close(control[1]);
    char a[32],b[32];snprintf(a,sizeof a,"%d",ready[1]);snprintf(b,sizeof b,"%d",control[0]);
    char *args[]={(char *)self,"owned-root",a,b,NULL};
    char *env[]={"GC_SESSION_ID=v3-owned","GC_CITY_PATH=/disposable-v3-test",
      "GC_TEMPLATE=fixture.worker","GC_RUNTIME_EPOCH=1","GC_INSTANCE_TOKEN=private-owned-v3-token",NULL};
    execve(self,args,env);_exit(2);
  }
  close(ready[1]);close(control[0]);pid_t leaf;
  assert(read(ready[0],&leaf,sizeof leaf)==sizeof leaf);
  return (struct owned_tree){root,leaf,control[1],ready[0]};
}
static void retire_leaf(struct owned_tree *tree) {
  assert(write(tree->control,"l",1)==1);char ack;
  assert(read(tree->ready,&ack,1)==1 && ack=='d');
}
static void retire_tree(struct owned_tree *tree) {
  assert(write(tree->control,"r",1)==1);close(tree->control);close(tree->ready);
  int status;assert(waitpid(tree->root,&status,0)==tree->root);
  assert(WIFEXITED(status) && WEXITSTATUS(status)==0);
}
#ifndef CENSUS_KERNEL_MAIN
#define CENSUS_KERNEL_MAIN main
#endif
int CENSUS_KERNEL_MAIN(int argc,char **argv) {
  if(argc==4 && !strcmp(argv[1],"owned-leaf")) {
    char ack='b';assert(write(atoi(argv[2]),&ack,1)==1);close(atoi(argv[2]));
    char done;assert(read(atoi(argv[3]),&done,1)==1);close(atoi(argv[3]));return 0;
  }
  if(argc==4 && !strcmp(argv[1],"owned-root")) {
    int ready=atoi(argv[2]),control=atoi(argv[3]),lr[2],lc[2];
    assert(!pipe(lr));assert(!pipe(lc));pid_t leaf=fork();assert(leaf>=0);
    if(!leaf) {
      close(lr[0]);close(lc[1]);close(ready);close(control);
      char a[32],b[32];snprintf(a,sizeof a,"%d",lr[1]);snprintf(b,sizeof b,"%d",lc[0]);
      char *args[]={argv[0],"owned-leaf",a,b,NULL};
      execve(argv[0],args,environ);_exit(2);
    }
    close(lr[1]);close(lc[0]);char ack;
    assert(read(lr[0],&ack,1)==1);close(lr[0]);
    assert(write(ready,&leaf,sizeof leaf)==sizeof leaf);bool alive=true;char cmd;
    while(read(control,&cmd,1)==1) {
      if((cmd=='l' || cmd=='r') && alive) {
        assert(write(lc[1],"x",1)==1);close(lc[1]);int status;
        assert(waitpid(leaf,&status,0)==leaf && WIFEXITED(status) && WEXITSTATUS(status)==0);alive=false;
      }
      if(cmd=='l') assert(write(ready,"d",1)==1);
      if(cmd=='r') break;
    }
    assert(!alive);close(ready);close(control);return 0;
  }
  struct utsname uname_result;assert(!uname(&uname_result));assert(!strncmp(uname_result.release,"6.8.",4));
  struct rlimit fdlimit={32,32};assert(!setrlimit(RLIMIT_NOFILE,&fdlimit));
  init_evidence();struct owned_tree tree=start_tree(argv[0]);
  struct census_budget budget={.limit=16777216};
  struct census_owned_identity *rows=census_alloc(&budget,3*sizeof *rows);assert(rows);
  assert(census_capture_owned((uint32_t)tree.root,&budget,&rows[0]));
  assert(census_capture_owned((uint32_t)tree.leaf,&budget,&rows[1]));
  assert(rows[0].uids[1]==geteuid() && rows[1].uids[1]==geteuid());
  assert(rows[0].ppid==(uint32_t)getpid() && rows[1].ppid==(uint32_t)tree.root);
  // This proves declared proc classification, not provider ownership. The root
  // is an explicitly owned env-root with a directly verified non-GC parent.
  struct census_owned_identity parent_row={0};
  assert(census_capture_owned((uint32_t)getpid(),&budget,&parent_row));
  assert(parent_row.no_gc_environment && !*parent_row.sid);
  rows[0].declared_root=true;
  census_identity_clear(&budget,&parent_row);
  struct census_history *history=census_history_new(&budget);assert(history);
  uint32_t owned_pids[2]={(uint32_t)tree.root,(uint32_t)tree.leaf};
  if(owned_pids[0]>owned_pids[1]) {uint32_t swap=owned_pids[0];owned_pids[0]=owned_pids[1];owned_pids[1]=swap;}
  assert(census_history_append(history,&budget,owned_pids,2,0,CENSUS_INITIAL,1,
    &tree,owned_history_capture));
  assert(history->scans[0].receipt.classified==2 && !history->failed);
  for(size_t i=0;i<2;i++) {
    assert(history->scans[0].rows[i].classification==CENSUS_CLASS_MANAGED);
    assert(history->scans[0].rows[i].identity.uids[1]==geteuid());
  }
  retire_leaf(&tree);
  assert(!census_history_append(history,&budget,owned_pids,2,1,CENSUS_CLOSING,2,
    &tree,owned_history_capture));
  assert(history->n==2 && history->failed && history->scans[1].receipt.classified==1);
  size_t leaf_index=owned_pids[0]==(uint32_t)tree.leaf?0:1;
  assert(history->scans[1].rows[leaf_index].pid==(uint32_t)tree.leaf &&
    history->scans[1].rows[leaf_index].failure==CENSUS_ROW_CAPTURE_FAILED);
  assert(history->scans[1].rows[leaf_index].raw_fault.pid==(uint32_t)tree.leaf &&
    history->scans[1].rows[leaf_index].raw_fault.error==ESRCH &&
    !strcmp(history->scans[1].rows[leaf_index].raw_fault.operation,"pidfd_open"));
  assert(history->scans[0].rows[leaf_index].identity.start==rows[1].start);
  /* A failed capture is NOT an absence certificate and cannot erase history. */
  census_history_free(&budget,history);
  ev.scan_index=3;struct scan scan={0};struct process absent={.pid=(uint32_t)tree.leaf};
  assert(read_process(&scan,&absent) && absent.absent && !absent.start);
  assert(ev.proofs_n==1 && ev.proofs[0].kind==CENSUS_ENUMERATED_ABSENT &&
    ev.proofs[0].pid==(uint32_t)tree.leaf && ev.proofs[0].scan_index==3 && !ev.proofs[0].start && !strcmp(ev.proofs[0].method,"pidfd_no_pid"));
  assert(census_capture_owned((uint32_t)tree.root,&budget,&rows[2]));rows[2].declared_root=true;
  assert(census_immediate_descendant_candidate(&rows[1],&rows[0],&rows[2]));
  struct census_owned_identity chain[2]={rows[1],rows[0]};
  struct census_descendant_witness witness={0};
  assert(census_descendant_no_pid(&budget,chain,2,&rows[2],3,&witness));
  assert(witness.provisional && witness.absence.start==0 &&
    witness.retirement.start==rows[1].start && witness.retirement.pid==rows[1].pid &&
    witness.absence.offset_ms==witness.retirement.offset_ms && witness.chain_n==2);
  assert(witness.chain[0].sid!=rows[1].sid && !strcmp(witness.chain[0].sid,rows[1].sid));
  census_descendant_witness_clear(&budget,&witness);
  chain[0].declared_root=true;
  assert(!census_descendant_no_pid(&budget,chain,2,&rows[2],3,&witness));
  chain[0].declared_root=false;chain[0].ppid++;
  assert(!census_descendant_no_pid(&budget,chain,2,&rows[2],3,&witness));chain[0].ppid--;
  rows[2].start++;
  assert(!census_descendant_no_pid(&budget,chain,2,&rows[2],3,&witness));rows[2].start--;
  struct process protected_old={.pid=rows[1].pid,.start=rows[1].start,.sid=rows[1].sid};
  memcpy(protected_old.token,rows[1].token,sizeof protected_old.token);
  assert(missing_proof(&protected_old,NULL,3)==0); // broad V2 guard unchanged
  rows[2].start++;assert(!census_immediate_descendant_candidate(&rows[1],&rows[0],&rows[2]));rows[2].start--;
  rows[1].ppid++;assert(!census_immediate_descendant_candidate(&rows[1],&rows[0],&rows[2]));rows[1].ppid--;
  rows[1].token[0]=rows[1].token[0]=='a'?'b':'a';
  assert(!census_immediate_descendant_candidate(&rows[1],&rows[0],&rows[2]));
  retire_tree(&tree);
  struct census_owned_identity lost={0};assert(!census_capture_owned(rows[0].pid,&budget,&lost));
  assert(!census_descendant_no_pid(&budget,chain,2,&lost,4,&witness));
  struct process protected_root={.pid=rows[0].pid,.start=rows[0].start,.sid=rows[0].sid,.root=true};
  assert(missing_proof(&protected_root,NULL,4)==0);
  for(size_t i=0;i<3;i++) census_identity_clear(&budget,&rows[i]);
  census_release(&budget,rows,3*sizeof *rows);assert(budget.used==0 && budget.peak>0 && budget.peak<=budget.limit);
  assert(budget.fd_tracking && !budget.fd_failed && budget.fd_peak<=32);
  /* Own pipe descriptors closed by fixture retirement outside reader tracking. */
  close(ev.procfd);
  printf("PASS actual kernel=%s UID=%u NOFILE32 charged_capture_peak=%zu reader_fd_peak=%u: owned full row history and exact failed retired-leaf capture retained; owned inherited descendant NULL witness/continuing declared root; true-root loss/changed incarnation/unknown ancestry/token mismatch DENY; provider join still required, no COMPLETE frame\n",uname_result.release,(unsigned)geteuid(),budget.peak,budget.fd_peak);
  return 0;
}
