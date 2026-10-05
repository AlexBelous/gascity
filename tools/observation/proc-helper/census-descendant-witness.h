/* Narrow source-owned witness boundary, not a full resolution assembler.
 * Include after observer.c + census-v3-reader.h. No caller-supplied errno or
 * proof boolean is accepted. The syscall below is the genuine flags=0 source. */
#ifndef GC_CENSUS_DESCENDANT_WITNESS_H
#define GC_CENSUS_DESCENDANT_WITNESS_H
#include "census-scan-history.h"
struct census_descendant_witness {
  struct proof_item absence,retirement;
  struct census_owned_identity *chain;
  size_t chain_n;
  bool provisional; /* Ordinary BOTH-frame provider join remains mandatory. */
};
static inline bool census_same_inherited(const struct census_owned_identity *a,
    const struct census_owned_identity *b) {
  return a->epoch==b->epoch && !memcmp(a->uids,b->uids,sizeof a->uids) &&
    !strcmp(a->sid,b->sid) && !strcmp(a->city,b->city) &&
    !strcmp(a->template,b->template) && !strcmp(a->token,b->token);
}
static inline void census_descendant_witness_clear(struct census_budget *b,
    struct census_descendant_witness *w) {
  for(size_t i=0;i<w->chain_n;i++) census_identity_clear(b,&w->chain[i]);
  census_release(b,w->chain,w->chain_n*sizeof *w->chain);memset(w,0,sizeof *w);
}
static inline bool census_descendant_no_pid(struct census_budget *budget,
    const struct census_owned_identity *prior,size_t n,
    const struct census_owned_identity *fresh_ancestors,int scan_index,
    struct census_descendant_witness *out) {
  if(out->chain || n<2 || n>128 || scan_index<2 || expired() ||
      budget->fd_failed || !ev.trusted_kernel || fixture) return false;
  const struct census_owned_identity *leaf=&prior[0];
  if(!census_managed_identity(leaf) || leaf->declared_root) return false;
  for(size_t i=0;i<n;i++) {
    const struct census_owned_identity *p=&prior[i];
    if(!census_managed_identity(p) || !census_same_inherited(leaf,p) ||
        p->declared_root!=(i==n-1)) return false;
    for(size_t k=0;k<i;k++) if(prior[k].pid==p->pid) return false;
    if(i+1<n && p->ppid!=prior[i+1].pid) return false;
    if(i && !census_same_owned(p,&fresh_ancestors[i-1])) return false;
  }
  /* Check all source identities BEFORE the real kernel probe. A permission,
   * procfs disappearance, bare callback ESRCH or test marker is not proof. */
  int fd=(int)syscall(SYS_pidfd_open,leaf->pid,0);int error=errno;
  if(fd>=0) {
    (void)census_fd_take(budget,fd);census_fd_close(budget,fd);return false;
  }
  if(error!=ESRCH || expired()) return false;
  struct census_probe probe={.pid=leaf->pid,.syscall_errno=error,
    .trusted_kernel=ev.trusted_kernel,.same_namespace=true};
  if(census_absence(&probe)!=CENSUS_ENUMERATED_ABSENT) return false;
  struct census_owned_identity *chain=census_alloc(budget,n*sizeof *chain);if(!chain) return false;
  size_t copied=0;
  for(;copied<n;copied++) if(!census_identity_copy(budget,&chain[copied],&prior[copied])) break;
  if(copied!=n || expired()) {
    for(size_t i=0;i<copied;i++) census_identity_clear(budget,&chain[i]);
    census_release(budget,chain,n*sizeof *chain);return false;
  }
  uint64_t start=(uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u;
  struct proof_item absence={.kind=CENSUS_ENUMERATED_ABSENT,.method="pidfd_no_pid",
    .pid=leaf->pid,.scan_index=scan_index,.offset_ms=mono_ms()-start};
  struct proof_item retirement=absence;retirement.kind=CENSUS_INCARNATION_RETIRED;retirement.start=leaf->start;
  *out=(struct census_descendant_witness){.absence=absence,.retirement=retirement,
    .chain=chain,.chain_n=n,.provisional=true};
  /* No global error/history is cleared, no frame COMPLETE/admission is returned. */
  return true;
}
#endif
