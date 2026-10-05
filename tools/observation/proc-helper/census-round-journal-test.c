#include <stddef.h>
#include "census-round-journal.h"
#include <assert.h>
#include <stdio.h>
struct scenario {unsigned fail_scan,birth_through,deadline_scan,hard_scan,changed_scan,bad_digest_scan,bad_id_scan;};
static bool source(void *ctx,unsigned scan,unsigned round,enum census_scan_kind kind,
                   struct census_round_scan *out) {
  struct scenario *s=ctx;
  *out=(struct census_round_scan){.scan_id=scan,.round=round,.kind=kind,.offset_ms=scan,
    .enumerated=2,.classified=2,.classified_all=true,.seal_revalidated=true};
  memset(out->raw_digest,'a',64);memset(out->live_digest,'b',64);
  if(kind==CENSUS_SEAL && round<=s->birth_through) out->late_birth=true;
  if(scan==s->deadline_scan) out->offset_ms=10000;
  if(scan==s->hard_scan) out->hard_unresolved=1;
  if(scan==s->changed_scan) out->live_digest[0]='c';
  if(scan==s->bad_digest_scan) out->raw_digest[0]='z';
  if(scan==s->bad_id_scan) out->scan_id=1;
  return scan!=s->fail_scan;
}
int main(void) {
  struct census_round_journal j={0};struct scenario s={.birth_through=1};
  assert(census_choose_fresh_round(&j,&s,source)==CENSUS_ROUNDS_SELECTED_PENDING_PROOF);
  assert(j.n==7 && j.selected_first==5 && j.selected_second==6 && j.selected_seal==7);
  assert(j.scans[3].late_birth && j.scans[3].scan_id==4); // no rewriting first failed seal
  memset(&j,0,sizeof j);s.birth_through=3;
  assert(census_choose_fresh_round(&j,&s,source)==CENSUS_ROUNDS_UNKNOWN && j.n==10);
  memset(&j,0,sizeof j);s=(struct scenario){.hard_scan=3};
  assert(census_choose_fresh_round(&j,&s,source)==CENSUS_ROUNDS_UNKNOWN && j.n==3 && j.scans[2].hard_unresolved==1);
  memset(&j,0,sizeof j);s=(struct scenario){.deadline_scan=4};
  assert(census_choose_fresh_round(&j,&s,source)==CENSUS_ROUNDS_UNKNOWN && j.n==4);
  memset(&j,0,sizeof j);s=(struct scenario){.fail_scan=2};
  assert(census_choose_fresh_round(&j,&s,source)==CENSUS_ROUNDS_UNKNOWN && j.n==2);
  memset(&j,0,sizeof j);s=(struct scenario){.changed_scan=3};
  assert(census_choose_fresh_round(&j,&s,source)==CENSUS_ROUNDS_SELECTED_PENDING_PROOF);
  assert(j.n==7 && j.selected_first==5 && j.scans[2].live_digest[0]=='c');
  unsigned saved=j.n;
  assert(census_choose_fresh_round(&j,&s,source)==CENSUS_ROUNDS_UNKNOWN && j.n==saved);
  memset(&j,0,sizeof j);s=(struct scenario){.bad_digest_scan=2};
  assert(census_choose_fresh_round(&j,&s,source)==CENSUS_ROUNDS_UNKNOWN && j.n==2);
  memset(&j,0,sizeof j);s=(struct scenario){.bad_id_scan=3};
  assert(census_choose_fresh_round(&j,&s,source)==CENSUS_ROUNDS_UNKNOWN && j.n==3);
  puts("PASS source-owned bounded round journal: fresh5/6/7 after late4; continuous births/hard failure/deadline DENY; selection still PENDING_PROOF, no COMPLETE");
}
