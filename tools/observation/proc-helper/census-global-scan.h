/* C source integration, not production dispatch/complete evidence.
 * All sources are compiled functions. No request PID/path/role filter exists. */
#ifndef GC_CENSUS_GLOBAL_SCAN_H
#define GC_CENSUS_GLOBAL_SCAN_H
#include "census-v3-enumerate.h"
#include "census-bounded-read.h"
#include "census-scan-history.h"
#include "census-root-classification.h"
struct census_global_fault {
  unsigned scan_index;
  const char *reason;
  struct census_capture_fault raw;
};
typedef bool (*census_enumeration_source)(void *,struct census_budget *,struct census_pid_list *);
struct census_global_source {
  struct census_budget *budget;
  struct census_history *history;
  census_enumeration_source enumerate;
  census_identity_source capture;
  void *source_context;
  struct census_global_fault errors[128];
  unsigned errors_n,errors_total;
  bool truncated,fatal;
};
static inline bool census_fixed_enumeration(void *unused,struct census_budget *b,struct census_pid_list *p) {
  (void)unused;return census_pid_enumerate(b,p);
}
static inline bool census_fixed_capture(void *unused,uint32_t pid,struct census_budget *b,
    struct census_owned_identity *out,struct census_capture_fault *fault) {
  (void)unused;return census_capture_bounded_evidenced(pid,b,out,fault);
}
static inline void census_global_error(struct census_global_source *s,unsigned scan,
    const char *reason,struct census_capture_fault raw) {
  s->errors_total++;
  if(s->errors_n==128) {s->truncated=s->fatal=true;return;}
  s->errors[s->errors_n++]=(struct census_global_fault){.scan_index=scan,.reason=reason,.raw=raw};
}
static inline struct census_global_source *census_global_new(struct census_budget *budget,
    void *context,census_enumeration_source enumerate_source,census_identity_source capture_source) {
  if(expired() || !census_fd_initialize(budget)) return NULL;
  struct census_global_source *s=census_alloc(budget,sizeof *s);if(!s) return NULL;
  s->budget=budget;s->source_context=context;s->enumerate=enumerate_source;s->capture=capture_source;
  if(!s->enumerate || !s->capture || !(s->history=census_history_new(budget))) {
    census_release(budget,s,sizeof *s);return NULL;
  }
  return s;
}
static inline void census_global_free(struct census_global_source *s) {
  if(!s) return;
  struct census_budget *b=s->budget;census_history_free(b,s->history);census_release(b,s,sizeof *s);
}
static inline bool census_global_scan_record(void *context,unsigned scan_id,unsigned round,
    enum census_scan_kind kind,struct census_round_scan *out) {
  struct census_global_source *s=context;
  if(s->truncated || scan_id!=s->history->n+1) return false;
  uint64_t start=(uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u;
  struct census_pid_list pids={0};
  bool enumerated=s->enumerate(s->source_context,s->budget,&pids);
  int enum_error=errno;
  if(!enumerated) {
    if(s->history->n>=10) {s->fatal=true;census_pid_clear(s->budget,&pids);return false;}
    struct census_history_scan *failed=&s->history->scans[s->history->n++];
    failed->receipt=(struct census_round_scan){.scan_id=scan_id,.round=round,.kind=kind,
      .offset_ms=mono_ms()-start,.enumerated=pids.n,.hard_unresolved=1};
    if(pids.n) failed->rows=census_alloc(s->budget,pids.n*sizeof *failed->rows);
    if(failed->rows) {
      failed->allocated_rows=pids.n;
      for(size_t i=0;i<pids.n;i++) {
        failed->rows[i].pid=pids.p[i];failed->rows[i].failure=CENSUS_ROW_CAPTURE_FAILED;
        failed->rows[i].raw_fault=(struct census_capture_fault){.pid=pids.p[i],
          .operation="not_inspected_after_enumeration_failure",.error=ECANCELED};
      }
    } else if(pids.n) failed->allocation_failed=true;
    census_global_error(s,scan_id,"enumeration_failed",
      (struct census_capture_fault){.operation=pids.first_operation?pids.first_operation:"enumerate",.error=pids.first_error?pids.first_error:(enum_error?enum_error:EIO)});
    if(pids.later_error) census_global_error(s,scan_id,"enumeration_followup_guard",
      (struct census_capture_fault){.operation=pids.later_operation,.error=pids.later_error});
    s->fatal=s->history->failed=true;*out=failed->receipt;
    census_pid_clear(s->budget,&pids);return false;
  }
  bool captured=census_history_append_record(s->history,s->budget,pids.p,pids.n,round,kind,
    mono_ms()-start,s->source_context,s->capture);
  census_pid_clear(s->budget,&pids);
  if(!s->history->n) {s->fatal=true;return false;}
  struct census_history_scan *scan=&s->history->scans[s->history->n-1];
  if(!census_root_classify(s->history)) captured=false;
  /* Completion stamp is source-derived and committed once before publication. */
  scan->receipt.offset_ms=mono_ms()-start;
  for(size_t i=0;i<scan->allocated_rows;i++) {
    struct census_history_row *row=&scan->rows[i];
    if(row->failure==CENSUS_ROW_OK) continue;
    struct census_capture_fault raw=row->raw_fault;
    if(row->failure==CENSUS_ROW_CLASSIFICATION_FAILED && !raw.operation) raw=(struct census_capture_fault){
      .pid=row->pid,.start=row->identity.start,.operation="classification",.error=EINVAL};
    census_global_error(s,scan_id,"process_unavailable",raw);
  }
  if(expired() || s->budget->fd_failed) {
    captured=false;s->history->failed=true;
    scan->receipt.classified_all=false;scan->receipt.hard_unresolved++;
    census_global_error(s,scan_id,"resource_guard",
      (struct census_capture_fault){.operation=expired()?"deadline":"fd_budget",
        .error=expired()?ETIMEDOUT:EMFILE});
  }
  /* New positively read seal PID still requires TWO later independent closings.
   * Raw birth remains in the ledger until a separate typed resolution assembler. */
  if(captured && kind==CENSUS_SEAL && s->history->n>1) {
    struct census_history_scan *previous=&s->history->scans[s->history->n-2];size_t at=0;
    for(size_t i=0;i<scan->allocated_rows;i++) {
      uint32_t pid=scan->rows[i].pid;
      while(at<previous->allocated_rows && previous->rows[at].pid<pid) at++;
      if(at<previous->allocated_rows && previous->rows[at].pid==pid) continue;
      scan->receipt.late_birth=true;
      census_global_error(s,scan_id,"uninspected_birth",
        (struct census_capture_fault){.pid=pid,.start=scan->rows[i].identity.start,.operation="census"});
    }
    scan->receipt.seal_revalidated=true; /* Full source capture, not stat-only. */
  }
  *out=scan->receipt;
  if(!captured || s->truncated) s->fatal=true;
  /* Even true returns/selected rounds remain PENDING_PROOF: errors above are raw. */
  return captured && !s->truncated;
}
static inline bool census_global_scan(void *context,unsigned scan_id,unsigned round,
    enum census_scan_kind kind,struct census_round_scan *out) {
  struct census_global_source *s=context;
  if(s->fatal || s->history->failed) return false;
  return census_global_scan_record(context,scan_id,round,kind,out);
}
#endif
