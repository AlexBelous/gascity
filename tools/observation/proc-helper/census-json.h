/* Charged V3 census codec. Source-only until the complete helper envelope,
 * bootstrap bounds and ordinary BOTH-frame provider join are integrated. */
#ifndef GC_CENSUS_JSON_H
#define GC_CENSUS_JSON_H
#include "census-continuation.h"
struct census_json {
  struct census_budget *budget;
  char *data;size_t n,capacity,retained_offset;
  char scratch[768];
  bool failed,retained_set;
};
static inline struct census_json *census_json_new(struct census_budget *b) {
  struct census_json *o=census_alloc(b,sizeof *o);if(o) o->budget=b;return o;
}
static inline void census_json_free(struct census_json *o) {
  if(!o) return;
  struct census_budget *b=o->budget;
  census_release(b,o->data,o->capacity);census_release(b,o,sizeof *o);
}
static inline void census_json_bytes(struct census_json *o,const char *data,size_t n) {
  if(o->failed) return;
  if(expired() || o->budget->fd_failed || n>MAX_OUTPUT || o->n>MAX_OUTPUT-n) {o->failed=true;return;}
  size_t need=o->n+n;
  if(need>o->capacity) {
    size_t cap=o->capacity?o->capacity:4096;
    while(cap<need) {if(cap>MAX_OUTPUT/2) {cap=MAX_OUTPUT;break;}cap*=2;}
    char *next=census_alloc(o->budget,cap);if(!next) {o->failed=true;return;}
    if(o->n) memcpy(next,o->data,o->n);
    census_release(o->budget,o->data,o->capacity);o->data=next;o->capacity=cap;
  }
  memcpy(o->data+o->n,data,n);o->n+=n;
}
static inline void census_json_text(struct census_json *o,const char *text) {census_json_bytes(o,text,strlen(text));}
static inline void census_json_printf(struct census_json *o,const char *format,...) {
  va_list args;va_start(args,format);int n=vsnprintf(o->scratch,sizeof o->scratch,format,args);va_end(args);
  if(n<0 || (size_t)n>=sizeof o->scratch) {o->failed=true;return;}
  census_json_bytes(o,o->scratch,(size_t)n);
}
static inline void census_json_string(struct census_json *o,const char *text) {
  if(!text || strnlen(text,4097)>4096) {o->failed=true;return;}
  census_json_text(o,"\"");
  for(const unsigned char *p=(const unsigned char *)text;*p;p++) {
    if(*p=='"' || *p=='\\') census_json_printf(o,"\\%c",*p);
    else if(*p<32) census_json_printf(o,"\\u%04x",*p);
    else census_json_bytes(o,(const char *)p,1);
  }
  census_json_text(o,"\"");
}
static inline void census_json_identity(struct census_json *o,const struct census_owned_identity *v,
    enum census_row_class classification) {
  census_json_printf(o,"{\"classification\":\"%s\",\"kernel_flags\":%u,\"no_gc_environment\":%s,\"environment_revalidated\":%s,\"pid\":%u,\"ppid\":%u,\"pgid\":%u,\"uids\":[%u,%u,%u,%u],\"start_ticks\":\"%" PRIu64 "\",\"epoch\":%" PRIu64 ",\"session_id\":",
    classification==CENSUS_CLASS_MANAGED?"managed":classification==CENSUS_CLASS_KERNEL?"kernel":"nonmanaged",
    v->kernel_flags,v->no_gc_environment?"true":"false",v->environment_revalidated?"true":"false",
    v->pid,v->ppid,v->pgid,v->uids[0],v->uids[1],v->uids[2],v->uids[3],v->start,v->epoch);
  census_json_string(o,v->sid);census_json_text(o,",\"city\":");census_json_string(o,v->city);
  census_json_text(o,",\"template\":");census_json_string(o,v->template);
  census_json_text(o,",\"instance_token_sha256\":");census_json_string(o,v->token);
  census_json_text(o,",\"name\":");census_json_string(o,v->name);
  census_json_printf(o,",\"declared_root\":%s,\"stat_revalidated\":%s,\"pidfd_bound\":%s,\"uids_revalidated\":%s}",
    v->declared_root?"true":"false",v->stat_revalidated?"true":"false",v->pidfd_bound?"true":"false",v->uids_revalidated?"true":"false");
}
static inline void census_json_census(struct census_json *o,const struct census_global_source *s,
    const struct census_resolution_ledger *r,const struct census_round_journal *j) {
  if(!census_final_resolutions_valid(s,r,j)) {o->failed=true;return;}
  census_json_text(o,"{\"schema\":\"bounded-process-census/v3\",\"scans\":[");
  for(unsigned i=0;i<s->history->n;i++) {
    const struct census_history_scan *scan=&s->history->scans[i];const struct census_round_scan *v=&scan->receipt;
    if(i) census_json_text(o,",");
    census_json_printf(o,"{\"scan_index\":%u,\"enumerated_count\":%zu,\"enumeration_digest\":\"%s\",\"live_count\":%zu,\"live_digest\":\"%s\",\"round\":%u,\"kind\":\"%s\",\"offset_ms\":%" PRIu64 ",\"verified\":[",
      v->scan_id,v->enumerated,v->raw_digest,v->classified,v->live_digest,v->round,
      v->kind==CENSUS_INITIAL?"initial":v->kind==CENSUS_CLOSING?"closing":"seal",v->offset_ms);
    bool comma=false;
    for(size_t k=0;k<scan->allocated_rows;k++) {
      const struct census_history_row *row=&scan->rows[k];
      if(!census_row_verified(row) || row->pid<=1 || row->pid==ev.binding.pid || infra(row->identity.name)) continue;
      if(comma) census_json_text(o,",");
      comma=true;
      census_json_identity(o,&row->identity,row->classification);
    }
    census_json_text(o,"]}");
  }
  const struct census_round_scan *seal=&s->history->scans[j->selected_seal-1].receipt;
  census_json_printf(o,"],\"selected_closings\":[%u,%u],\"seal\":{\"scan_index\":%u,\"enumerated_count\":%zu,\"pid_digest\":\"%s\",\"classified_count\":%zu,\"classified_digest\":\"%s\"},\"reconciled_count\":%zu,\"reconciled_digest\":\"%s\",\"proofs\":[",
    j->selected_first,j->selected_second,seal->scan_id,seal->enumerated,seal->raw_digest,seal->classified,seal->live_digest,seal->classified,seal->live_digest);
  for(unsigned i=0;i<r->proofs_n;i++) {
    const struct census_typed_proof *v=&r->proofs[i];const struct proof_item *p=&v->source;
    if(i) census_json_text(o,",");
    census_json_printf(o,"{\"kind\":\"%s\",\"method\":\"%s\",\"pid\":%u,\"start_ticks\":",census_proof_kind_name(p->kind),p->method,p->pid);
    if(p->start) census_json_printf(o,"\"%" PRIu64 "\"",p->start);else census_json_text(o,"null");
    census_json_printf(o,",\"replacement_start\":\"\",\"scan_index\":%d,\"offset_ms\":%" PRIu64 ",\"protected_identity\":%s,\"descendant_certificate_id\":%u}",p->scan_index,p->offset_ms,v->protected_identity?"true":"false",v->certificate_id);
  }
  census_json_text(o,"],\"certificates\":[");
  for(unsigned i=0;i<r->certificates_n;i++) {
    const struct census_typed_certificate *c=&r->certificates[i];if(i) census_json_text(o,",");
    census_json_printf(o,"{\"id\":%u,\"prior_scan\":%u,\"absence_proof\":%u,\"retirement_proof\":%u,\"chain\":[",c->id,c->prior_scan,c->absence_proof,c->retirement_proof);
    for(size_t k=0;k<c->witness.chain_n;k++) {if(k) census_json_text(o,",");census_json_identity(o,&c->witness.chain[k],CENSUS_CLASS_MANAGED);}
    census_json_text(o,"]}");
  }
  census_json_text(o,"],\"resolutions\":[");
  for(unsigned i=0;i<r->resolutions_n;i++) {
    const struct census_typed_resolution *v=&r->resolutions[i];if(i) census_json_text(o,",");
    census_json_printf(o,"{\"error_index\":%u,\"kind\":\"%s\",\"proof_index\":%u,\"classified_scan\":%u,\"selected_seal\":%u}",
      v->error_index,v->kind==CENSUS_RESOLUTION_ABSENCE?"kernel_absence":"fresh_classification",v->proof_index,v->classified_scan,v->selected_seal);
  }
  census_json_printf(o,"],\"peak_fd\":%u,\"retained_bytes\":",s->budget->fd_peak);
  o->retained_offset=o->n;o->retained_set=true;
  census_json_printf(o,"%16zu}",s->budget->peak);
}
/* UNKNOWN diagnostics retain the actual recorded prefix and original proofs.
 * Never selects a successful earlier triplet or clears a failed root/error.
 * The enclosing helper MUST mark this frame incomplete. */
static inline void census_json_unknown_census(struct census_json *o,const struct census_global_source *s,
    const struct census_resolution_ledger *r) {
  if(!census_typed_proofs_valid(r) || s->history->n>10 || s->errors_n>128) {o->failed=true;return;}
  census_json_text(o,"{\"schema\":\"bounded-process-census/v3\",\"scans\":[");
  for(unsigned i=0;i<s->history->n;i++) {
    const struct census_history_scan *scan=&s->history->scans[i];const struct census_round_scan *v=&scan->receipt;
    if(i) census_json_text(o,",");
    census_json_printf(o,"{\"scan_index\":%u,\"enumerated_count\":%zu,\"enumeration_digest\":\"%s\",\"live_count\":%zu,\"live_digest\":\"%s\",\"round\":%u,\"kind\":\"%s\",\"offset_ms\":%" PRIu64 ",\"verified\":[",
      v->scan_id,v->enumerated,v->raw_digest,v->classified,v->live_digest,v->round,
      v->kind==CENSUS_INITIAL?"initial":v->kind==CENSUS_CLOSING?"closing":"seal",v->offset_ms);
    bool comma=false;
    for(size_t k=0;k<scan->allocated_rows;k++) {
      const struct census_history_row *row=&scan->rows[k];
      if(!census_row_verified(row) || row->pid<=1 || row->pid==ev.binding.pid || infra(row->identity.name)) continue;
      if(comma) census_json_text(o,",");
      comma=true;census_json_identity(o,&row->identity,row->classification);
    }
    census_json_text(o,"]}");
  }
  census_json_text(o,"],\"selected_closings\":[0,0],\"seal\":{\"scan_index\":0,\"enumerated_count\":0,\"pid_digest\":\"\",\"classified_count\":0,\"classified_digest\":\"\"},\"reconciled_count\":0,\"reconciled_digest\":\"\",\"proofs\":[");
  for(unsigned i=0;i<r->proofs_n;i++) {
    const struct census_typed_proof *v=&r->proofs[i];const struct proof_item *p=&v->source;
    if(i) census_json_text(o,",");
    census_json_printf(o,"{\"kind\":\"%s\",\"method\":\"%s\",\"pid\":%u,\"start_ticks\":",census_proof_kind_name(p->kind),p->method,p->pid);
    if(p->start) census_json_printf(o,"\"%" PRIu64 "\"",p->start);else census_json_text(o,"null");
    census_json_printf(o,",\"replacement_start\":\"\",\"scan_index\":%d,\"offset_ms\":%" PRIu64 ",\"protected_identity\":%s,\"descendant_certificate_id\":%u}",p->scan_index,p->offset_ms,v->protected_identity?"true":"false",v->certificate_id);
  }
  census_json_text(o,"],\"certificates\":[");
  for(unsigned i=0;i<r->certificates_n;i++) {
    const struct census_typed_certificate *c=&r->certificates[i];if(i) census_json_text(o,",");
    census_json_printf(o,"{\"id\":%u,\"prior_scan\":%u,\"absence_proof\":%u,\"retirement_proof\":%u,\"chain\":[",c->id,c->prior_scan,c->absence_proof,c->retirement_proof);
    for(size_t k=0;k<c->witness.chain_n;k++) {if(k) census_json_text(o,",");census_json_identity(o,&c->witness.chain[k],CENSUS_CLASS_MANAGED);}
    census_json_text(o,"]}");
  }
  census_json_text(o,"],\"resolutions\":[");
  for(unsigned i=0;i<r->resolutions_n;i++) {
    const struct census_typed_resolution *v=&r->resolutions[i];if(i) census_json_text(o,",");
    census_json_printf(o,"{\"error_index\":%u,\"kind\":\"%s\",\"proof_index\":%u,\"classified_scan\":%u,\"selected_seal\":%u}",
      v->error_index,v->kind==CENSUS_RESOLUTION_ABSENCE?"kernel_absence":"fresh_classification",v->proof_index,v->classified_scan,v->selected_seal);
  }
  census_json_printf(o,"],\"peak_fd\":%u,\"retained_bytes\":",s->budget->fd_peak);
  o->retained_offset=o->n;o->retained_set=true;census_json_printf(o,"%16zu}",s->budget->peak);
}
static inline void census_json_errors(struct census_json *o,const struct census_global_source *s) {
  census_json_text(o,"[");
  for(unsigned i=0;i<s->errors_n;i++) {
    const struct census_global_fault *e=&s->errors[i];if(i) census_json_text(o,",");
    const char *reason=e->reason;char diagnostic_reason[64];
    /* Approved NEGATIVE-only FD3 text. Internal ledger/raw fields never change.
     * Invalid/missing categorical data falls back to the complete original
     * reason, never a dropped error or a new eligible operation. */
    if(!ev.complete && !strcmp(e->reason,"process_unavailable") && e->raw.operation &&
        !strcmp(e->raw.operation,"classification") && e->raw.error==EINVAL &&
        e->scan_index && e->scan_index<=s->history->n) {
      const struct census_history_row *row=census_history_find(&s->history->scans[e->scan_index-1],e->raw.pid);
      if(row && row->failure==CENSUS_ROW_CLASSIFICATION_FAILED && row->identity.start==e->raw.start) {
        const struct census_reject_diagnostic *d=&row->diagnostic;
        if(d->available && d->category>=1 && d->category<=4 && d->owner_present<=1 &&
            d->nonempty<=15 && d->newline<=1) {
          int n=snprintf(diagnostic_reason,sizeof diagnostic_reason,"process_unavailable:c%u:o%u:n%02x:x%u",
            d->category,d->owner_present,d->nonempty,d->newline);
          if(n>0 && (size_t)n<sizeof diagnostic_reason) reason=diagnostic_reason;
        }
      }
    }
    census_json_text(o,"{\"reason\":");census_json_string(o,reason);
    census_json_text(o,",\"operation\":");census_json_string(o,e->raw.operation);
    census_json_printf(o,",\"pid\":%u,\"errno\":%d,\"scan_index\":%u,\"resolved_by\":0,\"start_ticks\":",e->raw.pid,e->raw.error,e->scan_index);
    if(e->raw.start) census_json_printf(o,"\"%" PRIu64 "\"",e->raw.start);else census_json_text(o,"null");
    census_json_text(o,"}");
  }
  census_json_text(o,"]");
}
/* Called AFTER the entire enclosing owned payload is encoded. Fixed-width JSON
 * whitespace preserves the final allocation peak without another growth. */
static inline bool census_json_finalize(struct census_json *o) {
  if(o->failed || !o->retained_set || expired() || o->budget->fd_failed ||
      o->retained_offset>o->n || o->n-o->retained_offset<16) return false;
  int n=snprintf(o->scratch,sizeof o->scratch,"%16zu",o->budget->peak);
  if(n!=16) return false;
  memcpy(o->data+o->retained_offset,o->scratch,16);return true;
}
#endif
