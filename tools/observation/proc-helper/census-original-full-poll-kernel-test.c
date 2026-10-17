/* Owned children and controlled enumeration only. Actual production capture
 * obtains stage3 and stage2 independently; no fault/stage relabeling. This is
 * not a host scan, helper RPC, bootstrap/profile or provider-admission test. */
#define GC_HELPER_TEST
#define HELPER_SOURCE_REVISION "c28fd4df2e303f192b23226cc9c7a069375f6341"
#ifndef GC_POLL_FIXTURE_OBSERVER
#define GC_POLL_FIXTURE_OBSERVER "observer.c"
#endif
#ifndef GC_POLL_FIXTURE_READER
#define GC_POLL_FIXTURE_READER "census-v3-reader.h"
#endif
#ifndef GC_POLL_FIXTURE_CONTINUATION
#define GC_POLL_FIXTURE_CONTINUATION "census-continuation.h"
#endif
#define main observer_fixture_main
#include GC_POLL_FIXTURE_OBSERVER
#undef main
#include GC_POLL_FIXTURE_READER
#include GC_POLL_FIXTURE_CONTINUATION
#include "census-json.h"
#include <assert.h>
#include <sys/resource.h>
#include <sys/wait.h>

static pid_t owned[2];
static int gate[2];
static unsigned current_scan;
static bool target_reaped;

static pid_t start_owned(const char *self,unsigned index) {
  int control[2],ready[2];assert(!pipe(control) && !pipe(ready));
  pid_t pid=fork();assert(pid>=0);
  if(!pid) {
    close(control[1]);close(ready[0]);
    for(unsigned k=0;k<index;k++) close(gate[k]);
    char a[32],b[32];snprintf(a,sizeof a,"%d",control[0]);snprintf(b,sizeof b,"%d",ready[1]);
    char *args[]={(char *)self,"owned-child",a,b,NULL};char *env[]={NULL};
    execve(self,args,env);_exit(127);
  }
  close(control[0]);close(ready[1]);char answer;
  assert(read(ready[0],&answer,1)==1 && answer=='r');close(ready[0]);gate[index]=control[1];return pid;
}
static void stop_owned(unsigned index,bool reap_now) {
  assert(write(gate[index],"x",1)==1);close(gate[index]);gate[index]=-1;
  siginfo_t info;assert(!waitid(P_PID,owned[index],&info,WEXITED|WNOWAIT));
  if(reap_now) {int status;assert(waitpid(owned[index],&status,0)==owned[index] && WIFEXITED(status) && !WEXITSTATUS(status));}
}
static bool enumerate_owned(void *unused,struct census_budget *budget,struct census_pid_list *out) {
  (void)unused;uint32_t pids[2];unsigned n=0;pids[n++]=(uint32_t)owned[0];
  if(current_scan>=4 && !target_reaped) pids[n++]=(uint32_t)owned[1];
  if(n==2 && pids[1]<pids[0]) {uint32_t value=pids[0];pids[0]=pids[1];pids[1]=value;}
  for(unsigned k=0;k<n;k++) if(!census_pid_append(budget,out,pids[k])) return false;
  return true;
}
static bool has_shared_stage3(const struct census_resolution_ledger *r,uint32_t pid) {
  for(unsigned k=0;k<r->proofs_n;k++) if(r->proofs[k].source.pid==pid && r->proofs[k].exit_witness.stage==3) {
    unsigned births=0,companions=0;
    for(unsigned j=0;j<r->resolutions_n;j++) if(r->resolutions[j].proof_index==k+1) {
      births+=r->resolutions[j].kind==CENSUS_RESOLUTION_BOUND_BIRTH;
      companions+=r->resolutions[j].kind==CENSUS_RESOLUTION_KNOWN_TERMINAL;
    }
    return births==1 && companions==1;
  }
  return false;
}
int main(int argc,char **argv) {
  if(argc==4 && !strcmp(argv[1],"owned-child")) {
    assert(!prctl(PR_SET_NAME,"bd"));assert(write(atoi(argv[3]),"r",1)==1);close(atoi(argv[3]));
    char command;assert(read(atoi(argv[2]),&command,1)==1);close(atoi(argv[2]));return command=='x'?0:2;
  }
  assert(argc==2 && getuid()>0 && geteuid()==getuid());
  const char *mode=argv[1];bool positive=!strcmp(mode,"positive");
  assert(positive || !strcmp(mode,"no-stage2") || !strcmp(mode,"wrong-uid") ||
    !strcmp(mode,"wrong-start") || !strcmp(mode,"live-companion-fd") || !strcmp(mode,"reaped-original"));
  struct rlimit limit={32,32};assert(!setrlimit(RLIMIT_NOFILE,&limit));
  char self[4096];ssize_t length=readlink("/proc/self/exe",self,sizeof self-1);assert(length>0 && (size_t)length<sizeof self-1);self[length]=0;
  owned[0]=start_owned(self,0);owned[1]=start_owned(self,1);
  memset(&ev,0,sizeof ev);assert(!clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start));
  ev.procfd=open("/proc",O_RDONLY|O_DIRECTORY|O_CLOEXEC);assert(ev.procfd>=0);
  ev.binding.pid=(uint32_t)getpid();ev.trusted_kernel=true;
  length=readlink("/proc/self/ns/pid",ev.ns,sizeof ev.ns-1);assert(length>0);ev.ns[length]=0;strcpy(ev.binding.ns,ev.ns);
  struct census_budget budget={.limit=16*1024*1024};
  struct census_global_source *source=census_global_new(&budget,NULL,enumerate_owned,census_fixed_capture);
  struct census_resolution_ledger *ledger=census_resolution_new(&budget);assert(source && ledger);
  struct census_round_journal journal={0};
  for(current_scan=1;current_scan<=7;current_scan++) {
    if(current_scan==7) stop_owned(1,false); /* real unreaped zombie, no synthetic fault */
    struct census_round_scan receipt;
    (void)census_global_scan_record(source,current_scan,current_scan==1?0:(current_scan+1)/3,
      current_scan==1?CENSUS_INITIAL:((current_scan-1)%3==0?CENSUS_SEAL:CENSUS_CLOSING),&receipt);
    journal.scans[journal.n++]=receipt;
  }
  struct census_history_row *birth=census_history_find(&source->history->scans[3],(uint32_t)owned[1]);
  assert(census_row_verified(birth) && birth->raw_fault.retained_stage==3);
  for(unsigned k=4;k<=5;k++) assert(census_row_verified(census_history_find(&source->history->scans[k],(uint32_t)owned[1])));
  assert(!census_numeric_enumeration_absent(&source->history->scans[6],(uint32_t)owned[1]));
  unsigned poll_slot=source->errors_n;
  for(unsigned k=0;k<source->errors_n;k++) if(source->errors[k].raw.pid==(uint32_t)owned[1] &&
      source->errors[k].raw.operation && !strcmp(source->errors[k].raw.operation,"pidfd_poll")) poll_slot=k;
  assert(poll_slot<source->errors_n);
  struct census_capture_fault *companion=&source->errors[poll_slot].raw;
  assert(source->errors[poll_slot].scan_index==7 && companion->error==ESTALE && !companion->start &&
    companion->retained_stage==2 && companion->retained_start==birth->identity.start &&
    companion->retained_pidfd_plus_one && companion->retained_procdir_plus_one && !companion->capture_diagnostic_available);
  fprintf(stderr,"actual FULL4-6/stage3 + initial real POLLIN7/stage2 + numeric-present7 obtained\n");
  struct census_capture_fault saved=*companion;int borrowed=-1;
  uint32_t raw_pid=companion->pid;unsigned raw_scan=source->errors[poll_slot].scan_index;
  if(!strcmp(mode,"no-stage2")) companion->retained_stage=0;
  if(!strcmp(mode,"wrong-uid")) companion->retained_uids[0]++;
  if(!strcmp(mode,"wrong-start")) companion->retained_start++;
  if(!strcmp(mode,"live-companion-fd")) {
    borrowed=process_pidfd((uint32_t)owned[0]);assert(borrowed>=0 && census_fd_take(&budget,borrowed));
    companion->retained_pidfd_plus_one=(unsigned)borrowed+1;
  }
  if(!strcmp(mode,"reaped-original")) {int status;assert(waitpid(owned[1],&status,0)==owned[1]);target_reaped=true;}
  assert(census_resolve_exit_windows(source,ledger));
  bool admitted=has_shared_stage3(ledger,(uint32_t)owned[1]);
  unsigned separate_stage2=0;
  for(unsigned k=0;k<ledger->proofs_n;k++) if(ledger->proofs[k].source.pid==(uint32_t)owned[1] && ledger->proofs[k].exit_witness.stage==2) separate_stage2++;
  assert(companion->pid==raw_pid && source->errors[poll_slot].scan_index==raw_scan &&
    !strcmp(companion->operation,"pidfd_poll") && companion->error==ESTALE && !companion->start);
  fprintf(stderr,"mode=%s shared_stage3=%d separate_stage2=%u raw_fault_unchanged=TRUE\n",mode,admitted,separate_stage2);
  if(!positive) {
    /* Negative mutation never becomes published evidence. Restore only owned
     * test aliases and close the extra live-child descriptor exactly once. */
    if(borrowed>=0) census_fd_close(&budget,borrowed);
    if(companion->retained_pidfd_plus_one) *companion=saved;
    fprintf(stderr,"mode=%s shared_stage3=%d expected=0\n",mode,admitted);
  }
  if(!target_reaped) {int status;assert(waitpid(owned[1],&status,0)==owned[1]);target_reaped=true;}
  if(positive && admitted) {
    for(current_scan=8;current_scan<=10;current_scan++) {
      struct census_round_scan receipt;
      (void)census_global_scan_record(source,current_scan,3,current_scan==10?CENSUS_SEAL:CENSUS_CLOSING,&receipt);
      journal.scans[journal.n++]=receipt;
    }
    journal.selected_first=8;journal.selected_second=9;journal.selected_seal=10;
    for(unsigned k=0;k<ledger->resolutions_n;k++) ledger->resolutions[k].selected_seal=10;
    assert(census_finish_births(source,ledger,&journal) && census_final_resolutions_valid(source,ledger,&journal));
    struct census_json *json=census_json_new(&budget);assert(json);
    census_json_text(json,"{\"census\":");census_json_census(json,source,ledger,&journal);
    census_json_text(json,",\"errors\":");census_json_errors(json,source);
    uint64_t origin=(uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u;
    census_json_printf(json,",\"errors_total\":%u,\"errors_truncated\":false,\"duration_ms\":%" PRIu64 ",\"controller_pid\":%u}",source->errors_total,mono_ms()-origin,(unsigned)getpid());
    assert(!json->failed && fwrite(json->data,1,json->n,stdout)==json->n);putchar('\n');census_json_free(json);
  }
  stop_owned(0,true);census_resolution_free(&budget,ledger);census_global_free(source);
  assert(!budget.used && !budget.fd_failed);close(ev.procfd);
  return admitted==positive?0:1;
}
