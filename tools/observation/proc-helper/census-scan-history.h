/* Source-owned V3 history storage. Not a complete producer or retirement proof.
 * The capture callback is compiled source logic, never a request/caller field.
 * Its temporary allocations still require integration into the shared budget. */
#ifndef GC_CENSUS_SCAN_HISTORY_H
#define GC_CENSUS_SCAN_HISTORY_H
#include "census-owned-identity.h"
#include "census-round-journal.h"
#include "sha256.h"
#include <stdio.h>
#include <inttypes.h>

/* Internal V3 algorithm declarations for the forthcoming serializer freeze.
 * They do not reinterpret the V2 full-field scan.digest or add a host-equality gate.
 * All text values use length32BE+UTF8; raw PID values use canonical decimal.
 * Live fields are ordered as in census_history_identity_digest below. */
#define CENSUS_V3_RAW_HASH_DOMAIN "bounded-process-census/v3/enumerated-pids/1"
#define CENSUS_V3_LIVE_HASH_DOMAIN "bounded-process-census/v3/classified-identities/1"

enum census_row_class {CENSUS_CLASS_INVALID,CENSUS_CLASS_MANAGED,
  CENSUS_CLASS_NONMANAGED,CENSUS_CLASS_KERNEL};
enum census_row_failure {CENSUS_ROW_OK,CENSUS_ROW_CAPTURE_FAILED,
  CENSUS_ROW_CLASSIFICATION_FAILED};
struct census_history_row {
  uint32_t pid;
  enum census_row_class classification;
  enum census_row_failure failure;
  struct census_capture_fault raw_fault;
  struct census_owned_identity identity;
};
struct census_history_scan {
  struct census_round_scan receipt;
  struct census_history_row *rows;
  size_t allocated_rows;
  bool allocation_failed;
};
struct census_history {
  struct census_history_scan scans[10];
  unsigned n,errors_total;
  bool failed,truncated;
};
typedef bool (*census_identity_source)(void *,uint32_t,struct census_budget *,
                                      struct census_owned_identity *,struct census_capture_fault *);

static inline enum census_row_class census_classify_owned(const struct census_owned_identity *p) {
  if(!p->pid || !p->sid || !p->city || !p->template || !p->name ||
      !p->stat_revalidated || !p->pidfd_bound || !p->uids_revalidated) return CENSUS_CLASS_INVALID;
  bool empty=!*p->sid && !*p->city && !*p->template && !p->epoch && !*p->token;
  if(p->kernel_flags==0x00200000 && empty && !p->environment_revalidated &&
      !p->no_gc_environment && !p->declared_root) return CENSUS_CLASS_KERNEL;
  if(p->kernel_flags || !p->start || !p->pgid || !p->environment_revalidated) return CENSUS_CLASS_INVALID;
  if(empty && p->no_gc_environment && !p->declared_root) return CENSUS_CLASS_NONMANAGED;
  if(*p->sid && *p->city && *p->template && p->epoch &&
      !p->no_gc_environment && census_journal_digest(p->token)) return CENSUS_CLASS_MANAGED;
  return CENSUS_CLASS_INVALID;
}
static inline void census_history_field(sha256_ctx *h,const char *s) {
  uint32_t n=(uint32_t)strlen(s);
  unsigned char length[4]={(unsigned char)(n>>24),(unsigned char)(n>>16),
                          (unsigned char)(n>>8),(unsigned char)n};
  sha256_update(h,length,sizeof length);sha256_update(h,s,n);
}
static inline void census_history_identity_digest(sha256_ctx *h,
    const struct census_history_row *row) {
  const struct census_owned_identity *p=&row->identity;
  char fields[320];
  snprintf(fields,sizeof fields,"%u:%u:%u:%" PRIu64 ":%" PRIu64 ":%u:%u:%u:%u:%u:%d:%d:%d:%d:%d:%d:%d",
    p->pid,p->ppid,p->pgid,p->start,p->epoch,p->kernel_flags,
    p->uids[0],p->uids[1],p->uids[2],p->uids[3],row->classification,
    p->declared_root,p->stat_revalidated,p->pidfd_bound,p->uids_revalidated,
    p->environment_revalidated,p->no_gc_environment);
  census_history_field(h,fields);
  /* Only positively classified kernel comm is volatile. */
  if(row->classification!=CENSUS_CLASS_KERNEL) census_history_field(h,p->name);
  census_history_field(h,p->sid);census_history_field(h,p->city);
  census_history_field(h,p->template);census_history_field(h,p->token);
}
static inline struct census_history *census_history_new(struct census_budget *b) {
  return census_alloc(b,sizeof(struct census_history));
}
static inline void census_history_free(struct census_budget *b,struct census_history *h) {
  if(!h) return;
  for(unsigned s=0;s<h->n;s++) {
    struct census_history_scan *scan=&h->scans[s];
    for(size_t i=0;i<scan->allocated_rows;i++) census_identity_clear(b,&scan->rows[i].identity);
    census_release(b,scan->rows,scan->allocated_rows*sizeof *scan->rows);
  }
  census_release(b,h,sizeof *h);
}
/* Recording primitive used only behind a source-owned typed continuation gate.
 * The sticky failed latch and all original row errors remain immutable. */
static inline bool census_history_append_record(struct census_history *h,struct census_budget *b,
    const uint32_t *pids,size_t n,unsigned round,enum census_scan_kind kind,
    uint64_t offset_ms,void *context,census_identity_source source) {
  if(h->n>=10) return false;
  struct census_history_scan *scan=&h->scans[h->n++];
  scan->receipt=(struct census_round_scan){.scan_id=h->n,.round=round,.kind=kind,
    .offset_ms=offset_ms,.enumerated=n};
  unsigned expected_round=h->n==1?0:(h->n+1)/3;
  enum census_scan_kind expected_kind=h->n==1?CENSUS_INITIAL:
    ((h->n-1)%3==0?CENSUS_SEAL:CENSUS_CLOSING);
  if(!source || !pids || !n || n>65536 || offset_ms>=10000 ||
      (h->n>1 && offset_ms<h->scans[h->n-2].receipt.offset_ms) ||
      round!=expected_round || kind!=expected_kind || n>SIZE_MAX/sizeof *scan->rows) {
    h->failed=true;scan->receipt.hard_unresolved=1;return false;
  }
  scan->rows=census_alloc(b,n*sizeof *scan->rows);
  if(!scan->rows) {
    h->failed=true;scan->allocation_failed=true;scan->receipt.hard_unresolved=1;return false;
  }
  scan->allocated_rows=n;
  sha256_ctx raw,live;sha256_init(&raw);sha256_init(&live);
  census_history_field(&raw,CENSUS_V3_RAW_HASH_DOMAIN);
  census_history_field(&live,CENSUS_V3_LIVE_HASH_DOMAIN);
  for(size_t i=0;i<n;i++) {
    struct census_history_row *row=&scan->rows[i];row->pid=pids[i];
    char pid[16];snprintf(pid,sizeof pid,"%u",pids[i]);census_history_field(&raw,pid);
    if(!pids[i] || (i && pids[i]<=pids[i-1])) {
      row->failure=CENSUS_ROW_CLASSIFICATION_FAILED;
    } else if(!source(context,pids[i],b,&row->identity,&row->raw_fault)) {
      row->failure=CENSUS_ROW_CAPTURE_FAILED;
    } else {
      row->classification=census_classify_owned(&row->identity);
      if(row->identity.pid!=pids[i] || row->classification==CENSUS_CLASS_INVALID)
        row->failure=CENSUS_ROW_CLASSIFICATION_FAILED;
    }
    if(row->failure!=CENSUS_ROW_OK) {
      h->errors_total++;scan->receipt.hard_unresolved++;
      if(h->errors_total>128) h->truncated=true;
    } else {
      scan->receipt.classified++;census_history_identity_digest(&live,row);
    }
  }
  sha256_hex(&raw,scan->receipt.raw_digest);sha256_hex(&live,scan->receipt.live_digest);
  scan->receipt.classified_all=!scan->receipt.hard_unresolved && !h->truncated;
  h->failed=h->failed || !scan->receipt.classified_all;
  /* No kernel absence/resolution, seal or certificate authority is manufactured. */
  return scan->receipt.classified_all;
}
/* Legacy fail-stop path is unchanged. Only the typed source continuation
 * assembler may call the recording primitive after checking the whole prefix. */
static inline bool census_history_append(struct census_history *h,struct census_budget *b,
    const uint32_t *pids,size_t n,unsigned round,enum census_scan_kind kind,
    uint64_t offset_ms,void *context,census_identity_source source) {
  if(h->failed) return false;
  return census_history_append_record(h,b,pids,n,round,kind,offset_ms,context,source);
}
#endif
