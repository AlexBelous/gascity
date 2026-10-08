/* Genuine owned-child status disappearance after a successful charged stat.
 * Root continues; no errno injection, signals, host census or live helper. */
#define CENSUS_KERNEL_MAIN census_previous_status_kernel_main
#include "census-v3-kernel-test.c"
#include "census-envelope.h"
struct status_fixture {struct owned_tree tree;unsigned scans;bool hook_fired;};
static struct status_fixture *status_active;
static void owned_status_exit(uint32_t pid) {
  if(!status_active || status_active->scans!=2 || pid!=(uint32_t)status_active->tree.leaf) return;
  assert(!status_active->hook_fired);census_test_before_status=NULL;
  retire_leaf(&status_active->tree);status_active->hook_fired=true;
}
static bool status_enumeration(void *context,struct census_budget *b,struct census_pid_list *pids) {
  struct status_fixture *f=context;f->scans++;
  uint32_t root=(uint32_t)f->tree.root,leaf=(uint32_t)f->tree.leaf;
  assert((uint32_t)getpid()<root && (uint32_t)getpid()<leaf);
  if(!census_pid_append(b,pids,(uint32_t)getpid())) return false;
  if(f->scans<=2 && leaf<root && !census_pid_append(b,pids,leaf)) return false;
  if(!census_pid_append(b,pids,root)) return false;
  return f->scans>2 || leaf<root || census_pid_append(b,pids,leaf);
}
int main(int argc,char **argv) {
  if(argc==4) return census_previous_status_kernel_main(argc,argv);
  struct rlimit fdlimit={32,32};assert(!setrlimit(RLIMIT_NOFILE,&fdlimit));
  init_evidence();struct status_fixture f={.tree=start_tree(argv[0])};status_active=&f;
  census_test_before_status=owned_status_exit;
  struct census_budget b={.limit=16777216};
  struct census_global_source *s=census_global_new(&b,&f,status_enumeration,census_fixed_capture);assert(s);
  struct census_resolution_ledger *r=census_resolution_new(&b);assert(r);
  struct census_round_journal j={0};enum census_round_result result=census_choose_proven_continuation(s,r,&j);
  assert(f.hook_fired && s->history->n>=2 && s->errors_n==1);
  const struct census_global_fault *error=&s->errors[0];
  assert(!strcmp(error->raw.operation,"status") && error->raw.start &&
    error->raw.pid==(uint32_t)f.tree.leaf && (error->raw.error==ENOENT || error->raw.error==ESRCH));
  printf("ACTUAL owned status fault op=%s errno=%d known_start=%" PRIu64 " scan=%u result=%d scans=%u proofs=%u certificates=%u resolutions=%u continuing-root=%u\n",error->raw.operation,error->raw.error,error->raw.start,error->scan_index,result,j.n,r->proofs_n,r->certificates_n,r->resolutions_n,(unsigned)f.tree.root);
  bool pass=result==CENSUS_ROUNDS_SELECTED_PENDING_PROOF && j.n==4 &&
    r->certificates_n==1 && r->proofs_n==2 && r->resolutions_n==1 &&
    r->resolutions[0].proof_index==2 && census_final_resolutions_valid(s,r,&j);
  if(pass) {
    assert(r->proofs[1].source.start==error->raw.start && r->proofs[1].protected_identity);
    r->proofs[1].source.pid++;assert(!census_final_resolutions_valid(s,r,&j));r->proofs[1].source.pid--;
    struct census_global_fault wrong=*error;wrong.raw.start=0;assert(!census_absence_raw_eligible(&wrong));
    wrong=*error;wrong.raw.error=EACCES;assert(!census_absence_raw_eligible(&wrong));
    wrong=*error;wrong.raw.error=ENOENT;assert(!census_absence_raw_eligible(&wrong));
    struct census_history_row *root=census_history_find(&s->history->scans[2],(uint32_t)f.tree.root);
    root->identity.start++;assert(!census_final_resolutions_valid(s,r,&j));root->identity.start--;
    assert(census_final_resolutions_valid(s,r,&j));
    struct census_json *o=census_json_new(&b);assert(o);
    census_json_text(o,"{\"schema\":\"source-proc-census-vector/v1\",\"census\":");
    census_json_census(o,s,r,&j);census_json_text(o,",\"errors\":");census_json_errors(o,s);
    uint64_t start=(uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u;
    census_json_printf(o,",\"errors_total\":%u,\"errors_truncated\":false,\"duration_ms\":%" PRIu64 ",\"controller_pid\":%u,\"certificate_disposition\":\"provisional\"}",s->errors_total,mono_ms()-start,ev.binding.pid);
    assert(census_json_finalize(o));
    if(argc==2) {int fd=open(argv[1],O_WRONLY|O_CREAT|O_EXCL|O_CLOEXEC,0600);assert(fd>=0);size_t n=0;while(n<o->n) {ssize_t k=write(fd,o->data+n,o->n-n);assert(k>0);n+=(size_t)k;}assert(!close(fd));}
    census_json_free(o);
  }
  retire_tree(&f.tree);census_resolution_free(&b,r);census_global_free(s);assert(!b.used && !b.fd_failed);close(ev.procfd);
  printf("%s genuine known-start status-disappearance source certificate / wrong-witness, NULL-start status, EACCES, changed-root DENY; no provider/host/runtime\n",pass?"PASS":"RED");
  return pass?0:1;
}
