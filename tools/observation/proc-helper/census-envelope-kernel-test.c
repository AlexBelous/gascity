/* OWNED processes only: real flags=0 ESRCH, full proc fields, RLIMIT32.
 * Enumerated owned PID list/root label are fixture controls, NOT host/provider.
 * No production helper, caps, signals, database or RPC is touched. */
#define CENSUS_KERNEL_MAIN census_previous_kernel_main
#include "census-v3-kernel-test.c"
#include "census-continuation.h"
#include "census-envelope.h"
struct continuation_fixture {struct owned_tree tree;unsigned scans,mode;bool root_lost;};
static bool owned_enumeration(void *context,struct census_budget *b,struct census_pid_list *pids) {
  struct continuation_fixture *f=context;f->scans++;
  if(f->scans==2) retire_leaf(&f->tree);
  if(f->mode==2 && f->scans==3) {retire_tree(&f->tree);f->root_lost=true;}
  uint32_t root=(uint32_t)f->tree.root,leaf=(uint32_t)f->tree.leaf;
  /* Own parent process is positively inspected to derive the proc root; the
   * callback label is discarded by census_root_classify. No host census. */
  assert((uint32_t)getpid()<root && (uint32_t)getpid()<leaf);
  if(f->mode!=5 && !census_pid_append(b,pids,(uint32_t)getpid())) return false;
  if(f->scans<=2 && leaf<root && !census_pid_append(b,pids,leaf)) return false;
  if(!census_pid_append(b,pids,root)) return false;
  return f->scans>2 || leaf<root || census_pid_append(b,pids,leaf);
}
static bool owned_capture_continuation(void *context,uint32_t pid,struct census_budget *b,
    struct census_owned_identity *out,struct census_capture_fault *fault) {
  struct continuation_fixture *f=context;
  if(f->mode==4 && f->scans==2 && pid==(uint32_t)f->tree.leaf) {
    *fault=(struct census_capture_fault){.pid=pid,.operation="pidfd_open",.error=EACCES};return false;
  }
  bool ok=census_capture_bounded_evidenced(pid,b,out,fault);
  if(ok && ((!f->mode && pid==(uint32_t)f->tree.leaf) || f->mode==5)) out->declared_root=true;
  if(ok && f->mode==1 && f->scans>=2 && pid==(uint32_t)f->tree.root) out->start++;
  if(ok && f->mode==3 && f->scans==1 && pid==(uint32_t)f->tree.leaf) out->uids[0]++;
  return ok;
}
static void owned_envelope_fixture(struct census_global_source *s,
    struct census_resolution_ledger *r,struct census_round_journal *j,const char *path) {
  assert(!clock_gettime(CLOCK_REALTIME,&ev.realtime_start));timestamp(ev.realtime_start,ev.started);
  memset(ev.nonce,'1',64);ev.nonce[64]=0;
  memset(ev.binary,'2',64);ev.binary[64]=0;
  memset(ev.policy,'3',64);ev.policy[64]=0;
  memset(ev.binding.binary,'4',64);ev.binding.binary[64]=0;
  strcpy(ev.binding.source,HELPER_SOURCE_REVISION);
  strcpy(ev.boot,"01234567-89ab-cdef-0123-456789abcdef");strcpy(ev.binding.boot,ev.boot);
  strcpy(ev.ns,"pid:[123456]");strcpy(ev.binding.ns,ev.ns);
  strcpy(ev.binding.kernel_release,"6.8.0-138-generic");
  strcpy(ev.binding.proof_profile,"linux6.8-pidfd-flags0-no-esrch-filters/v1");
  ev.binding.uid=(uint32_t)getuid();
  struct census_history_row *caller=census_history_find(&s->history->scans[0],ev.binding.pid);assert(caller);
  ev.binding.start=caller->identity.start;
  size_t before=s->budget->used;
  struct census_envelope *e=census_envelope_new(s,r,j);assert(e);
  assert(e->complete==(j->selected_seal>0));
  assert(census_envelope_finalize(e));
  if(path && *path) {
    int fd=open(path,O_WRONLY|O_CREAT|O_EXCL|O_CLOEXEC,0600);assert(fd>=0);
    size_t n=0;while(n<e->json->n) {ssize_t k=write(fd,e->json->data+n,e->json->n-n);assert(k>0);n+=(size_t)k;}
    assert(!close(fd));
  }
  census_envelope_free(s->budget,e);assert(s->budget->used==before);
  /* Even a valid provisional census cannot bypass a closing caller fence. */
  ev.complete=false;e=census_envelope_new(s,r,j);assert(e && !e->complete);
  assert(census_envelope_finalize(e));census_envelope_free(s->budget,e);assert(s->budget->used==before);
  ev.complete=true;
}
int main(int argc,char **argv) {
  if(argc==4) return census_previous_kernel_main(argc,argv);
  struct utsname kernel;assert(!uname(&kernel));assert(!strncmp(kernel.release,"6.8.",4));
  struct rlimit fdlimit={32,32};assert(!setrlimit(RLIMIT_NOFILE,&fdlimit));
  size_t max_bytes=0;unsigned max_fds=0;
  for(unsigned mode=0;mode<6;mode++) {
    init_evidence();struct continuation_fixture f={.tree=start_tree(argv[0]),.mode=mode};
    struct census_budget b={.limit=16777216};
    struct census_global_source *s=census_global_new(&b,&f,owned_enumeration,owned_capture_continuation);assert(s);
    struct census_resolution_ledger *r=census_resolution_new(&b);assert(r);
    struct census_round_journal j={0};
    enum census_round_result result=census_choose_proven_continuation(s,r,&j);
    assert(s->history->failed && s->fatal); /* NEVER cleared even on positive continuation. */
    if(mode==5) {
      assert(result==CENSUS_ROUNDS_UNKNOWN && j.n==1 && !r->certificates_n);
      struct census_history_row *unproven_root=census_history_find(&s->history->scans[0],(uint32_t)f.tree.root);
      assert(unproven_root && !unproven_root->identity.declared_root &&
        !strcmp(unproven_root->raw_fault.operation,"parent_unavailable"));
      retire_tree(&f.tree);census_resolution_free(&b,r);census_global_free(s);assert(!b.used);
      close(ev.procfd);continue;
    }
    assert(s->history->n>=2 && !s->history->scans[1].receipt.classified_all &&
      s->history->scans[1].receipt.hard_unresolved==1);
    struct census_history_row *failed=census_history_find(&s->history->scans[1],(uint32_t)f.tree.leaf);
    assert(failed && failed->failure==CENSUS_ROW_CAPTURE_FAILED &&
      !strcmp(failed->raw_fault.operation,"pidfd_open") &&
      failed->raw_fault.error==(mode==4?EACCES:ESRCH));
    assert(s->errors[0].scan_index==2 && s->errors[0].raw.pid==(uint32_t)f.tree.leaf &&
      s->errors[0].raw.start==0 && s->errors[0].raw.error==failed->raw_fault.error);
    if(!mode) {
      assert(result==CENSUS_ROUNDS_SELECTED_PENDING_PROOF && j.n==4 &&
        j.selected_first==2 && j.selected_second==3 && j.selected_seal==4);
      assert(r->certificates_n==1 && r->proofs_n==2 && r->resolutions_n==1);
      assert(r->proofs[0].source.start==0 && !r->proofs[0].protected_identity &&
        !r->proofs[0].certificate_id && r->proofs[1].source.start>0 &&
        r->proofs[1].protected_identity && r->proofs[1].certificate_id==1);
      assert(r->certificates[0].prior_scan==1 && r->certificates[0].absence_proof==1 &&
        r->certificates[0].retirement_proof==2 && r->resolutions[0].proof_index==1 &&
        census_certificate_history(s->history,&r->certificates[0]));
      assert(census_history_find(&s->history->scans[0],(uint32_t)getpid())->identity.no_gc_environment);
      assert(census_history_find(&s->history->scans[0],(uint32_t)f.tree.root)->identity.declared_root);
      assert(!census_history_find(&s->history->scans[0],(uint32_t)f.tree.leaf)->identity.declared_root);
      /* This real producer-side resolution never changes the ordinary V2 guard. */
      const struct census_owned_identity *leaf=&r->certificates[0].witness.chain[0];
      struct process protected_leaf={.pid=leaf->pid,.start=leaf->start,.sid=leaf->sid};
      assert(missing_proof(&protected_leaf,NULL,4)==0);
      struct census_history_row *laterroot=census_history_find(&s->history->scans[2],(uint32_t)f.tree.root);
      laterroot->identity.start++;assert(!census_certificate_history(s->history,&r->certificates[0]));
      assert(!census_final_resolutions_valid(s,r,&j));laterroot->identity.start--;
      assert(census_final_resolutions_valid(s,r,&j));
      struct census_json *json=census_json_new(&b);assert(json);
      census_json_text(json,"{\"schema\":\"source-proc-census-vector/v1\",\"census\":");
      census_json_census(json,s,r,&j);census_json_text(json,",\"errors\":");census_json_errors(json,s);
      uint64_t start=(uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u;
      census_json_printf(json,",\"errors_total\":%u,\"errors_truncated\":false,\"duration_ms\":%" PRIu64 ",\"controller_pid\":%u,\"certificate_disposition\":\"provisional\"}",s->errors_total,mono_ms()-start,ev.binding.pid);
      assert(census_json_finalize(json));
      /* Owned-test output argument, never a helper request/path selector. */
      if(argc==2) {
        int vector=open(argv[1],O_CREAT|O_EXCL|O_WRONLY|O_CLOEXEC,0600);assert(vector>=0);
        size_t written=0;
        while(written<json->n) {ssize_t n=write(vector,json->data+written,json->n-written);assert(n>0);written+=(size_t)n;}
        assert(!close(vector));
      }
      census_json_free(json);
      if(argc==2) {char path[1024];int n=snprintf(path,sizeof path,"%s.envelope.json",argv[1]);assert(n>0 && (size_t)n<sizeof path);owned_envelope_fixture(s,r,&j,path);}
      r->proofs[0].source.pid++;assert(!census_final_resolutions_valid(s,r,&j));r->proofs[0].source.pid--;
      r->proofs[1].protected_identity=false;assert(!census_final_resolutions_valid(s,r,&j));r->proofs[1].protected_identity=true;
      r->proofs[1].certificate_id=0;assert(!census_final_resolutions_valid(s,r,&j));r->proofs[1].certificate_id=1;
      assert(census_final_resolutions_valid(s,r,&j));
    } else {
      assert(result==CENSUS_ROUNDS_UNKNOWN && !j.selected_seal);
      if(mode==2) assert(r->certificates_n==1 && j.n==3); /* Actual root exit AFTER original witness. */
      else assert(r->certificates_n==0 && j.n==2);
      if(mode==2) {
        struct census_json *json=census_json_new(&b);assert(json);
        census_json_text(json,"{\"complete\":false,\"certificate_disposition\":\"provisional\",\"census\":");
        census_json_unknown_census(json,s,r);census_json_text(json,",\"errors\":");census_json_errors(json,s);
        census_json_printf(json,",\"errors_total\":%u,\"errors_truncated\":false}",s->errors_total);
        assert(census_json_finalize(json) && s->errors_n==2 && r->proofs_n==2 && r->certificates_n==1 && r->resolutions_n==1);
        if(argc==2) {
          char path[1024];int n=snprintf(path,sizeof path,"%s.unknown.json",argv[1]);assert(n>0 && (size_t)n<sizeof path);
          int vector=open(path,O_CREAT|O_EXCL|O_WRONLY|O_CLOEXEC,0600);assert(vector>=0);
          size_t written=0;
          while(written<json->n) {ssize_t count=write(vector,json->data+written,json->n-written);assert(count>0);written+=(size_t)count;}
          assert(!close(vector));
        }
        census_json_free(json);
        if(argc==2) {char path[1024];int n=snprintf(path,sizeof path,"%s.unknown-envelope.json",argv[1]);assert(n>0 && (size_t)n<sizeof path);owned_envelope_fixture(s,r,&j,path);}
      }
    }
    if(!f.root_lost) retire_tree(&f.tree);
    census_resolution_free(&b,r);census_global_free(s);assert(!b.used && !b.fd_failed && b.fd_peak<=32);
    if(b.peak>max_bytes) max_bytes=b.peak;
    if(b.fd_peak>max_fds) max_fds=b.fd_peak;
    close(ev.procfd);
  }
  printf("PASS owned kernel=%s UID=%u NOFILE32: genuine early-pidfd NULL witness/derived protected known-start certificate; all recorded continuing ancestors; append4 PENDING_PROOF with original failed/fatal latches and rawerror retained; actual later-root exit, changed-root tuple, unknown chain and EACCES DENY; charged_peak=%zu reader_fd_peak=%u; full typed V3 owned-source fixture envelope + original UNKNOWN diagnostics; no host/provider/dispatch/bootstrap acceptance\n",kernel.release,(unsigned)geteuid(),max_bytes,max_fds);
}
