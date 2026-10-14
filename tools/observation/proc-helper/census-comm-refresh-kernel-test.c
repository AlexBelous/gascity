/* A mutable comm change between captures must trigger fresh fields, not a
 * retirement proof. Only our pipe-controlled child and real procfs are read. */
#define GC_HELPER_TEST
#define HELPER_SOURCE_REVISION "243390d18c175f3f0a293ad3b182ec777533a2f1"
#define main observer_fixture_main
#include "observer.c"
#undef main
#include "census-bounded-read.h"
#include "census-v3-reader.h"
#include "census-continuation.h"
#include "census-json.h"
#include <assert.h>
#include <sys/wait.h>

static pid_t child,root_child;
static int command_fd,reply_fd,hits,rename_on_hit;
static unsigned scan_calls;
static bool enumerate_birth(void *unused,struct census_budget *budget,struct census_pid_list *pids) {
  (void)unused;
  if(!census_pid_append(budget,pids,(uint32_t)getpid())) return false;
  if(!census_pid_append(budget,pids,(uint32_t)root_child)) return false;
  return ++scan_calls<4 || census_pid_append(budget,pids,(uint32_t)child);
}
static void rename_child(void) {
  assert(write(command_fd,"n",1)==1);
  char ack;assert(read(reply_fd,&ack,1)==1 && ack=='r');
}
static void rename_during_capture(uint32_t pid) {
  if(pid==(uint32_t)child && ++hits==rename_on_hit) rename_child();
}
static int deny_environment(uint32_t pid) {return pid==(uint32_t)child?EACCES:0;}

int main(int argc,char **argv) {
  if(argc==4 && !strcmp(argv[1],"child")) {
    int in=atoi(argv[2]),out=atoi(argv[3]);char cmd;unsigned n=0;
    int gate[2];assert(!pipe(gate));pid_t leaf=fork();assert(leaf>=0);
    if(leaf) {
      close(gate[0]);assert(!prctl(PR_SET_NAME,"comm-root",0,0,0));
      assert(write(out,&leaf,sizeof leaf)==sizeof leaf);
      assert(write(gate[1],"r",1)==1);close(gate[1]);close(in);close(out);
      int status;assert(waitpid(leaf,&status,0)==leaf && WIFEXITED(status) && !WEXITSTATUS(status));return 0;
    }
    close(gate[1]);assert(read(gate[0],&cmd,1)==1);close(gate[0]);
    assert(!prctl(PR_SET_NAME,"comm-before",0,0,0));
    assert(write(out,"r",1)==1);
    while(read(in,&cmd,1)==1 && cmd=='n') {
      assert(!prctl(PR_SET_NAME,++n==1?"comm-after":"comm-again",0,0,0));
      assert(write(out,"r",1)==1);
    }
    close(in);close(out);return 0;
  }
  int commands[2],replies[2];assert(!pipe(commands) && !pipe(replies));
  child=fork();assert(child>=0);
  if(!child) {
    close(commands[1]);close(replies[0]);char a[24],b[24];
    snprintf(a,sizeof a,"%d",commands[0]);snprintf(b,sizeof b,"%d",replies[1]);
    char *args[]={argv[0],"child",a,b,NULL};
    char *env[]={"GC_SESSION_ID=comm-fixture","GC_CITY_PATH=/fixture","GC_TEMPLATE=fixture.worker",
      "GC_RUNTIME_EPOCH=1","GC_INSTANCE_TOKEN=fixture-token",NULL};
    execve(argv[0],args,env);_exit(2);
  }
  close(commands[0]);close(replies[1]);command_fd=commands[1];reply_fd=replies[0];
  root_child=child;assert(read(reply_fd,&child,sizeof child)==sizeof child);
  char ack;assert(read(reply_fd,&ack,1)==1 && ack=='r');
  memset(&ev,0,sizeof ev);ev.processfd=ev.active_pidfd=-1;
  ev.procfd=open("/proc",O_RDONLY|O_DIRECTORY|O_CLOEXEC);assert(ev.procfd>=0);
  assert(!clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start));
  ev.binding.pid=(uint32_t)getpid();ev.trusted_kernel=true;
  ssize_t ns_n=readlink("/proc/self/ns/pid",ev.ns,sizeof ev.ns-1);assert(ns_n>0);
  ev.ns[ns_n]=0;strcpy(ev.binding.ns,ev.ns);
  struct census_budget budget={.limit=16777216};
  if(argc==2 && (!strcmp(argv[1],"continuation") || !strcmp(argv[1],"birth-change") ||
      !strcmp(argv[1],"birth-change-vector"))) {
    bool change=strcmp(argv[1],"continuation")!=0;
    struct census_global_source *source=census_global_new(&budget,NULL,enumerate_birth,census_fixed_capture);
    struct census_resolution_ledger *ledger=census_resolution_new(&budget);
    assert(source && ledger);hits=0;rename_on_hit=3;
    census_test_before_environ=change?rename_during_capture:NULL;
    struct census_round_journal rounds={0};
    enum census_round_result result=census_choose_proven_continuation(source,ledger,&rounds);
    census_test_before_environ=NULL;
    bool original_birth_only=source->history->n==7 &&
      source->errors_n==1 && !strcmp(source->errors[0].reason,"uninspected_birth");
    bool accepted=result==CENSUS_ROUNDS_SELECTED_PENDING_PROOF && original_birth_only &&
      source->errors_n==1 && !strcmp(source->errors[0].reason,"uninspected_birth") &&
      census_final_resolutions_valid(source,ledger,&rounds);
    fprintf(stderr,"continuation result=%d scans=%u faults=%u accepted=%d\n",
      result,source->history->n,source->errors_n,accepted);
    if(accepted && change) {
      struct census_history_row *original=census_history_find(&source->history->scans[3],(uint32_t)child);
      assert(original && !strcmp(original->identity.name,"comm-before"));
      original->identity.uids[0]++;
      assert(!census_final_resolutions_valid(source,ledger,&rounds));original->identity.uids[0]--;
      original->identity.declared_root=true;
      assert(!census_final_resolutions_valid(source,ledger,&rounds));original->identity.declared_root=false;
      struct census_history_row *root=census_history_find(&source->history->scans[3],(uint32_t)root_child);
      assert(root && root->identity.declared_root);root->identity.start++;
      assert(!census_final_resolutions_valid(source,ledger,&rounds));root->identity.start--;
      assert(census_final_resolutions_valid(source,ledger,&rounds));
    }
    if(accepted && !strcmp(argv[1],"birth-change-vector")) {
      struct census_json *json=census_json_new(&budget);assert(json);
      census_json_printf(json,"{\"controller_pid\":%u,\"errors\":",ev.binding.pid);
      census_json_errors(json,source);census_json_text(json,",\"census\":");
      census_json_census(json,source,ledger,&rounds);
      uint64_t origin=(uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u;
      census_json_printf(json,",\"duration_ms\":%" PRIu64 "}",mono_ms()-origin);
      assert(census_json_finalize(json));assert(fwrite(json->data,1,json->n,stdout)==json->n);putchar('\n');
      census_json_free(json);
    }
    census_resolution_free(&budget,ledger);census_global_free(source);
    close(command_fd);close(reply_fd);int status;
    assert(waitpid(root_child,&status,0)==root_child && WIFEXITED(status) && !WEXITSTATUS(status));
    close(ev.procfd);assert(!budget.used && !budget.fd_failed);
    return accepted?0:1;
  }
  struct census_owned_identity prior={0},fresh={0},denied={0};
  struct census_capture_fault fault={0};
  assert(census_capture_bounded_evidenced((uint32_t)child,&budget,&prior,&fault));
  assert(!strcmp(prior.name,"comm-before") && !prior.no_gc_environment);
  rename_child();
  bool ok=census_capture_bounded_custodied_prior((uint32_t)child,&budget,&fresh,&fault,
    prior.start,prior.uids,false,&prior);
  /* Reap our child before reporting a regression, including the old-source RED. */
  if(!ok) {
    fprintf(stderr,"mutable comm rejected: operation=%s errno=%d\n",fault.operation,fault.error);
    close(command_fd);close(reply_fd);int status;waitpid(root_child,&status,0);
    census_identity_clear(&budget,&prior);close(ev.procfd);return 1;
  }
  assert(fresh.pid==prior.pid && fresh.start==prior.start && !strcmp(fresh.name,"comm-after"));
  assert(!strcmp(fresh.sid,prior.sid) && fresh.epoch==prior.epoch && !strcmp(fresh.token,prior.token));
  assert(fresh.stat_revalidated && fresh.pidfd_bound && fresh.uids_revalidated && fresh.environment_revalidated);
  /* A name change during the two full reads is still inconsistent and denied. */
  hits=0;rename_on_hit=2;census_test_before_environ=rename_during_capture;
  assert(!census_capture_bounded_custodied_prior((uint32_t)child,&budget,&denied,&fault,
    fresh.start,fresh.uids,false,&fresh));
  assert(fault.error==ESTALE && !strcmp(fault.operation,"identity_comparison"));
  census_test_before_environ=NULL;
  /* A changed comm never grants retained-comm authority to a failed env read. */
  census_test_environ_error=deny_environment;
  assert(!census_capture_bounded_custodied_prior((uint32_t)child,&budget,&denied,&fault,
    fresh.start,fresh.uids,false,&fresh));
  assert(fault.error==EACCES && !strcmp(fault.operation,"environ") && !fault.retained_comm_verified);
  census_test_environ_error=NULL;
  close(command_fd);close(reply_fd);int status;
  assert(waitpid(root_child,&status,0)==root_child && WIFEXITED(status) && !WEXITSTATUS(status));
  census_identity_clear(&budget,&prior);census_identity_clear(&budget,&fresh);
  census_identity_clear(&budget,&denied);close(ev.procfd);
  assert(!budget.used && !budget.fd_failed);
  puts("PASS mutable comm refreshes full identity; intra-capture change and env failure remain denied");
  return 0;
}
