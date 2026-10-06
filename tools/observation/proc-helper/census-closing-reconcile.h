/* Compare the CURRENT source triplet after independently indexed retirements.
 * Raw scans, digests, faults and sticky latches are never rewritten. No syscall,
 * synthetic witness, caller selector or additional round is introduced here.
 * Include after census_positive_birth and census_same_classified. */
#ifndef GC_CENSUS_CLOSING_RECONCILE_H
#define GC_CENSUS_CLOSING_RECONCILE_H

static inline bool census_closing_absence_link(const struct census_global_source *s,
    const struct census_resolution_ledger *r,const struct census_history_scan *scan,
    const struct census_history_row *row) {
  unsigned matches=0;
  for(unsigned e=0;e<s->errors_n;e++) {
    const struct census_global_fault *fault=&s->errors[e];
    if(fault->scan_index!=scan->receipt.scan_id || fault->raw.pid!=row->pid) continue;
    if(!census_absence_raw_eligible(fault)) return false;
    for(unsigned i=0;i<r->resolutions_n;i++) {
      const struct census_typed_resolution *v=&r->resolutions[i];
      if(v->error_index!=e+1) continue;
      if(v->kind!=CENSUS_RESOLUTION_ABSENCE || !v->proof_index ||
          v->proof_index>r->proofs_n || v->classified_scan || v->selected_seal) return false;
      const struct proof_item *p=&r->proofs[v->proof_index-1].source;
      if(!census_absence_proof_matches(fault,p) || p->scan_index>(int)s->history->n) return false;
      matches++;
    }
  }
  return matches==1;
}
static inline bool census_closing_retired(const struct census_global_source *s,
    const struct census_resolution_ledger *r,const struct census_history_scan *scan,
    const struct census_history_row *row) {
  const struct census_owned_identity *v=&row->identity;
  if(!census_row_verified(row) || v->declared_root || row->classification==CENSUS_CLASS_KERNEL ||
      v->pid==ev.binding.pid || infra(v->name)) return false;
  unsigned matches=0;
  for(unsigned i=0;i<r->proofs_n;i++) {
    const struct census_typed_proof *p=&r->proofs[i];
    if(p->source.kind!=CENSUS_INCARNATION_RETIRED || p->source.pid!=v->pid ||
        p->source.start!=v->start || p->source.scan_index<=(int)scan->receipt.scan_id ||
        p->source.scan_index>(int)s->history->n) continue;
    if(p->protected_identity!=(row->classification==CENSUS_CLASS_MANAGED)) return false;
    matches++;
  }
  return matches==1;
}
static inline bool census_closing_survivors_equal(const struct census_global_source *s,
    const struct census_resolution_ledger *r,const struct census_history_scan *closing,
    const struct census_history_scan *seal) {
  /* Equal raw counts cannot conceal an unproved raw digest mutation. */
  if(closing->receipt.classified==seal->receipt.classified &&
      strcmp(closing->receipt.live_digest,seal->receipt.live_digest)) return false;
  size_t survivors=0;
  for(size_t i=0;i<closing->allocated_rows;i++) {
    const struct census_history_row *row=&closing->rows[i];
    if(!census_row_verified(row)) {
      if(!census_closing_absence_link(s,r,closing,row)) return false;
      continue;
    }
    if(census_closing_retired(s,r,closing,row)) continue;
    const struct census_history_row *last=census_history_find(seal,row->pid);
    if(!census_same_classified(row,last)) return false;
    survivors++;
  }
  if(survivors!=seal->receipt.classified) return false;
  /* Every live seal row, including caller/infra, must have BOTH closing reads.
   * A newly born or reused surviving PID cannot enter via a count subtraction. */
  for(size_t i=0;i<seal->allocated_rows;i++) {
    const struct census_history_row *last=&seal->rows[i];
    if(!census_row_verified(last)) {
      if(!census_closing_absence_link(s,r,seal,last)) return false;
      continue;
    }
    const struct census_history_row *prior=census_history_find(closing,last->pid);
    if(!census_same_classified(prior,last)) return false;
  }
  return true;
}
static inline bool census_current_closings_equal(const struct census_global_source *s,
    const struct census_resolution_ledger *r) {
  unsigned n=s->history->n;
  if((n!=4 && n!=7 && n!=10) || r->denied || !census_recorded_prefix_valid(s) ||
      !census_typed_proofs_valid(r) || !census_standalone_history_valid(s,r)) return false;
  /* Namespace origin is established by the existing immutable proc/bootstrap
   * and witness creator fences. This pure comparator neither replaces them nor
   * infers namespace authority from optional unit-fixture metadata strings. */
  if(r->proofs_n && (!ev.trusted_kernel || fixture)) return false;
  for(unsigned c=0;c<r->certificates_n;c++)
    if(!census_certificate_history(s->history,&r->certificates[c])) return false;
  for(unsigned e=0;e<s->errors_n;e++) {
    if(census_positive_birth(s,&s->errors[e])) continue;
    unsigned matches=0;
    for(unsigned i=0;i<r->resolutions_n;i++) {
      const struct census_typed_resolution *v=&r->resolutions[i];
      if(v->error_index!=e+1) continue;
      if(v->kind!=CENSUS_RESOLUTION_ABSENCE || !census_absence_raw_eligible(&s->errors[e]) ||
          !v->proof_index || v->proof_index>r->proofs_n || v->classified_scan || v->selected_seal) return false;
      const struct proof_item *p=&r->proofs[v->proof_index-1].source;
      if(!census_absence_proof_matches(&s->errors[e],p) || p->scan_index>(int)n) return false;
      matches++;
    }
    if(matches!=1) return false;
  }
  const struct census_history_scan *a=&s->history->scans[n-3],*b=&s->history->scans[n-2],
    *seal=&s->history->scans[n-1];
  if(a->receipt.kind!=CENSUS_CLOSING || b->receipt.kind!=CENSUS_CLOSING ||
      seal->receipt.kind!=CENSUS_SEAL || seal->receipt.late_birth) return false;
  /* This derives full seal coverage from ALL live reads or indexed absences;
   * it does not flip captured/failed/seal_revalidated flags after a raw fault. */
  return census_closing_survivors_equal(s,r,a,seal) && census_closing_survivors_equal(s,r,b,seal);
}
#endif
