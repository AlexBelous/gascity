/* Synthetic source callback tests ownership/failure custody; no host scan. */
#define _GNU_SOURCE
#include "census-scan-history.h"
#include <assert.h>
#include <stdio.h>

struct fixture {uint32_t fail;bool incomplete;char name[32];};
static bool capture(void *context,uint32_t pid,struct census_budget *budget,
                    struct census_owned_identity *out,struct census_capture_fault *fault) {
  struct fixture *f=context;
  if(pid==f->fail) {*fault=(struct census_capture_fault){.pid=pid,.operation="fixture_capture",.error=ESRCH};return false;}
  struct census_owned_identity in={.pid=pid,.ppid=1,.pgid=pid,.start=100+pid,
    .sid="",.city="",.template="",.name=f->name,.no_gc_environment=true,
    .environment_revalidated=true,.stat_revalidated=true,.pidfd_bound=true,
    .uids_revalidated=true};
  in.uids[0]=in.uids[1]=in.uids[2]=in.uids[3]=1000;
  if(f->incomplete) {in.sid="partial-managed";in.no_gc_environment=false;}
  return census_identity_copy(budget,out,&in);
}
int main(void) {
  char empty_digest[65];sha256_sum("",0,empty_digest);
  assert(!strcmp(empty_digest,"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"));
  struct census_budget b={.limit=16777216};
  struct census_history *h=census_history_new(&b);assert(h);
  uint32_t ids[]={20,30};struct fixture f={.name="before"};
  assert(census_history_append(h,&b,ids,2,0,CENSUS_INITIAL,1,&f,capture));
  assert(h->n==1 && h->scans[0].receipt.classified_all && h->scans[0].receipt.classified==2);
  assert(h->scans[0].rows[0].classification==CENSUS_CLASS_NONMANAGED);
  strcpy(f.name,"after");f.fail=30;
  assert(!census_history_append(h,&b,ids,2,1,CENSUS_CLOSING,2,&f,capture));
  assert(h->n==2 && h->scans[1].rows[1].pid==30 &&
    h->scans[1].rows[1].failure==CENSUS_ROW_CAPTURE_FAILED && !h->scans[1].receipt.classified_all);
  assert(h->scans[1].receipt.hard_unresolved==1 && h->scans[1].receipt.classified==1);
  assert(!strcmp(h->scans[0].rows[0].identity.name,"before"));
  assert(!strcmp(h->scans[1].rows[0].identity.name,"after"));
  assert(strcmp(h->scans[0].receipt.live_digest,h->scans[1].receipt.live_digest));
  /* The failed receipt blocks further appends; it cannot be cleared/retried. */
  f.fail=0;assert(!census_history_append(h,&b,ids,2,1,CENSUS_CLOSING,3,&f,capture));
  assert(h->n==2);
  census_history_free(&b,h);assert(b.used==0);
  h=census_history_new(&b);assert(h);f.incomplete=true;
  assert(!census_history_append(h,&b,ids,2,0,CENSUS_INITIAL,1,&f,capture));
  assert(h->scans[0].rows[0].failure==CENSUS_ROW_CLASSIFICATION_FAILED);
  census_history_free(&b,h);assert(!b.used);
  h=census_history_new(&b);assert(h);f.incomplete=false;
  b.limit=b.used+1;
  assert(!census_history_append(h,&b,ids,2,0,CENSUS_INITIAL,1,&f,capture));
  assert(h->n==1 && h->failed && h->scans[0].allocation_failed);
  census_history_free(&b,h);assert(!b.used);b.limit=16777216;
  /* Kernel classification needs positive flags, and is the only start/PGID=0 case. */
  struct census_owned_identity kernel={.pid=2,.kernel_flags=0x00200000,
    .sid="",.city="",.template="",.name="kernel-name",.stat_revalidated=true,
    .pidfd_bound=true,.uids_revalidated=true};
  assert(census_classify_owned(&kernel)==CENSUS_CLASS_KERNEL);
  kernel.kernel_flags=0;kernel.environment_revalidated=true;kernel.no_gc_environment=true;
  assert(census_classify_owned(&kernel)==CENSUS_CLASS_INVALID);
  kernel.kernel_flags=0x00200000;
  assert(census_classify_owned(&kernel)==CENSUS_CLASS_INVALID);
  /* The hash omits only positively classified kernel comm, never UID or a user comm. */
  kernel.environment_revalidated=false;kernel.no_gc_environment=false;
  struct census_history_row row={.pid=2,.classification=CENSUS_CLASS_KERNEL,.identity=kernel};
  sha256_ctx digest;char before[65],after[65];
  sha256_init(&digest);census_history_identity_digest(&digest,&row);sha256_hex(&digest,before);
  row.identity.name="other kernel comm";
  sha256_init(&digest);census_history_identity_digest(&digest,&row);sha256_hex(&digest,after);
  assert(!strcmp(before,after));
  row.identity.uids[1]=1000;
  sha256_init(&digest);census_history_identity_digest(&digest,&row);sha256_hex(&digest,after);
  assert(strcmp(before,after));
  row.identity=kernel;row.classification=CENSUS_CLASS_NONMANAGED;
  sha256_init(&digest);census_history_identity_digest(&digest,&row);sha256_hex(&digest,before);
  row.identity.name="changed user comm";
  sha256_init(&digest);census_history_identity_digest(&digest,&row);sha256_hex(&digest,after);
  assert(strcmp(before,after));
  puts("PASS owned immutable row history: partial captured rows and exact failed PID retained; partial GC/memory/kernel relabel DENY; failed capture never resolved, no COMPLETE");
}
