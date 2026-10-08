/* Compiled synthetic enumeration/capture sources. No host census or proof. */
#define GC_HELPER_TEST
#define main observer_fixture_main
#include "observer.c"
#undef main
#include "census-v3-reader.h"
#include "census-continuation.h"
#include <assert.h>
struct fixture {unsigned calls,enum_fail;uint32_t capture_fail;bool birth_root_replaced;};
static bool list(void *ctx,struct census_budget *b,struct census_pid_list *out) {
  struct fixture *f=ctx;f->calls++;
  if(!census_pid_append(b,out,20)) return false;
  if(f->enum_fail==f->calls) {errno=EACCES;return false;}
  return f->calls<4 || census_pid_append(b,out,30);
}
static bool capture(void *ctx,uint32_t pid,struct census_budget *b,
    struct census_owned_identity *out,struct census_capture_fault *fault) {
  struct fixture *f=ctx;
  if(pid==f->capture_fail) {
    *fault=(struct census_capture_fault){.pid=pid,.operation="fixture_capture",.error=EPERM};return false;
  }
  struct census_owned_identity in={.pid=pid,.ppid=1,.pgid=pid,.start=100+pid,
    .sid="",.city="",.template="",.name="fixture",.environment_revalidated=true,
    .no_gc_environment=true,.stat_revalidated=true,.pidfd_bound=true,.uids_revalidated=true};
  if(f->birth_root_replaced && pid==30) {
    in.ppid=20;in.start=f->calls==4?130:131;in.sid="original-birth-root";
    in.city="/fixture";in.template="fixture.worker";in.epoch=1;in.no_gc_environment=false;
    memset(in.token,'a',64);in.token[64]=0;
    in.uids[0]=in.uids[1]=in.uids[2]=in.uids[3]=1000;
  }
  return census_identity_copy(b,out,&in);
}
int main(void) {
  (void)census_capture_owned;(void)census_immediate_descendant_candidate;
  ev.procfd=open("/proc",O_RDONLY|O_DIRECTORY|O_CLOEXEC);assert(ev.procfd>=0);
  assert(!clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start));
  struct census_budget b={.limit=16777216};struct fixture f={0};
  struct census_global_source *s=census_global_new(&b,&f,list,capture);assert(s);
  struct census_round_journal j={0};
  assert(census_choose_fresh_round(&j,s,census_global_scan)==CENSUS_ROUNDS_SELECTED_PENDING_PROOF);
  assert(j.n==7 && j.selected_first==5 && j.selected_second==6 && j.selected_seal==7);
  assert(s->history->n==7 && s->history->scans[0].allocated_rows==1 && s->history->scans[3].allocated_rows==2);
  assert(s->errors_n==1 && s->errors[0].scan_index==4 && s->errors[0].raw.pid==30 &&
    !strcmp(s->errors[0].reason,"uninspected_birth"));
  assert(s->history->scans[3].receipt.late_birth && j.scans[3].late_birth);
  for(unsigned i=0;i<j.n;i++) assert(j.scans[i].offset_ms<10000 &&
    (!i || j.scans[i].offset_ms>=j.scans[i-1].offset_ms));
  census_global_free(s);assert(!b.used && !b.fd_failed && b.fd_peak<=32);
  f=(struct fixture){0};s=census_global_new(&b,&f,list,capture);assert(s);
  struct census_resolution_ledger *resolutions=census_resolution_new(&b);assert(resolutions);
  memset(&j,0,sizeof j);
  assert(census_choose_proven_continuation(s,resolutions,&j)==CENSUS_ROUNDS_SELECTED_PENDING_PROOF);
  assert(j.n==7 && j.selected_first==5 && j.selected_second==6 && j.selected_seal==7 &&
    s->errors_n==1 && s->errors[0].scan_index==4 && resolutions->resolutions_n==1 &&
    resolutions->resolutions[0].kind==CENSUS_RESOLUTION_FRESH &&
    resolutions->resolutions[0].error_index==1 && resolutions->resolutions[0].classified_scan==5 &&
    resolutions->resolutions[0].selected_seal==7 && !resolutions->resolutions[0].proof_index);
  census_resolution_free(&b,resolutions);census_global_free(s);assert(!b.used);
  /* SAME PID new stable incarnation cannot erase the known positive root at
   * original seal4. Source classification derives root from positive parent20. */
  f=(struct fixture){.birth_root_replaced=true};s=census_global_new(&b,&f,list,capture);assert(s);
  resolutions=census_resolution_new(&b);assert(resolutions);memset(&j,0,sizeof j);
  assert(census_choose_proven_continuation(s,resolutions,&j)==CENSUS_ROUNDS_UNKNOWN);
  assert(j.n==7 && s->errors_n==1 && s->errors[0].scan_index==4 && s->errors[0].raw.start==130 &&
    !resolutions->resolutions_n && s->history->scans[3].rows[1].identity.declared_root &&
    s->history->scans[3].rows[1].identity.start==130 && s->history->scans[4].rows[1].identity.start==131);
  census_resolution_free(&b,resolutions);census_global_free(s);assert(!b.used);
  f=(struct fixture){.enum_fail=1};s=census_global_new(&b,&f,list,capture);assert(s);
  memset(&j,0,sizeof j);
  assert(census_choose_fresh_round(&j,s,census_global_scan)==CENSUS_ROUNDS_UNKNOWN);
  assert(s->history->n==1 && s->history->scans[0].rows[0].pid==20 &&
    s->history->scans[0].receipt.classified==0 && s->errors[0].raw.error==EACCES);
  census_global_free(s);assert(!b.used);
  f=(struct fixture){.capture_fail=20};s=census_global_new(&b,&f,list,capture);assert(s);
  memset(&j,0,sizeof j);
  assert(census_choose_fresh_round(&j,s,census_global_scan)==CENSUS_ROUNDS_UNKNOWN);
  assert(s->errors[0].raw.error==EPERM && s->errors[0].raw.pid==20 &&
    !strcmp(s->errors[0].raw.operation,"fixture_capture"));
  assert(s->history->scans[0].rows[0].raw_fault.error==EPERM);
  census_global_free(s);assert(!b.used);
  close(ev.procfd);
  puts("PASS source enum/capture/history/round adapter: real common clock/charged arrays/own FD seed; late4 selects fresh5/6/7 PENDING_PROOF with rawbirth retained; partial enum EACCES/capture EPERM preserve PID/op/errno and remain UNKNOWN; no host census/producer wire");
}
