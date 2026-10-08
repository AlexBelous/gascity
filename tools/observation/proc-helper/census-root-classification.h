/* Proc-only root classification before publication. No provider authority,
 * caller root selector or captured boolean establishes a declared root. */
#ifndef GC_CENSUS_ROOT_CLASSIFICATION_H
#define GC_CENSUS_ROOT_CLASSIFICATION_H
#include "census-scan-history.h"
static inline struct census_history_row *census_root_parent(struct census_history_scan *scan,uint32_t pid) {
  size_t lo=0,hi=scan->allocated_rows;
  while(lo<hi) {size_t mid=lo+(hi-lo)/2;
    if(scan->rows[mid].pid<pid) lo=mid+1;else hi=mid;
  }
  return lo<scan->allocated_rows && scan->rows[lo].pid==pid?&scan->rows[lo]:NULL;
}
static inline bool census_root_classify(struct census_history *h) {
  if(!h->n) return false;
  struct census_history_scan *scan=&h->scans[h->n-1];
  /* Remove ANY source callback root label before deriving from full scan. */
  for(size_t i=0;i<scan->allocated_rows;i++) scan->rows[i].identity.declared_root=false;
  for(size_t i=0;i<scan->allocated_rows;i++) {
    struct census_history_row *row=&scan->rows[i];struct census_owned_identity *p=&row->identity;
    if(row->failure!=CENSUS_ROW_OK || row->classification!=CENSUS_CLASS_MANAGED ||
        p->pid<=1 || p->pid==ev.binding.pid || infra(p->name)) continue;
    struct census_history_row *parent_row=census_root_parent(scan,p->ppid);
    bool unavailable=!parent_row || parent_row->failure!=CENSUS_ROW_OK ||
      parent_row->classification==CENSUS_CLASS_INVALID;
    /* PPID1 is still positively inspected; missing parent is never a root. */
    if(unavailable || parent_row->classification==CENSUS_CLASS_KERNEL) {
      row->failure=CENSUS_ROW_CLASSIFICATION_FAILED;
      row->raw_fault=(struct census_capture_fault){.pid=p->pid,.start=p->start,
        .operation=unavailable?"parent_unavailable":"parent_not_user_process",.error=ESTALE};
      scan->receipt.classified--;scan->receipt.hard_unresolved++;h->errors_total++;
      if(h->errors_total>128) h->truncated=true;
      continue;
    }
    const struct census_owned_identity *parent_identity=&parent_row->identity;
    /* Preserve existing root semantics: positive same-SID user parent is an
     * inherited descendant; positive different/non-GC or infrastructure parent
     * declares a proc root. FULL tuple/UID equality is separately mandatory
     * before any protected-descendant certificate can use that chain. */
    if(*parent_identity->sid && !strcmp(parent_identity->sid,p->sid) && !infra(parent_identity->name)) continue;
    p->declared_root=true;
  }
  sha256_ctx live;sha256_init(&live);census_history_field(&live,CENSUS_V3_LIVE_HASH_DOMAIN);
  for(size_t i=0;i<scan->allocated_rows;i++) if(scan->rows[i].failure==CENSUS_ROW_OK)
    census_history_identity_digest(&live,&scan->rows[i]);
  sha256_hex(&live,scan->receipt.live_digest);
  scan->receipt.classified_all=!scan->receipt.hard_unresolved && !h->truncated;
  h->failed=h->failed || !scan->receipt.classified_all;
  return scan->receipt.classified_all;
}
#endif
