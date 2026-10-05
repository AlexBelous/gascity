/* C V3 producer foundation, included after observer.c by owned-process tests.
 * Not wired into the V2 producer, no production frame or provider authority. */
#ifndef GC_CENSUS_V3_READER_H
#define GC_CENSUS_V3_READER_H
#include "census-owned-identity.h"

#include "census-bounded-read.h"

static inline bool census_capture_owned(uint32_t pid,struct census_budget *budget,
                                  struct census_owned_identity *out) {
  return census_capture_bounded(pid,budget,out);
}
static inline bool census_managed_identity(const struct census_owned_identity *p) {
  return p->pid>1 && p->pid!=ev.binding.pid && p->kernel_flags==0 &&
    p->sid && *p->sid && p->city && *p->city && p->template && *p->template &&
    p->epoch && *p->token && !infra(p->name) && p->stat_revalidated &&
    p->pidfd_bound && p->uids_revalidated && p->environment_revalidated && !p->no_gc_environment;
}
static inline bool census_same_owned(const struct census_owned_identity *a,
                              const struct census_owned_identity *b) {
  return a->pid==b->pid && a->ppid==b->ppid && a->pgid==b->pgid &&
    a->start==b->start && a->epoch==b->epoch && a->kernel_flags==b->kernel_flags &&
    !memcmp(a->uids,b->uids,sizeof a->uids) && !strcmp(a->sid,b->sid) &&
    !strcmp(a->city,b->city) && !strcmp(a->template,b->template) && !strcmp(a->token,b->token) &&
    !strcmp(a->name,b->name) && a->declared_root==b->declared_root &&
    a->stat_revalidated==b->stat_revalidated && a->pidfd_bound==b->pidfd_bound &&
    a->uids_revalidated==b->uids_revalidated && a->environment_revalidated==b->environment_revalidated &&
    a->no_gc_environment==b->no_gc_environment;
}
/* Immediate-chain discrimination only. Longer acyclic chains and provider join
 * remain separate producer work. This never creates a retirement proof. */
static inline bool census_immediate_descendant_candidate(const struct census_owned_identity *leaf,
    const struct census_owned_identity *root,const struct census_owned_identity *freshroot) {
  return census_managed_identity(leaf) && census_managed_identity(root) &&
    census_managed_identity(freshroot) && !leaf->declared_root && root->declared_root &&
    leaf->pid!=root->pid && leaf->ppid==root->pid &&
    !memcmp(leaf->uids,root->uids,sizeof leaf->uids) && leaf->epoch==root->epoch &&
    !strcmp(leaf->sid,root->sid) && !strcmp(leaf->city,root->city) &&
    !strcmp(leaf->template,root->template) && !strcmp(leaf->token,root->token) && census_same_owned(root,freshroot);
}
#endif
