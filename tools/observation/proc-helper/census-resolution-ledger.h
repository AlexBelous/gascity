/* Source-owned typed resolutions. Never clears raw faults or grants provider
 * authority. A managed certificate stays provisional until BOTH ordinary joins.
 * Included after observer.c, census-v3-reader.h and census-global-scan.h. */
#ifndef GC_CENSUS_RESOLUTION_LEDGER_H
#define GC_CENSUS_RESOLUTION_LEDGER_H
#include "census-descendant-witness.h"

struct census_typed_proof {
  struct proof_item source;
  bool protected_identity;
  unsigned certificate_id;
};
struct census_typed_certificate {
  unsigned id,prior_scan,absence_proof,retirement_proof;
  struct census_descendant_witness witness;
};
enum census_resolution_kind {CENSUS_RESOLUTION_ABSENCE,CENSUS_RESOLUTION_FRESH,
  CENSUS_RESOLUTION_UNCLASSIFIED_RETIREMENT};
struct census_typed_resolution {
  unsigned error_index,proof_index,classified_scan,selected_seal;
  enum census_resolution_kind kind;
};
struct census_resolution_ledger {
  struct census_typed_proof proofs[128];
  struct census_typed_certificate certificates[64];
  struct census_typed_resolution resolutions[128];
  unsigned proofs_n,certificates_n,resolutions_n;
  bool denied;
};
static inline struct census_history_row *census_history_find(
    const struct census_history_scan *scan,uint32_t pid) {
  size_t lo=0,hi=scan->allocated_rows;
  while(lo<hi) {size_t mid=lo+(hi-lo)/2;
    if(scan->rows[mid].pid<pid) lo=mid+1;else hi=mid;
  }
  return lo<scan->allocated_rows && scan->rows[lo].pid==pid?&scan->rows[lo]:NULL;
}
static inline bool census_row_verified(const struct census_history_row *row) {
  return row && row->failure==CENSUS_ROW_OK && row->classification!=CENSUS_CLASS_INVALID;
}
static inline struct census_resolution_ledger *census_resolution_new(struct census_budget *b) {
  return census_alloc(b,sizeof(struct census_resolution_ledger));
}
static inline void census_resolution_free(struct census_budget *b,struct census_resolution_ledger *r) {
  if(!r) return;
  for(unsigned i=0;i<r->certificates_n;i++) census_descendant_witness_clear(b,&r->certificates[i].witness);
  census_release(b,r,sizeof *r);
}
/* Every recorded later scan must contain every continuing ancestor. A single
 * current-root check cannot authorize a missing or changed intermediate root. */
static inline bool census_certificate_history(const struct census_history *h,
    const struct census_typed_certificate *cert) {
  if(!cert->prior_scan || cert->prior_scan>=h->n || !cert->witness.provisional ||
      cert->witness.chain_n<2 || cert->witness.chain_n>128) return false;
  for(size_t i=0;i<cert->witness.chain_n;i++) {
    const struct census_owned_identity *v=&cert->witness.chain[i];
    struct census_history_row *prior=census_history_find(&h->scans[cert->prior_scan-1],v->pid);
    if(!census_row_verified(prior) || !census_same_owned(v,&prior->identity)) return false;
    for(unsigned s=cert->prior_scan;s<h->n;s++) {
      struct census_history_row *fresh=census_history_find(&h->scans[s],v->pid);
      if(i) {
        if(!census_row_verified(fresh) || !census_same_owned(v,&fresh->identity)) return false;
      } else if(census_row_verified(fresh) && fresh->identity.start==v->start) return false;
    }
  }
  return true;
}
static inline bool census_same_proof_source(const struct proof_item *a,const struct proof_item *b) {
  return a->kind==b->kind && a->method && b->method && !strcmp(a->method,b->method) &&
    a->pid==b->pid && a->start==b->start && a->replacement_start==b->replacement_start &&
    a->offset_ms==b->offset_ms && a->scan_index==b->scan_index;
}
/* The serializer must use this typed carrier, never proof_item verbatim. */
static inline bool census_typed_proofs_valid(const struct census_resolution_ledger *r) {
  if(r->certificates_n>64 || r->proofs_n>128 || r->resolutions_n>128) return false;
  bool protected_used[128]={0};
  for(unsigned i=0;i<r->proofs_n;i++) {
    const struct census_typed_proof *p=&r->proofs[i];const struct proof_item *v=&p->source;
    if(v->pid<=1 || v->pid>INT32_MAX || v->scan_index<1 || v->scan_index>10 ||
        v->offset_ms>=10000 || v->replacement_start || !v->method) return false;
    if(v->kind==CENSUS_ENUMERATED_ABSENT) {
      if(v->start || p->protected_identity || p->certificate_id || strcmp(v->method,"pidfd_no_pid")) return false;
    } else if(v->kind==CENSUS_TERMINAL_ZOMBIE) {
      if(!v->start || p->protected_identity || p->certificate_id || strcmp(v->method,"pidfd_zombie_stat")) return false;
    } else if(v->kind==CENSUS_INCARNATION_RETIRED) {
      if(!v->start || strcmp(v->method,"pidfd_no_pid") ||
          (p->protected_identity?(!p->certificate_id || p->certificate_id>r->certificates_n):p->certificate_id!=0)) return false;
    } else if(v->kind==CENSUS_UNCLASSIFIED_INCARNATION_RETIRED) {
      if(!v->start || p->protected_identity || p->certificate_id ||
          strcmp(v->method,"bound_pidfd_exited")) return false;
    } else return false;
    for(unsigned k=0;k<i;k++) {
      const struct proof_item *old=&r->proofs[k].source;
      if(old->pid==v->pid && old->start==v->start &&
          (old->scan_index==v->scan_index || v->kind==CENSUS_INCARNATION_RETIRED ||
           v->kind==CENSUS_UNCLASSIFIED_INCARNATION_RETIRED ||
           old->kind==CENSUS_INCARNATION_RETIRED ||
           old->kind==CENSUS_UNCLASSIFIED_INCARNATION_RETIRED)) return false;
    }
  }
  for(unsigned i=0;i<r->certificates_n;i++) {
    const struct census_typed_certificate *c=&r->certificates[i];
    if(c->id!=i+1 || !c->absence_proof || !c->retirement_proof ||
        c->absence_proof>=c->retirement_proof || c->retirement_proof>r->proofs_n || protected_used[c->retirement_proof-1]) return false;
    const struct census_typed_proof *a=&r->proofs[c->absence_proof-1],*known=&r->proofs[c->retirement_proof-1];
    if(a->protected_identity || a->certificate_id || !known->protected_identity || known->certificate_id!=c->id ||
        !census_same_proof_source(&a->source,&c->witness.absence) ||
        !census_same_proof_source(&known->source,&c->witness.retirement) ||
        a->source.kind!=CENSUS_ENUMERATED_ABSENT || a->source.start || a->source.replacement_start ||
        !a->source.method || strcmp(a->source.method,"pidfd_no_pid") ||
        known->source.kind!=CENSUS_INCARNATION_RETIRED || !known->source.start ||
        known->source.replacement_start || strcmp(known->source.method,"pidfd_no_pid") ||
        a->source.pid!=known->source.pid || a->source.scan_index!=known->source.scan_index ||
        a->source.offset_ms!=known->source.offset_ms) return false;
    protected_used[c->retirement_proof-1]=true;
  }
  for(unsigned i=0;i<r->proofs_n;i++) if(r->proofs[i].protected_identity!=protected_used[i]) return false;
  return true;
}
static inline bool census_absence_raw_eligible(const struct census_global_fault *e) {
  if(strcmp(e->reason,"process_unavailable") || !e->raw.operation || e->raw.pid<=1 ||
      e->raw.pid==ev.binding.pid) return false;
  return (!strcmp(e->raw.operation,"pidfd_open") && (e->raw.error==ESRCH || (e->raw.error==EINVAL && !e->raw.start))) ||
    (!strcmp(e->raw.operation,"pidfd_poll") && e->raw.error==ESTALE && !e->raw.start) ||
    (!strcmp(e->raw.operation,"stat") && e->raw.error==ENOENT) ||
    (!strcmp(e->raw.operation,"environ") && e->raw.error==ESRCH) ||
    (!strcmp(e->raw.operation,"status") && e->raw.error==ESRCH && e->raw.start) ||
    (!strcmp(e->raw.operation,"comm") && (e->raw.error==ENOENT || e->raw.error==ESRCH));
}
static inline bool census_absence_proof_matches(const struct census_global_fault *e,
    const struct proof_item *p) {
  if(p->pid!=e->raw.pid || p->scan_index<(int)e->scan_index) return false;
  if(p->kind==CENSUS_TERMINAL_ZOMBIE)
    return e->raw.operation && !strcmp(e->raw.operation,"pidfd_poll") &&
      e->raw.error==ESTALE && !e->raw.start && p->start &&
      p->method && !strcmp(p->method,"pidfd_zombie_stat");
  return p->start==e->raw.start;
}
/* The producer captured this in-scan exit on one freshly bound pidfd. An
 * arbitrary later no-PID syscall, env errno alone, or cross-scan proof cannot
 * be substituted for the original source witness. */
static inline bool census_unclassified_link_valid(const struct census_global_source *s,
    const struct census_resolution_ledger *r,const struct census_typed_resolution *link) {
  if(link->kind!=CENSUS_RESOLUTION_UNCLASSIFIED_RETIREMENT || !link->error_index ||
      link->error_index>s->errors_n || !link->proof_index || link->proof_index>r->proofs_n ||
      link->classified_scan || link->selected_seal) return false;
  const struct census_global_fault *e=&s->errors[link->error_index-1];
  const struct census_typed_proof *proof=&r->proofs[link->proof_index-1];
  const struct proof_item *p=&proof->source;
  if(strcmp(e->reason,"process_unavailable") || !e->raw.operation ||
      strcmp(e->raw.operation,"environ") || e->raw.error!=ESRCH || !e->raw.start ||
      !e->raw.bound_exit_valid || e->raw.bound_start!=e->raw.start ||
      e->raw.pid<=1 || e->raw.pid==ev.binding.pid || !e->scan_index ||
      e->scan_index>s->history->n || !ev.trusted_kernel || fixture ||
      !ev.ns[0] || strcmp(ev.ns,ev.binding.ns) ||
      p->kind!=CENSUS_UNCLASSIFIED_INCARNATION_RETIRED || !p->method ||
      strcmp(p->method,"bound_pidfd_exited") || proof->protected_identity ||
      proof->certificate_id || p->pid!=e->raw.pid || p->start!=e->raw.start ||
      p->replacement_start || p->scan_index!=(int)e->scan_index ||
      p->offset_ms!=e->raw.bound_exit_offset_ms ||
      p->offset_ms>s->history->scans[e->scan_index-1].receipt.offset_ms ||
      (e->scan_index>1 && p->offset_ms<
        s->history->scans[e->scan_index-2].receipt.offset_ms)) return false;
  return true;
}
/* Derive the full chain ONLY from one immutable prior scan; no supplied chain,
 * root selector, errno or callback proof is accepted by this assembler. */
static inline bool census_resolve_descendant(struct census_global_source *s,
    struct census_resolution_ledger *r,unsigned error_index) {
  if(r->denied || !error_index || error_index>s->errors_n || s->truncated ||
      s->errors_n!=s->errors_total || expired() || s->budget->fd_failed ||
      r->certificates_n>=64 || r->proofs_n>126 || r->resolutions_n>=128) return false;
  const struct census_global_fault *e=&s->errors[error_index-1];
  struct census_history *h=s->history;
  if(!census_absence_raw_eligible(e) || e->scan_index<2 || e->scan_index>h->n ||
      (!e->raw.start && strcmp(e->raw.operation,"pidfd_open") && strcmp(e->raw.operation,"stat"))) return false;
  for(unsigned i=0;i<r->resolutions_n;i++) if(r->resolutions[i].error_index==error_index) return false;
  unsigned prior_index=0;struct census_history_row *leaf=NULL;
  for(unsigned scan=e->scan_index-1;scan;scan--) {
    struct census_history_row *v=census_history_find(&h->scans[scan-1],e->raw.pid);
    if(census_row_verified(v)) {prior_index=scan;leaf=v;break;}
  }
  if(!leaf || leaf->classification!=CENSUS_CLASS_MANAGED || leaf->identity.declared_root ||
      (e->raw.start && e->raw.start!=leaf->identity.start)) return false;
  /* Temporary arrays are charged too; final witness deep-copies before release. */
  struct census_owned_identity *chain=census_alloc(s->budget,128*sizeof *chain);
  struct census_owned_identity *fresh=census_alloc(s->budget,127*sizeof *fresh);
  if(!chain || !fresh) {
    census_release(s->budget,chain,128*sizeof *chain);census_release(s->budget,fresh,127*sizeof *fresh);return false;
  }
  size_t count=0;struct census_history_row *at=leaf;bool valid=true;
  while(valid) {
    if(count==128 || !census_row_verified(at) || at->classification!=CENSUS_CLASS_MANAGED ||
        !census_managed_identity(&at->identity) || !census_same_inherited(&leaf->identity,&at->identity)) {valid=false;break;}
    for(size_t k=0;k<count;k++) if(chain[k].pid==at->pid) valid=false;
    if(!valid) break;
    chain[count++]=at->identity; /* Borrowed only inside this bounded operation. */
    if(at->identity.declared_root) break;
    at=census_history_find(&h->scans[prior_index-1],at->identity.ppid);
  }
  if(count<2 || !chain[count-1].declared_root) valid=false;
  for(size_t i=1;valid && i<count;i++) {
    for(unsigned scan=prior_index;scan<h->n;scan++) {
      struct census_history_row *v=census_history_find(&h->scans[scan],chain[i].pid);
      if(!census_row_verified(v) || !census_same_owned(&chain[i],&v->identity)) {valid=false;break;}
      fresh[i-1]=v->identity;
    }
  }
  for(unsigned scan=prior_index;valid && scan<h->n;scan++) {
    struct census_history_row *v=census_history_find(&h->scans[scan],leaf->pid);
    if(census_row_verified(v) && v->identity.start==leaf->identity.start) valid=false;
  }
  struct census_descendant_witness witness={0};
  if(valid) valid=census_descendant_no_pid(s->budget,chain,count,fresh,(int)h->n,&witness);
  census_release(s->budget,chain,128*sizeof *chain);census_release(s->budget,fresh,127*sizeof *fresh);
  if(!valid) return false;
  /* Exactly one known-start retirement per incarnation in the global ledger. */
  for(unsigned i=0;i<r->certificates_n;i++) if(r->certificates[i].witness.retirement.pid==witness.retirement.pid &&
      r->certificates[i].witness.retirement.start==witness.retirement.start) {
    census_descendant_witness_clear(s->budget,&witness);return false;
  }
  unsigned id=r->certificates_n+1,absence=r->proofs_n+1,retirement=r->proofs_n+2;
  r->certificates[r->certificates_n++]=(struct census_typed_certificate){.id=id,.prior_scan=prior_index,
    .absence_proof=absence,.retirement_proof=retirement,.witness=witness};
  r->proofs[r->proofs_n++]=(struct census_typed_proof){.source=witness.absence};
  r->proofs[r->proofs_n++]=(struct census_typed_proof){.source=witness.retirement,
    .protected_identity=true,.certificate_id=id};
  r->resolutions[r->resolutions_n++]=(struct census_typed_resolution){.error_index=error_index,
    .proof_index=e->raw.start?retirement:absence,.kind=CENSUS_RESOLUTION_ABSENCE};
  return true;
}
/* This check is repeated before every subsequent capture and final serialization;
 * it does not clear failed/fatal history or manufacture COMPLETE/provider authority. */
static inline bool census_resolution_prefix_valid(const struct census_global_source *s,
    const struct census_resolution_ledger *r) {
  if(r->denied || !census_typed_proofs_valid(r) || s->truncated || s->errors_n!=s->errors_total || s->budget->fd_failed || expired()) return false;
  for(unsigned i=0;i<r->certificates_n;i++) if(!census_certificate_history(s->history,&r->certificates[i])) return false;
  for(unsigned i=0;i<s->errors_n;i++) {
    const struct census_global_fault *e=&s->errors[i];unsigned matched=0;
    for(unsigned k=0;k<r->resolutions_n;k++) {
      const struct census_typed_resolution *resolution=&r->resolutions[k];
      if(resolution->error_index!=i+1) continue;
      if(resolution->kind==CENSUS_RESOLUTION_UNCLASSIFIED_RETIREMENT) {
        if(!census_unclassified_link_valid(s,r,resolution)) return false;
      } else {
        if(resolution->kind!=CENSUS_RESOLUTION_ABSENCE || !census_absence_raw_eligible(e) ||
            !resolution->proof_index || resolution->proof_index>r->proofs_n) return false;
        const struct census_typed_proof *proof=&r->proofs[resolution->proof_index-1];
        if(!census_absence_proof_matches(e,&proof->source)) return false;
      }
      matched++;
    }
    if(matched!=1) return false;
  }
  return true;
}
/* Structural prefix checks are independent of the sticky historical failure
 * latch. An unexplained hard failure can never be bypassed by an empty ledger. */
static inline bool census_recorded_prefix_valid(const struct census_global_source *s) {
  const struct census_history *h=s->history;
  if(!h->n || h->n>10 || h->truncated || s->truncated || s->errors_n!=s->errors_total ||
      s->budget->fd_failed || expired()) return false;
  for(unsigned i=0;i<h->n;i++) {
    const struct census_history_scan *scan=&h->scans[i];const struct census_round_scan *receipt=&scan->receipt;
    unsigned round=i?((i-1)/3+1):0;
    enum census_scan_kind kind=!i?CENSUS_INITIAL:(i%3==0?CENSUS_SEAL:CENSUS_CLOSING);
    if(receipt->scan_id!=i+1 || receipt->round!=round || receipt->kind!=kind ||
        receipt->offset_ms>=10000 || (i && receipt->offset_ms<h->scans[i-1].receipt.offset_ms) ||
        scan->allocation_failed || receipt->enumerated!=scan->allocated_rows || !scan->rows ||
        !receipt->enumerated || receipt->enumerated>65536 || !receipt->classified ||
        !census_journal_digest(receipt->raw_digest) || !census_journal_digest(receipt->live_digest)) return false;
    unsigned failed=0;size_t classified=0;
    for(size_t k=0;k<scan->allocated_rows;k++) {
      const struct census_history_row *row=&scan->rows[k];
      if(!row->pid || (k && row->pid<=scan->rows[k-1].pid)) return false;
      if(census_row_verified(row)) {classified++;continue;}
      failed++;unsigned raw_matches=0;
      for(unsigned e=0;e<s->errors_n;e++) if(s->errors[e].scan_index==i+1 &&
          s->errors[e].raw.pid==row->pid && !strcmp(s->errors[e].reason,"process_unavailable")) raw_matches++;
      if(raw_matches!=1) return false;
    }
    if(failed!=receipt->hard_unresolved || classified!=receipt->classified ||
        receipt->classified_all!=(failed==0)) return false;
  }
  return true;
}
#endif
