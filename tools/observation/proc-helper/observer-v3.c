/* Separate immutable-candidate V3 entry. Not installed or runtime enabled.
 * The V2 source/entry remains unchanged. Compile/release review must pin this
 * entire new candidate before use; no argument or request selects a proc path. */
#define main gc_legacy_v2_reference_main
#include "observer.c"
#undef main
#include "census-v3-reader.h"
#include "census-envelope.h"
#include "census-bootstrap.h"
#define CENSUS_V3_POLICY \
  "gc-proc-helper/v3;fd=3;fd_count=32;request=1024;wall_ms=10000;pids=65536;" \
  "env=16777216;output=16777216;retained=16777216;errors=128;" \
  "openat2=beneath,no_symlinks,no_magiclinks;" \
  "seccomp=default_errno_x86_64_v3_pidfd_flags0;roots=exact_tmux_v1;" \
  "kernel=pf_kthread_stat_v1;kernel_comm=excluded_from_coverage_v1;" \
  "non_target_env=validated_city_context_no_session_keys_v1;" \
  "session_keys=sid,template,epoch,token_presence_v1;census=bounded-process-census/v3;rounds=3;" \
  "certificates=provisional_descendant_chain_v3"
static struct census_bootstrap census_v3;
int main(int argc,char **argv) {
  (void)argv;
  if(argc!=1 || !hex_string(HELPER_SOURCE_REVISION,40)) return 2;
  ev.complete=true;ev.processfd=ev.active_pidfd=ev.procfd=-1;
  if(clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start) ||
      clock_gettime(CLOCK_REALTIME,&ev.realtime_start)) return 2;
  timestamp(ev.realtime_start,ev.started);
  sha256_sum(CENSUS_V3_POLICY,strlen(CENSUS_V3_POLICY),ev.policy);
  struct sigaction ignore={.sa_handler=SIG_IGN};
  if(sigemptyset(&ignore.sa_mask) || sigaction(SIGPIPE,&ignore,NULL) ||
      !census_bootstrap_init(&census_v3)) return 2;
  struct census_budget *budget=&census_v3.budget;
  if(!census_binding_v3(budget) || !census_self_hash_v3(budget) ||
      !census_text_v3(budget,"sys/kernel/random/boot_id",ev.boot,sizeof ev.boot) ||
      strcmp(ev.boot,ev.binding.boot) || !namespace_identity(ev.ns,sizeof ev.ns) ||
      strcmp(ev.ns,ev.binding.ns) || !census_mount_v3(budget) ||
      !census_peer_v3(&census_v3,true) || !census_receive_request_v3(budget) ||
      prctl(PR_SET_DUMPABLE,0,0,0,0) || !census_kernel_v3(budget)) return 2;
  ev.trusted_kernel=true;
  int flags=fcntl(CONNECTION_FD,F_GETFL);
  if(flags<0 || fcntl(CONNECTION_FD,F_SETFL,flags|O_NONBLOCK) || install_seccomp()) return 2;
  struct census_global_source *source=census_global_new(budget,NULL,census_fixed_enumeration,census_fixed_capture);
  if(!source) return 2;
  struct census_resolution_ledger *proofs=census_resolution_new(budget);if(!proofs) return 2;
  enum census_round_result result=census_choose_proven_continuation(source,proofs,&census_v3.journal);
  ev.complete=result==CENSUS_ROUNDS_SELECTED_PENDING_PROOF;
  if(!census_peer_v3(&census_v3,false)) {
    ev.complete=false;
    census_global_error(source,source->history->n,"caller_binding_changed",
      (struct census_capture_fault){.operation="binding",.pid=ev.binding.pid,.start=ev.binding.start,.error=ESTALE});
  }
  char boot[37];
  if(!census_text_v3(budget,"sys/kernel/random/boot_id",boot,sizeof boot) || strcmp(boot,ev.boot)) {
    ev.complete=false;
    census_global_error(source,source->history->n,"boot_changed",
      (struct census_capture_fault){.operation="identity",.error=ESTALE});
  }
  /* The pinned proc directory and this process's PID namespace cannot be
   * replaced: seccomp denies mount/setns/unshare and all executable mappings.
   * No unreviewed post-sandbox readlink/uname syscall is introduced. */
  struct census_envelope *envelope=census_envelope_new(source,proofs,&census_v3.journal);
  if(!envelope || !census_envelope_finalize(envelope)) return 2;
  struct output wire={.b=envelope->json->data,.n=envelope->json->n};
  bool complete=envelope->complete;
  /* No re-encoding/retry on any limit or write failure; original UNKNOWN is
   * preserved, and transport failure cannot manufacture COMPLETE. */
  if(!send_output(&wire)) return 2;
  census_envelope_free(budget,envelope);census_resolution_free(budget,proofs);
  census_global_free(source);census_identity_clear(budget,&census_v3.caller);
  census_fd_close(budget,ev.procfd);
  return complete?0:1;
}
