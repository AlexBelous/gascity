/* In-scan exit proof for a PID whose environment vanished before ownership
 * classification. The only witness is the original capture's fresh bound
 * pidfd; this assembler performs no later replacement syscall. */
#ifndef GC_CENSUS_UNCLASSIFIED_RETIREMENT_H
#define GC_CENSUS_UNCLASSIFIED_RETIREMENT_H
#include "census-standalone-absence.h"

static inline bool census_resolve_unclassified_retirement(struct census_global_source *s,
    struct census_resolution_ledger *r,unsigned error_index) {
  if(r->denied || !error_index || error_index>s->errors_n || s->truncated ||
      s->errors_n!=s->errors_total || expired() || s->budget->fd_failed ||
      r->proofs_n>=128 || r->resolutions_n>=128 || !ev.trusted_kernel || fixture ||
      !ev.ns[0] || strcmp(ev.ns,ev.binding.ns)) return false;
  const struct census_global_fault *e=&s->errors[error_index-1];
  if(strcmp(e->reason,"process_unavailable") || !e->raw.operation ||
      strcmp(e->raw.operation,"environ") || e->raw.error!=ESRCH ||
      !e->raw.start || !e->raw.bound_exit_valid ||
      e->raw.bound_start!=e->raw.start || e->raw.pid<=1 ||
      e->raw.pid==ev.binding.pid || !e->scan_index || e->scan_index>s->history->n) return false;
  for(unsigned i=0;i<r->resolutions_n;i++)
    if(r->resolutions[i].error_index==error_index) return false;
  for(unsigned scan=0;scan<e->scan_index-1;scan++)
    if(census_row_verified(census_history_find(&s->history->scans[scan],e->raw.pid))) return false;
  struct proof_item proof={.kind=CENSUS_UNCLASSIFIED_INCARNATION_RETIRED,
    .method="bound_pidfd_exited",.pid=e->raw.pid,.start=e->raw.start,
    .scan_index=(int)e->scan_index,.offset_ms=e->raw.bound_exit_offset_ms};
  unsigned index=r->proofs_n+1;
  r->proofs[r->proofs_n++]=(struct census_typed_proof){.source=proof};
  r->resolutions[r->resolutions_n++]=(struct census_typed_resolution){
    .error_index=error_index,.proof_index=index,
    .kind=CENSUS_RESOLUTION_UNCLASSIFIED_RETIREMENT};
  bool valid=census_typed_proofs_valid(r) &&
    census_unclassified_link_valid(s,r,&r->resolutions[r->resolutions_n-1]) &&
    census_standalone_history_valid(s,r);
  if(!valid) r->denied=true;
  return valid;
}
#endif
