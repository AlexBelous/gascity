/* Own-child controlled stat -> environ ESRCH. No host scan or provider RPC. */
#define GC_HELPER_TEST
#define HELPER_SOURCE_REVISION "e8c588fec1f62849b5973830487fa6c0b639147d"
#define main observer_fixture_main
#include "observer.c"
#undef main
#include "census-bounded-read.h"
#include "census-v3-reader.h"
#include "census-continuation.h"
#include "census-json.h"
#include <assert.h>
#include <sys/resource.h>
#include <sys/wait.h>

static pid_t owned_child=-1;
static int gate_write=-1, hook_ok=0,child_environ_hits=0,retire_on_hit=1;
static bool enumerate_owned_pair(void *,struct census_budget *,struct census_pid_list *);

static void retire_before_environ(uint32_t pid) {
  if(pid!=(uint32_t)owned_child || gate_write<0) return;
  if(++child_environ_hits!=retire_on_hit) return;
  int wrote=write(gate_write,"x",1)==1;
  close(gate_write);gate_write=-1;
  int status=0;
  if(waitpid(owned_child,&status,0)==owned_child && wrote &&
      WIFEXITED(status) && WEXITSTATUS(status)==0) hook_ok=1;
  owned_child=-1;
  census_test_before_environ=NULL;
}

static int run_prior_guard(void) {
  int gate[2]={-1,-1},rc=2;
  struct census_budget budget={.limit=16777216};
  struct census_global_source *source=NULL;
  struct census_resolution_ledger *ledger=NULL;
  memset(&ev,0,sizeof ev);ev.processfd=ev.active_pidfd=-1;
  ev.procfd=open("/proc",O_RDONLY|O_DIRECTORY|O_CLOEXEC);
  if(ev.procfd<0 || clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start)) goto done;
  ev.binding.pid=(uint32_t)getpid();ev.trusted_kernel=true;
  ssize_t n=readlink("/proc/self/ns/pid",ev.ns,sizeof ev.ns-1);
  if(n<0) goto done;
  ev.ns[n]=0;strcpy(ev.binding.ns,ev.ns);
  if(pipe(gate)) goto done;
  owned_child=fork();if(owned_child<0) goto done;
  if(!owned_child) {
    close(gate[1]);char command;
    ssize_t got=read(gate[0],&command,1);close(gate[0]);_exit(got==1?0:2);
  }
  close(gate[0]);gate[0]=-1;gate_write=gate[1];gate[1]=-1;
  source=census_global_new(&budget,NULL,enumerate_owned_pair,census_fixed_capture);
  ledger=census_resolution_new(&budget);
  if(!source || !ledger) goto done;
  retire_on_hit=3;census_test_before_environ=retire_before_environ;
  struct census_round_scan receipt={0};
  if(!census_global_scan_record(source,1,0,CENSUS_INITIAL,&receipt)) {
    fprintf(stderr,"prior scan failed errors=%u operation=%s\n",source->errors_n,
      source->errors_n?source->errors[0].raw.operation:"none");goto done;
  }
  struct census_history_row *prior=census_history_find(&source->history->scans[0],(uint32_t)owned_child);
  if(!census_row_verified(prior)) {fputs("prior unverified\n",stderr);goto done;}
  (void)census_global_scan_record(source,2,1,CENSUS_CLOSING,&receipt);
  if(!hook_ok || source->errors_n!=1 || !source->errors[0].raw.bound_exit_valid ||
      source->errors[0].scan_index!=2) {
    fprintf(stderr,"retirement missing hook=%d errors=%u witness=%d scan=%u hits=%d\n",
      hook_ok,source->errors_n,source->errors_n?source->errors[0].raw.bound_exit_valid:0,
      source->errors_n?source->errors[0].scan_index:0,child_environ_hits);goto done;
  }
  if(census_resolve_unclassified_retirement(source,ledger,1) || ledger->proofs_n || ledger->resolutions_n) goto done;
  puts("prior_verified_pid_veto=1 raw_environ_esrch_witness=1");
  rc=0;
done:
  census_resolution_free(&budget,ledger);
  census_global_free(source);
  if(gate[0]>=0) close(gate[0]);
  if(gate[1]>=0) close(gate[1]);
  if(gate_write>=0) close(gate_write);
  if(owned_child>0) {kill(owned_child,SIGKILL);waitpid(owned_child,NULL,0);}
  if(ev.procfd>=0) close(ev.procfd);
  if(budget.used || budget.fd_failed) return 3;
  return rc;
}

static bool enumerate_owned_pair(void *unused,struct census_budget *budget,
    struct census_pid_list *pids) {
  (void)unused;
  if(!census_pid_append(budget,pids,(uint32_t)getpid())) return false;
  return owned_child<0 || census_pid_append(budget,pids,(uint32_t)owned_child);
}

static int run_vector(void) {
  int gate[2]={-1,-1},rc=2;
  struct rlimit fdlimit={32,32};
  if(setrlimit(RLIMIT_NOFILE,&fdlimit)) return 5;
  struct census_budget budget={.limit=16777216};
  struct census_global_source *source=NULL;
  struct census_resolution_ledger *ledger=NULL;
  struct census_json *json=NULL;
  memset(&ev,0,sizeof ev);ev.processfd=ev.active_pidfd=-1;
  ev.procfd=open("/proc",O_RDONLY|O_DIRECTORY|O_CLOEXEC);
  if(ev.procfd<0 || clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start)) goto done;
  ev.binding.pid=(uint32_t)getpid();ev.trusted_kernel=true;
  ssize_t n=readlink("/proc/self/ns/pid",ev.ns,sizeof ev.ns-1);
  if(n<0) goto done;
  ev.ns[n]=0;strcpy(ev.binding.ns,ev.ns);
  if(pipe(gate)) goto done;
  owned_child=fork();if(owned_child<0) goto done;
  if(!owned_child) {
    close(gate[1]);char command;
    ssize_t got=read(gate[0],&command,1);close(gate[0]);_exit(got==1?0:2);
  }
  close(gate[0]);gate[0]=-1;gate_write=gate[1];gate[1]=-1;
  source=census_global_new(&budget,NULL,enumerate_owned_pair,census_fixed_capture);
  ledger=census_resolution_new(&budget);
  if(!source || !ledger) goto done;
  census_test_before_environ=retire_before_environ;
  struct census_round_journal rounds={0};
  enum census_round_result result=census_choose_proven_continuation(source,ledger,&rounds);
  if(result!=CENSUS_ROUNDS_SELECTED_PENDING_PROOF || !hook_ok || source->errors_n!=1 ||
      ledger->proofs_n!=1 || ledger->resolutions_n!=1 || !census_final_resolutions_valid(source,ledger,&rounds)) {
    fprintf(stderr,"vector gate: result=%d hook=%d errors=%u proofs=%u resolutions=%u scans=%u fatal=%d\n",
      result,hook_ok,source->errors_n,ledger->proofs_n,ledger->resolutions_n,
      source->history->n,source->fatal);goto done;
  }
  /* Mutation controls run against the exact accepted kernel-origin ledger.
   * Restore each field before emitting the original positive vector. */
  int saved_errno=source->errors[0].raw.error;
  source->errors[0].raw.error=EACCES;
  assert(!census_final_resolutions_valid(source,ledger,&rounds));
  source->errors[0].raw.error=saved_errno;
  source->errors[0].raw.bound_exit_valid=false;
  assert(!census_final_resolutions_valid(source,ledger,&rounds));
  source->errors[0].raw.bound_exit_valid=true;
  uint64_t saved_offset=ledger->proofs[0].source.offset_ms;
  ledger->proofs[0].source.offset_ms=source->history->scans[0].receipt.offset_ms+1;
  assert(!census_final_resolutions_valid(source,ledger,&rounds));
  ledger->proofs[0].source.offset_ms=saved_offset;
  const char *saved_method=ledger->proofs[0].source.method;
  ledger->proofs[0].source.method="pidfd_no_pid";
  assert(!census_final_resolutions_valid(source,ledger,&rounds));
  ledger->proofs[0].source.method=saved_method;
  ledger->proofs[1]=ledger->proofs[0];ledger->proofs_n=2;
  assert(!census_final_resolutions_valid(source,ledger,&rounds));
  ledger->proofs_n=1;
  uint32_t saved_pid=source->errors[0].raw.pid;
  source->errors[0].raw.pid=ev.binding.pid;
  assert(!census_final_resolutions_valid(source,ledger,&rounds));
  source->errors[0].raw.pid=saved_pid;
  struct census_history_row *later=&source->history->scans[1].rows[0];
  struct census_history_row saved_later=*later;
  later->pid=saved_pid;later->identity.pid=saved_pid;
  later->identity.start=source->errors[0].raw.start;
  assert(!census_final_resolutions_valid(source,ledger,&rounds));
  *later=saved_later;
  assert(census_final_resolutions_valid(source,ledger,&rounds));
  json=census_json_new(&budget);if(!json) goto done;
  census_json_text(json,"{\"scope\":\"fixture_procfs\",\"controller_pid\":");
  census_json_printf(json,"%u,\"errors\":",ev.binding.pid);
  census_json_errors(json,source);
  census_json_text(json,",\"census\":");
  census_json_census(json,source,ledger,&rounds);
  uint64_t origin=(uint64_t)ev.monotonic_start.tv_sec*1000u+
    (uint64_t)ev.monotonic_start.tv_nsec/1000000u;
  uint64_t now=mono_ms();
  if(now<origin || now-origin>=10000) goto done;
  census_json_printf(json,",\"duration_ms\":%" PRIu64 "}",now-origin);
  if(!census_json_finalize(json)) goto done;
  if(fwrite(json->data,1,json->n,stdout)!=json->n || putchar('\n')==EOF) goto done;
  rc=0;
done:
  census_json_free(json);
  census_resolution_free(&budget,ledger);
  census_global_free(source);
  if(gate[0]>=0) close(gate[0]);
  if(gate[1]>=0) close(gate[1]);
  if(gate_write>=0) close(gate_write);
  if(owned_child>0) {kill(owned_child,SIGKILL);waitpid(owned_child,NULL,0);}
  if(ev.procfd>=0) close(ev.procfd);
  if(budget.used || budget.fd_failed) return 3;
  return rc;
}

int main(int argc,char **argv) {
  if(argc==2 && !strcmp(argv[1],"vector")) return run_vector();
  if(argc==2 && !strcmp(argv[1],"prior")) return run_prior_guard();
  bool second_pass=argc==2 && !strcmp(argv[1],"second-pass");
  if(argc!=1 && !second_pass) return 4;
  int gate[2]={-1,-1},rc=1;
  struct census_budget budget={.limit=16777216};
  struct census_owned_identity row={0};
  struct census_capture_fault fault={0};
  memset(&ev,0,sizeof ev);ev.processfd=ev.active_pidfd=-1;
  ev.procfd=open("/proc",O_RDONLY|O_DIRECTORY|O_CLOEXEC);
  if(ev.procfd<0 || clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start)) goto done;
  ev.binding.pid=(uint32_t)getpid();ev.trusted_kernel=true;
  ssize_t n=readlink("/proc/self/ns/pid",ev.ns,sizeof ev.ns-1);
  if(n<0) goto done;
  ev.ns[n]=0;strcpy(ev.binding.ns,ev.ns);
  if(pipe(gate)) goto done;
  owned_child=fork();if(owned_child<0) goto done;
  if(!owned_child) {
    close(gate[1]);char command;
    ssize_t got=read(gate[0],&command,1);close(gate[0]);_exit(got==1?0:2);
  }
  close(gate[0]);gate[0]=-1;gate_write=gate[1];gate[1]=-1;
  retire_on_hit=second_pass?2:1;
  census_test_before_environ=retire_before_environ;
  int captured=census_capture_bounded_evidenced((uint32_t)owned_child,&budget,&row,&fault);
  int raw_match=!captured && hook_ok && fault.operation &&
    !strcmp(fault.operation,"environ") && fault.error==ESRCH && fault.start>0;
  int witness=raw_match && fault.bound_exit_valid && fault.bound_start==fault.start &&
    fault.bound_exit_offset_ms<10000;
  printf("raw_environ_esrch_known_start=%d bound_exit_witness=%d fd_budget_ok=%d\n",
    raw_match,witness,!budget.fd_failed);
  rc=raw_match && hook_ok && !budget.fd_failed && (second_pass?!witness:witness)?0:2;
done:
  census_identity_clear(&budget,&row);
  if(gate[0]>=0) close(gate[0]);
  if(gate[1]>=0) close(gate[1]);
  if(gate_write>=0) close(gate_write);
  if(owned_child>0) {kill(owned_child,SIGKILL);waitpid(owned_child,NULL,0);}
  if(ev.procfd>=0) close(ev.procfd);
  return rc;
}
