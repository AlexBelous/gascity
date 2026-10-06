/* Owned child only: exact Linux pidfd/Z source, no host census or provider. */
#ifndef HELPER_SOURCE_REVISION
#define HELPER_SOURCE_REVISION "ff760ba6f3311f605863296c146e43b1e7a86f08"
#endif
#define GC_HELPER_TEST
#define main observer_fixture_main
#include "observer.c"
#undef main
#include "census-v3-reader.h"
#include "census-continuation.h"
#include "census-json.h"
#include <sys/wait.h>

struct zombie_test_owned { pid_t child; };
static bool zombie_test_enumerate(void *ctx,struct census_budget *b,struct census_pid_list *p) {
  const struct zombie_test_owned *o=ctx;
  if((uint32_t)getpid()>=(uint32_t)o->child) return false;
  return census_pid_append(b,p,(uint32_t)getpid()) && census_pid_append(b,p,(uint32_t)o->child);
}
int main(int argc,char **argv) {
  int gate[2]={-1,-1},status=0,rc=2;pid_t child=-1;
  struct census_budget budget={.limit=16777216};
  struct census_global_source *source=NULL;struct census_resolution_ledger *ledger=NULL;
  struct census_round_journal journal={0};
  memset(&ev,0,sizeof ev);ev.procfd=-1;ev.processfd=-1;ev.active_pidfd=-1;
  if(pipe(gate)) goto cleanup;
  child=fork();if(child<0) goto cleanup;
  if(child==0) {
    close(gate[1]);char c;
    while(read(gate[0],&c,1)<0 && errno==EINTR) {}
    close(gate[0]);_exit(0);
  }
  close(gate[0]);gate[0]=-1;
  ev.procfd=open("/proc",O_RDONLY|O_DIRECTORY|O_CLOEXEC);
  ev.binding.pid=(uint32_t)getpid();ev.trusted_kernel=true;
  ssize_t n=readlink("/proc/self/ns/pid",ev.ns,sizeof ev.ns-1);
  if(ev.procfd<0 || n<=0 || (size_t)n>=sizeof ev.ns-1 || clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start)) goto cleanup;
  ev.ns[n]=0;strcpy(ev.binding.ns,ev.ns);
  struct census_owned_identity live={0};struct census_capture_fault live_fault={0};
  bool live_ok=census_capture_bounded_evidenced((uint32_t)child,&budget,&live,&live_fault);
  printf("live ok=%d start=%llu operation=%s\n",live_ok,(unsigned long long)live.start,
    live_fault.operation?live_fault.operation:"none");
  census_identity_clear(&budget,&live);
  if(!live_ok) goto cleanup;
  uint64_t live_terminal_start=0;
  if(census_terminal_zombie((uint32_t)child,&budget,&live_terminal_start) || live_terminal_start) goto cleanup;
  struct zombie_test_owned owned={.child=child};
  source=census_global_new(&budget,&owned,zombie_test_enumerate,census_fixed_capture);
  if(!source) goto cleanup;
  struct census_round_scan first={0};
  if(!census_global_scan_record(source,1,0,CENSUS_INITIAL,&first)) goto cleanup;
  census_global_error(source,1,"process_unavailable",
    (struct census_capture_fault){.pid=(uint32_t)child,.operation="pidfd_poll",.error=ESTALE});
  ledger=census_resolution_new(&budget);if(!ledger) goto cleanup;
  if(census_resolve_standalone(source,ledger,1) || ledger->proofs_n || ledger->resolutions_n) goto cleanup;
  struct census_history_row *prior=census_history_find(&source->history->scans[0],(uint32_t)child);
  const struct census_owned_identity *known=NULL;
  if(!prior || !census_row_verified(prior)) goto cleanup;
  enum census_row_class saved=prior->classification;
  prior->classification=CENSUS_CLASS_MANAGED;
  bool protected_denied=!census_standalone_prior(source->history,(uint32_t)child,1,&known);
  prior->classification=saved;
  if(!protected_denied) goto cleanup;
  printf("negative live forged_poll=denied protected_prior=denied\n");
  census_resolution_free(&budget,ledger);ledger=NULL;
  census_global_free(source);source=NULL;
  close(gate[1]);gate[1]=-1;
  bool zombie=false;
  for(int i=0;i<100;i++) {
    siginfo_t si={0};if(waitid(P_PID,child,&si,WEXITED|WNOWAIT|WNOHANG)) goto cleanup;
    if(si.si_pid==child) {zombie=true;break;}usleep(10000);
  }
  if(!zombie) goto cleanup;
  uint64_t terminal_start=0;
  bool terminal=census_terminal_zombie((uint32_t)child,&budget,&terminal_start);
  printf("terminal_probe=%d start=%llu errno=%d fd_failed=%d\n",terminal,
    (unsigned long long)terminal_start,errno,budget.fd_failed);
  source=census_global_new(&budget,&owned,zombie_test_enumerate,census_fixed_capture);
  if(!source) goto cleanup;
  ledger=census_resolution_new(&budget);if(!ledger) goto cleanup;
  enum census_round_result result=census_choose_proven_continuation(source,ledger,&journal);
  printf("result=%d scans=%u errors=%u proofs=%u resolutions=%u selected=%u/%u/%u fdpeak=%u\n",
    result,journal.n,source->errors_n,ledger->proofs_n,ledger->resolutions_n,
    journal.selected_first,journal.selected_second,journal.selected_seal,budget.fd_peak);
  for(unsigned i=0;i<ledger->proofs_n;i++) {
    const struct proof_item *p=&ledger->proofs[i].source;
    printf("proof[%u]=%s method=%s start=%llu scan=%d\n",i+1,
      census_proof_kind_name(p->kind),p->method,(unsigned long long)p->start,p->scan_index);
  }
  bool selected=result==CENSUS_ROUNDS_SELECTED_PENDING_PROOF && journal.n==4 &&
      source->errors_n==4 && ledger->proofs_n==4 && ledger->resolutions_n==4 &&
      census_final_resolutions_valid(source,ledger,&journal);
  if(selected) {
    int original=source->errors[0].raw.error;
    source->errors[0].raw.error=EIO;
    bool wrong_poll_denied=!census_final_resolutions_valid(source,ledger,&journal);
    source->errors[0].raw.error=original;
    ledger->proofs[0].protected_identity=true;
    bool protected_proof_denied=!census_final_resolutions_valid(source,ledger,&journal);
    ledger->proofs[0].protected_identity=false;
    selected=wrong_poll_denied && protected_proof_denied && census_final_resolutions_valid(source,ledger,&journal);
    printf("negative wrong_poll=%s protected_proof=%s\n",
      wrong_poll_denied?"denied":"ACCEPTED",protected_proof_denied?"denied":"ACCEPTED");
  }
  rc=selected?0:1;
  if(rc==0 && argc==2) {
    struct census_json *json=census_json_new(&budget);
    if(!json) {rc=2;goto cleanup;}
    census_json_census(json,source,ledger,&journal);
    int fd=json->failed?-1:open(argv[1],O_WRONLY|O_CREAT|O_EXCL|O_CLOEXEC,0600);
    if(fd<0) {census_json_free(json);rc=2;goto cleanup;}
    dprintf(fd,"{\"scope\":\"owned_kernel_test\",\"source_revision\":\"%s\",\"controller_pid\":%u,\"duration_ms\":%llu,\"census\":",
      HELPER_SOURCE_REVISION,ev.binding.pid,
      (unsigned long long)(mono_ms()-((uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u)));
    if(write(fd,json->data,json->n)!=(ssize_t)json->n) rc=2;
    dprintf(fd,",\"errors\":[");
    for(unsigned i=0;i<source->errors_n;i++) {
      const struct census_global_fault *e=&source->errors[i];
      dprintf(fd,"%s{\"reason\":\"%s\",\"pid\":%u,\"operation\":\"%s\",\"errno\":%d,\"start_ticks\":null,\"scan_index\":%u,\"resolved_by\":0}",
        i?",":"",e->reason,e->raw.pid,e->raw.operation,e->raw.error,e->scan_index);
    }
    dprintf(fd,"]}\n");
    if(close(fd)) rc=2;
    census_json_free(json);
  }
cleanup:
  if(gate[0]>=0) close(gate[0]);
  if(gate[1]>=0) close(gate[1]);
  if(ledger) census_resolution_free(&budget,ledger);
  if(source) census_global_free(source);
  if(ev.procfd>=0) close(ev.procfd);
  if(child>0) {
    pid_t reaped;do {reaped=waitpid(child,&status,0);} while(reaped<0 && errno==EINTR);
    fprintf(stderr,"cleanup reaped=%d own_child=%d\n",(int)reaped,(int)child);
    if(reaped!=child) rc=3;
  }
  return rc;
}
