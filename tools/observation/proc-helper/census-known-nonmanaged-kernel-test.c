/* Own-child controlled stat -> environ ESRCH. No host scan or provider RPC. */
#define GC_HELPER_TEST
#define HELPER_SOURCE_REVISION "59cc7f5dc3e325eef3b584ea3ca3b4ae82b11702"
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
  if(!census_row_verified(prior) || prior->classification!=CENSUS_CLASS_NONMANAGED ||
      prior->identity.declared_root || !prior->identity.start) {
    fputs("prior nonmanaged identity unverified\n",stderr);goto done;
  }
  /* Negative scheduling fixture: model a known-start raw fault while our
   * child is still live. The independent flags0 syscall succeeds; neither
   * that synthetic raw error nor the prior identity may manufacture proof. */
  int live_fd=(int)syscall(SYS_pidfd_open,prior->identity.pid,0);
  if(live_fd<0) goto done;
  close(live_fd);
  source->history->n=2;
  source->history->scans[1].receipt.scan_id=2;
  census_global_error(source,2,"process_unavailable",(struct census_capture_fault){
    .pid=prior->identity.pid,.start=prior->identity.start,.operation="environ",.error=ESRCH});
  const struct census_owned_identity *live_prior=NULL;
  if(!census_standalone_knownstart_valid(source,&source->errors[0],&live_prior) ||
      census_resolve_standalone(source,ledger,1) || ledger->proofs_n || ledger->resolutions_n) goto done;
  source->history->n=1;memset(&source->history->scans[1],0,sizeof source->history->scans[1]);
  source->errors_n=source->errors_total=0;memset(source->errors,0,sizeof source->errors);
  (void)census_global_scan_record(source,2,1,CENSUS_CLOSING,&receipt);
  if(!hook_ok || source->errors_n!=1 || !source->errors[0].raw.bound_exit_valid ||
      source->errors[0].scan_index!=2) {
    fprintf(stderr,"retirement missing hook=%d errors=%u witness=%d scan=%u hits=%d\n",
      hook_ok,source->errors_n,source->errors_n?source->errors[0].raw.bound_exit_valid:0,
      source->errors_n?source->errors[0].scan_index:0,child_environ_hits);goto done;
  }
  /* The child was waitpid-reaped by the owned hook. This is a fresh kernel
   * absence check, not a forged raw errno or an inferred managed identity. */
  errno=0;
  int fresh_fd=(int)syscall(SYS_pidfd_open,source->errors[0].raw.pid,0);
  int fresh_errno=errno;
  if(fresh_fd>=0) {close(fresh_fd);goto done;}
  struct census_probe probe={.pid=source->errors[0].raw.pid,
    .start=prior->identity.start,.syscall_errno=fresh_errno,
    .trusted_kernel=true,.same_namespace=!strcmp(ev.ns,ev.binding.ns)};
  if(fresh_errno!=ESRCH || census_absence(&probe)!=CENSUS_INCARNATION_RETIRED) goto done;
  int descendant=census_resolve_descendant(source,ledger,1);
  int unclassified=census_resolve_unclassified_retirement(source,ledger,1);
  int standalone=census_resolve_standalone(source,ledger,1);
  if(descendant || unclassified || !standalone || ledger->proofs_n!=1 || ledger->resolutions_n!=1) {
    fprintf(stderr,"verified_prior=1 raw_knownstart_environ_esrch=1 genuine_fresh_no_pid=1 descendant=%d unclassified=%d standalone=%d proofs=%u resolutions=%u\n",
      descendant,unclassified,standalone,ledger->proofs_n,ledger->resolutions_n);goto done;
  }
  puts("prior_nonmanaged=1 raw_knownstart_environ_esrch=1 bound_exit=1 fresh_pidfd_esrch=1 standalone_known_nonmanaged=1");
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

static void reject_knownstart_guards(struct census_global_source *s) {
  struct census_resolution_ledger fresh={0};
  const struct census_global_fault *e=&s->errors[0];
  struct census_history_row *prior=census_history_find(&s->history->scans[0],e->raw.pid);
  struct census_history_row saved_prior=*prior;
  bool trusted=ev.trusted_kernel,was_fixture=fixture;
  ev.trusted_kernel=false;assert(!census_resolve_standalone(s,&fresh,1));ev.trusted_kernel=trusted;
  fixture=true;assert(!census_resolve_standalone(s,&fresh,1));fixture=was_fixture;
  char ns[80];strcpy(ns,ev.binding.ns);ev.binding.ns[0]=0;
  assert(!census_resolve_standalone(s,&fresh,1));strcpy(ev.binding.ns,ns);
  unsigned fault_scan=s->errors[0].scan_index;s->errors[0].scan_index=1;
  assert(!census_resolve_standalone(s,&fresh,1));s->errors[0].scan_index=fault_scan;
  int error=s->errors[0].raw.error;
  const int refused[]={EINVAL,EACCES,EPERM,EMFILE};
  for(unsigned i=0;i<sizeof refused/sizeof *refused;i++) {
    s->errors[0].raw.error=refused[i];assert(!census_resolve_standalone(s,&fresh,1));
  }
  s->errors[0].raw.error=error;
  uint64_t raw_start=s->errors[0].raw.start;s->errors[0].raw.start++;
  assert(!census_resolve_standalone(s,&fresh,1));s->errors[0].raw.start=raw_start;
  prior->classification=CENSUS_CLASS_MANAGED;assert(!census_resolve_standalone(s,&fresh,1));*prior=saved_prior;
  prior->identity.declared_root=true;assert(!census_resolve_standalone(s,&fresh,1));*prior=saved_prior;
  prior->identity.uids_revalidated=false;assert(!census_resolve_standalone(s,&fresh,1));*prior=saved_prior;
  prior->identity.name="tmux";assert(!census_resolve_standalone(s,&fresh,1));*prior=saved_prior;
  prior->identity.start++;assert(!census_resolve_standalone(s,&fresh,1));*prior=saved_prior;
  s->truncated=true;assert(!census_resolve_standalone(s,&fresh,1));s->truncated=false;
  s->errors_total++;assert(!census_resolve_standalone(s,&fresh,1));s->errors_total--;
  s->budget->fd_failed=true;assert(!census_resolve_standalone(s,&fresh,1));s->budget->fd_failed=false;
  struct timespec started=ev.monotonic_start;ev.monotonic_start.tv_sec-=11;
  assert(!census_resolve_standalone(s,&fresh,1));ev.monotonic_start=started;
  struct census_history_row *fault=census_history_find(&s->history->scans[1],e->raw.pid);
  struct census_history_row saved_fault=*fault;
  *fault=saved_prior;assert(!census_resolve_standalone(s,&fresh,1));
  fault->identity.start++;assert(!census_resolve_standalone(s,&fresh,1));*fault=saved_fault;
  fresh.resolutions_n=1;fresh.resolutions[0].error_index=1;
  assert(!census_resolve_standalone(s,&fresh,1));fresh.resolutions_n=0;
  assert(!fresh.proofs_n && !fresh.resolutions_n && !fresh.denied);
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
  retire_on_hit=3;census_test_before_environ=retire_before_environ;
  struct census_round_journal rounds={0};
  enum census_round_result result=census_choose_proven_continuation(source,ledger,&rounds);
  if(result!=CENSUS_ROUNDS_SELECTED_PENDING_PROOF || !hook_ok || source->errors_n!=1 ||
      ledger->proofs_n!=1 || ledger->resolutions_n!=1 || !census_final_resolutions_valid(source,ledger,&rounds)) {
    fprintf(stderr,"vector gate: result=%d hook=%d errors=%u proofs=%u resolutions=%u scans=%u fatal=%d\n",
      result,hook_ok,source->errors_n,ledger->proofs_n,ledger->resolutions_n,
      source->history->n,source->fatal);goto done;
  }
  reject_knownstart_guards(source);
  /* Mutation controls run against the exact accepted kernel-origin ledger.
   * Restore each field before emitting the original positive vector. */
  int saved_errno=source->errors[0].raw.error;
  source->errors[0].raw.error=EACCES;
  assert(!census_final_resolutions_valid(source,ledger,&rounds));
  source->errors[0].raw.error=saved_errno;
  /* The direct proof is the independent fresh syscall, not the unknown-owner
   * bound-exit path. Losing its private capture attestation changes no wire. */
  source->errors[0].raw.bound_exit_valid=false;
  assert(census_final_resolutions_valid(source,ledger,&rounds));
  source->errors[0].raw.bound_exit_valid=true;
  uint64_t saved_offset=ledger->proofs[0].source.offset_ms;
  ledger->proofs[0].source.offset_ms=10000;
  assert(!census_final_resolutions_valid(source,ledger,&rounds));
  ledger->proofs[0].source.offset_ms=saved_offset;
  unsigned duplicate=ledger->resolutions_n;
  ledger->resolutions[duplicate]=ledger->resolutions[0];ledger->resolutions_n++;
  assert(!census_standalone_history_valid(source,ledger));ledger->resolutions_n=duplicate;
  uint64_t fault_clock=source->history->scans[1].receipt.offset_ms;
  source->history->scans[1].receipt.offset_ms=saved_offset+1;
  ledger->proofs[0].source.offset_ms=saved_offset;
  assert(!census_standalone_history_valid(source,ledger));
  source->history->scans[1].receipt.offset_ms=fault_clock;

  const char *saved_method=ledger->proofs[0].source.method;
  ledger->proofs[0].source.method="bound_pidfd_exited";
  assert(!census_final_resolutions_valid(source,ledger,&rounds));
  ledger->proofs[0].source.method=saved_method;
  ledger->proofs[1]=ledger->proofs[0];ledger->proofs_n=2;
  assert(!census_final_resolutions_valid(source,ledger,&rounds));
  ledger->proofs_n=1;
  uint32_t saved_pid=source->errors[0].raw.pid;
  source->errors[0].raw.pid=ev.binding.pid;
  assert(!census_final_resolutions_valid(source,ledger,&rounds));
  source->errors[0].raw.pid=saved_pid;
  struct census_history_row *later=census_history_find(&source->history->scans[1],saved_pid);
  struct census_history_row saved_later=*later;
  *later=*census_history_find(&source->history->scans[0],saved_pid);
  assert(!census_standalone_history_valid(source,ledger));
  later->identity.start++;
  assert(!census_standalone_history_valid(source,ledger));
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
