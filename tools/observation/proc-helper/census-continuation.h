/* Bounded internal source continuation. Neither a COMPLETE serializer nor
 * provider authority. The original fail-stop wrappers/latches are unchanged. */
#ifndef GC_CENSUS_CONTINUATION_H
#define GC_CENSUS_CONTINUATION_H
#include "census-global-scan.h"
#include "census-resolution-ledger.h"
#include "census-standalone-absence.h"

static inline bool census_positive_birth(const struct census_global_source *s,
    const struct census_global_fault *e) {
  if(strcmp(e->reason,"uninspected_birth") || !e->raw.operation || strcmp(e->raw.operation,"census") ||
      e->raw.error || e->raw.pid<=1 || e->raw.pid==ev.binding.pid ||
      !e->scan_index || e->scan_index>s->history->n) return false;
  struct census_history_row *original=census_history_find(&s->history->scans[e->scan_index-1],e->raw.pid);
  return census_row_verified(original) && e->raw.start==original->identity.start;
}
static inline bool census_continuation_gate(struct census_global_source *s,
    struct census_resolution_ledger *r) {
  if(r->denied || !census_typed_proofs_valid(r) || !census_recorded_prefix_valid(s) ||
      !census_standalone_history_valid(s,r)) return false;
  for(unsigned i=0;i<r->certificates_n;i++) if(!census_certificate_history(s->history,&r->certificates[i])) return false;
  for(unsigned e=0;e<s->errors_n;e++) {
    /* A positively captured seal birth may motivate the next bounded triplet.
     * It stays UNRESOLVED until two fresh closings and a seal verify it. */
    if(census_positive_birth(s,&s->errors[e])) continue;
    bool resolved=false;
    for(unsigned k=0;k<r->resolutions_n;k++) if(r->resolutions[k].error_index==e+1) resolved=true;
    if(!resolved && !census_resolve_descendant(s,r,e+1) && !census_resolve_standalone(s,r,e+1)) return false;
  }
  return true;
}
static inline bool census_same_classified(const struct census_history_row *a,
    const struct census_history_row *b) {
  if(!census_row_verified(a) || !census_row_verified(b) || a->classification!=b->classification) return false;
  if(a->classification==CENSUS_CLASS_KERNEL) {
    struct census_owned_identity x=a->identity,y=b->identity;x.name=y.name="";
    return census_same_owned(&x,&y);
  }
  return census_same_owned(&a->identity,&b->identity);
}
#include "census-closing-reconcile.h"
static inline bool census_finish_births(struct census_global_source *s,
    struct census_resolution_ledger *r,const struct census_round_journal *j) {
  if(j->selected_seal!=s->history->n || !j->selected_first || !j->selected_second) return false;
  for(unsigned e=0;e<s->errors_n;e++) {
    if(!census_positive_birth(s,&s->errors[e])) continue;
    const struct census_global_fault *error=&s->errors[e];unsigned first=j->selected_first;
    if(first<=error->scan_index || r->resolutions_n>=128) return false;
    struct census_history_row *a=census_history_find(&s->history->scans[first-1],error->raw.pid);
    struct census_history_row *b=census_history_find(&s->history->scans[j->selected_second-1],error->raw.pid);
    struct census_history_row *seal=census_history_find(&s->history->scans[j->selected_seal-1],error->raw.pid);
    struct census_history_row *original=census_history_find(&s->history->scans[error->scan_index-1],error->raw.pid);
    if(!census_same_classified(original,a) || !census_same_classified(a,b) || !census_same_classified(b,seal)) return false;
    r->resolutions[r->resolutions_n++]=(struct census_typed_resolution){.error_index=e+1,
      .kind=CENSUS_RESOLUTION_FRESH,.classified_scan=first,.selected_seal=j->selected_seal};
  }
  return true;
}
static inline bool census_final_resolutions_valid(const struct census_global_source *s,
    const struct census_resolution_ledger *r,const struct census_round_journal *j) {
  if(r->resolutions_n!=s->errors_n || r->denied || !census_typed_proofs_valid(r) ||
      !census_recorded_prefix_valid(s) || !census_standalone_history_valid(s,r) || j->selected_seal!=s->history->n ||
      j->selected_first+1!=j->selected_second || j->selected_second+1!=j->selected_seal) return false;
  for(unsigned c=0;c<r->certificates_n;c++) if(!census_certificate_history(s->history,&r->certificates[c])) return false;
  for(unsigned e=0;e<s->errors_n;e++) {
    unsigned matches=0;
    for(unsigned k=0;k<r->resolutions_n;k++) {
      const struct census_typed_resolution *v=&r->resolutions[k];if(v->error_index!=e+1) continue;
      if(v->kind==CENSUS_RESOLUTION_ABSENCE) {
        if(!census_absence_raw_eligible(&s->errors[e]) || !v->proof_index || v->proof_index>r->proofs_n ||
            v->classified_scan || v->selected_seal) return false;
        const struct census_typed_proof *p=&r->proofs[v->proof_index-1];
        if(!census_absence_proof_matches(&s->errors[e],&p->source)) return false;
      } else {
        if(v->kind!=CENSUS_RESOLUTION_FRESH || !census_positive_birth(s,&s->errors[e]) ||
            v->proof_index || v->classified_scan!=j->selected_first || v->selected_seal!=j->selected_seal ||
            v->classified_scan<=s->errors[e].scan_index) return false;
        uint32_t pid=s->errors[e].raw.pid;
        struct census_history_row *a=census_history_find(&s->history->scans[j->selected_first-1],pid);
        struct census_history_row *b=census_history_find(&s->history->scans[j->selected_second-1],pid);
        struct census_history_row *seal=census_history_find(&s->history->scans[j->selected_seal-1],pid);
        struct census_history_row *original=census_history_find(&s->history->scans[s->errors[e].scan_index-1],pid);
        if(!census_same_classified(original,a) || !census_same_classified(a,b) || !census_same_classified(b,seal)) return false;
      }
      matches++;
    }
    if(matches!=1) return false;
  }
  return true;
}
static inline bool census_continuation_capture(struct census_global_source *s,
    struct census_resolution_ledger *r,struct census_round_journal *j,
    unsigned round,enum census_scan_kind kind) {
  if(j->n>=10 || (j->n && !census_continuation_gate(s,r))) return false;
  unsigned before=s->history->n;struct census_round_scan receipt={.scan_id=j->n+1,.round=round,.kind=kind};
  /* Sticky h.failed/s.fatal are preserved. Only this positively checked source
   * gate can record another slot, never a caller-provided retry permission. */
  (void)census_global_scan_record(s,j->n+1,round,kind,&receipt);
  if(s->history->n!=before+1) return false;
  j->scans[j->n++]=receipt;
  return census_continuation_gate(s,r);
}
static inline enum census_round_result census_choose_proven_continuation(
    struct census_global_source *s,struct census_resolution_ledger *r,
    struct census_round_journal *j) {
  if(j->n || s->history->n || r->resolutions_n ||
      !census_continuation_capture(s,r,j,0,CENSUS_INITIAL)) return CENSUS_ROUNDS_UNKNOWN;
  for(unsigned round=1;round<=3;round++) {
    unsigned first=j->n;
    if(!census_continuation_capture(s,r,j,round,CENSUS_CLOSING) ||
        !census_continuation_capture(s,r,j,round,CENSUS_CLOSING) ||
        !census_continuation_capture(s,r,j,round,CENSUS_SEAL)) return CENSUS_ROUNDS_UNKNOWN;
    const struct census_round_scan *a=&j->scans[first],*b=&j->scans[first+1],*seal=&j->scans[first+2];
    if(!census_current_closings_equal(s,r)) continue;
    j->selected_first=a->scan_id;j->selected_second=b->scan_id;j->selected_seal=seal->scan_id;
    if(!census_finish_births(s,r,j) || !census_final_resolutions_valid(s,r,j)) return CENSUS_ROUNDS_UNKNOWN;
    return CENSUS_ROUNDS_SELECTED_PENDING_PROOF;
  }
  return CENSUS_ROUNDS_UNKNOWN;
}
#endif
