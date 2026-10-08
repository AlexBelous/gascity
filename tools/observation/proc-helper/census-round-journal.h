/* Bounded V3 internal-round journal. Not a COMPLETE proof/producer by itself.
 * Callback is source-owned, never selected by a helper request/caller. */
#ifndef GC_CENSUS_ROUND_JOURNAL_H
#define GC_CENSUS_ROUND_JOURNAL_H
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <string.h>

enum census_scan_kind {CENSUS_INITIAL,CENSUS_CLOSING,CENSUS_SEAL};
enum census_round_result {CENSUS_ROUNDS_UNKNOWN,CENSUS_ROUNDS_SELECTED_PENDING_PROOF};
struct census_round_scan {
  unsigned scan_id,round;
  enum census_scan_kind kind;
  uint64_t offset_ms;
  size_t enumerated,classified;
  char raw_digest[65],live_digest[65];
  bool classified_all,seal_revalidated,late_birth;
  unsigned hard_unresolved;
};
struct census_round_journal {
  struct census_round_scan scans[10];
  unsigned n,selected_first,selected_second,selected_seal;
};
typedef bool (*census_source_scan)(void *,unsigned,unsigned,enum census_scan_kind,
                                  struct census_round_scan *);
static inline bool census_journal_digest(const char digest[65]) {
  for(size_t i=0;i<64;i++)
    if(!((digest[i]>='0' && digest[i]<='9') || (digest[i]>='a' && digest[i]<='f'))) return false;
  return digest[64]==0;
}
static inline bool census_journal_capture(struct census_round_journal *j,void *context,
    census_source_scan source,unsigned round,enum census_scan_kind kind) {
  if(j->n>=10) return false;
  struct census_round_scan next={.scan_id=j->n+1,.round=round,.kind=kind};
  bool returned=source(context,next.scan_id,round,kind,&next);
  // Preserve even failed source receipts; never clear or recycle a slot.
  j->scans[j->n++]=next;
  if(!returned || next.scan_id!=j->n || next.round!=round || next.kind!=kind ||
      next.offset_ms>=10000 || (j->n>1 && next.offset_ms<j->scans[j->n-2].offset_ms) ||
      !census_journal_digest(next.raw_digest) || !census_journal_digest(next.live_digest) ||
      !next.classified_all || next.hard_unresolved || !next.enumerated ||
      next.enumerated>65536 || !next.classified || next.classified>next.enumerated) return false;
  return true;
}
static inline enum census_round_result census_choose_fresh_round(struct census_round_journal *j,
    void *context,census_source_scan source) {
  if(j->n || !census_journal_capture(j,context,source,0,CENSUS_INITIAL)) return CENSUS_ROUNDS_UNKNOWN;
  for(unsigned round=1;round<=3;round++) {
    unsigned first=j->n;
    if(!census_journal_capture(j,context,source,round,CENSUS_CLOSING) ||
       !census_journal_capture(j,context,source,round,CENSUS_CLOSING) ||
       !census_journal_capture(j,context,source,round,CENSUS_SEAL)) return CENSUS_ROUNDS_UNKNOWN;
    const struct census_round_scan *a=&j->scans[first],*b=&j->scans[first+1],*seal=&j->scans[first+2];
    if(seal->late_birth || !seal->seal_revalidated || a->classified!=b->classified ||
       b->classified!=seal->classified || strcmp(a->live_digest,b->live_digest) ||
       strcmp(b->live_digest,seal->live_digest)) continue;
    j->selected_first=a->scan_id;j->selected_second=b->scan_id;j->selected_seal=seal->scan_id;
    // Selection is NOT completeness. Kernel reconciliation, immutable raw
    // resolutions/certificates, caller/root/provider fences still must pass.
    return CENSUS_ROUNDS_SELECTED_PENDING_PROOF;
  }
  return CENSUS_ROUNDS_UNKNOWN;
}
#endif
