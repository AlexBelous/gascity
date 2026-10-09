/* Existing enum-NULL/known nonmanaged grammar. Fresh real kernel probe only;
 * no request selector, errno adapter, protected fallback or provider authority. */
#ifndef GC_CENSUS_STANDALONE_ABSENCE_H
#define GC_CENSUS_STANDALONE_ABSENCE_H
#include "census-resolution-ledger.h"

/* A zombie remains numerically visible in /proc, but cannot be a live row.
 * Read Z from one pinned proc directory before and after pidfd_open/poll.
 * Reap/reuse invalidates the old directory rather than rebinding its stat. */
static inline bool census_zombie_stat(struct census_budget *b,int dir,uint32_t pid,uint64_t *start) {
  struct census_buffer raw={0};
  if(!census_buffer_read(b,dir,"stat",65536,&raw)) return false;
  char *end=strrchr((char *)raw.data,')');
  bool zombie=end && (size_t)(end-(char *)raw.data)+3<raw.n &&
    end[1]==' ' && end[2]=='Z' && end[3]==' ';
  struct census_owned_identity parsed={0};
  bool ok=zombie && census_parse_bounded_stat(&raw,pid,&parsed) && parsed.start;
  if(ok) *start=parsed.start;
  census_buffer_clear(b,&raw);
  return ok;
}
static inline bool census_terminal_zombie(uint32_t pid,struct census_budget *b,uint64_t *start) {
  int pin=-1,dir=-1;bool ok=false;short events=0;
  uint64_t before=0,after=0,final=0;
  if(expired() || !census_fd_initialize(b)) return false;
  char path[32];snprintf(path,sizeof path,"%u",pid);
  dir=fixed_open(ev.procfd,path,O_DIRECTORY);
  if(dir<0 || !census_fd_take(b,dir) || !census_zombie_stat(b,dir,pid,&before)) goto done;
  pin=process_pidfd(pid);
  if(pin<0 || !census_fd_take(b,pin) || !census_zombie_stat(b,dir,pid,&after) ||
      before!=after || process_poll(pin,&events)<0 || !(events&POLLIN) ||
      (events&(POLLERR|POLLNVAL)) || !census_zombie_stat(b,dir,pid,&final) ||
      after!=final || expired() || b->fd_failed) goto done;
  *start=final;ok=true;
done:
  if(pin>=0) census_fd_close(b,pin);
  if(dir>=0) census_fd_close(b,dir);
  return ok && !b->fd_failed;
}

static inline bool census_standalone_prior(const struct census_history *h,uint32_t pid,
    unsigned before,const struct census_owned_identity **known) {
  *known=NULL;
  for(unsigned scan=0;scan<before;scan++) {
    const struct census_history_row *row=census_history_find(&h->scans[scan],pid);
    if(!census_row_verified(row)) continue;
    const struct census_owned_identity *v=&row->identity;
    if(row->classification!=CENSUS_CLASS_NONMANAGED || census_classify_owned(v)!=CENSUS_CLASS_NONMANAGED || v->declared_root ||
        pid==ev.binding.pid || infra(v->name)) return false;
    if(*known && (*known)->start==v->start && !census_same_owned(*known,v)) return false;
    *known=v;
  }
  return true;
}
/* Direct known-start retirement binds the original environ fault to an
 * unchanged, fully verified NONMANAGED incarnation. Nullable pairs retain
 * their separate grammar below. */
static inline bool census_standalone_knownstart_valid(const struct census_global_source *s,
    const struct census_global_fault *e,const struct census_owned_identity **known) {
  if(!census_absence_raw_eligible(e) || !e->raw.start || !e->raw.operation ||
      strcmp(e->raw.operation,"environ") || e->raw.error!=ESRCH ||
      e->scan_index<2 || e->scan_index>s->history->n ||
      !census_standalone_prior(s->history,e->raw.pid,e->scan_index-1,known) ||
      !*known || (*known)->start!=e->raw.start) return false;
  for(unsigned scan=0;scan<s->history->n;scan++) {
    const struct census_history_row *row=census_history_find(&s->history->scans[scan],e->raw.pid);
    if(!census_row_verified(row)) continue;
    if(scan>=e->scan_index-1 || !census_same_owned(*known,&row->identity)) return false;
  }
  return true;
}
static inline bool census_standalone_history_valid(const struct census_global_source *s,
    const struct census_resolution_ledger *r) {
  for(unsigned i=0;i<r->proofs_n;i++) {
    const struct census_typed_proof *p=&r->proofs[i];bool certificate_member=false;
    for(unsigned k=0;k<r->certificates_n;k++) if(r->certificates[k].absence_proof==i+1 ||
        r->certificates[k].retirement_proof==i+1) certificate_member=true;
    if(certificate_member) continue;
    const struct proof_item *v=&p->source;const struct census_owned_identity *known=NULL;
    if(v->kind==CENSUS_UNCLASSIFIED_INCARNATION_RETIRED) {
      if(p->protected_identity || p->certificate_id || v->pid==ev.binding.pid ||
          v->scan_index<1 || (unsigned)v->scan_index>s->history->n) return false;
      /* Unknown means no earlier positively classified incarnation, including
       * a root or descendant. A later same-start verified row refutes exit;
       * a later different start is admitted only by its ordinary full capture. */
      for(unsigned scan=0;scan<s->history->n;scan++) {
        const struct census_history_row *row=census_history_find(&s->history->scans[scan],v->pid);
        if(scan<(unsigned)v->scan_index-1 && census_row_verified(row)) return false;
        if(scan>=(unsigned)v->scan_index && census_row_verified(row) &&
            row->identity.start==v->start) return false;
      }
      unsigned links=0;
      for(unsigned k=0;k<r->resolutions_n;k++) {
        if(r->resolutions[k].proof_index!=i+1) continue;
        if(!census_unclassified_link_valid(s,r,&r->resolutions[k])) return false;
        links++;
      }
      if(links!=1) return false;
      continue;
    }
    if(p->protected_identity || p->certificate_id || v->pid==ev.binding.pid ||
        v->scan_index<1 || (unsigned)v->scan_index>s->history->n ||
        v->offset_ms<s->history->scans[v->scan_index-1].receipt.offset_ms ||
        !census_standalone_prior(s->history,v->pid,(unsigned)v->scan_index-1,&known)) return false;
    unsigned matching=0;
    if(v->kind==CENSUS_TERMINAL_ZOMBIE) {
      if(known || !v->start || strcmp(v->method,"pidfd_zombie_stat")) return false;
      unsigned links=0;
      for(unsigned k=0;k<r->resolutions_n;k++) {
        const struct census_typed_resolution *link=&r->resolutions[k];
        if(link->proof_index!=i+1) continue;
        if(!link->error_index || link->error_index>s->errors_n) return false;
        const struct census_global_fault *e=&s->errors[link->error_index-1];
        if(link->kind!=CENSUS_RESOLUTION_ABSENCE || link->classified_scan || link->selected_seal ||
            e->raw.pid!=v->pid || e->raw.start || strcmp(e->raw.operation,"pidfd_poll") ||
            e->raw.error!=ESTALE || e->scan_index>(unsigned)v->scan_index) return false;
        links++;
      }
      if(links!=1) return false;
    } else if(v->kind==CENSUS_ENUMERATED_ABSENT) {
      for(unsigned k=0;k<r->proofs_n;k++) {
        const struct census_typed_proof *q=&r->proofs[k];
        if(q->source.kind==CENSUS_INCARNATION_RETIRED && q->source.pid==v->pid &&
            q->source.scan_index==v->scan_index && q->source.offset_ms==v->offset_ms &&
            !q->protected_identity && !q->certificate_id && known && q->source.start==known->start) matching++;
      }
      if(matching!=(known?1u:0u)) return false;
      unsigned links=0;
      for(unsigned k=0;k<r->resolutions_n;k++) {
        const struct census_typed_resolution *link=&r->resolutions[k];
        if(link->proof_index!=i+1) continue;
        if(!link->error_index || link->error_index>s->errors_n) return false;
        const struct census_global_fault *e=&s->errors[link->error_index-1];
        if(link->kind!=CENSUS_RESOLUTION_ABSENCE || link->classified_scan || link->selected_seal ||
            e->raw.pid!=v->pid || e->raw.start || strcmp(e->raw.operation,"pidfd_open") ||
            (e->raw.error!=ESRCH && e->raw.error!=EINVAL) || e->scan_index>(unsigned)v->scan_index) return false;
        links++;
      }
      if(links!=1) return false;
    } else {
      if(!known || v->start!=known->start) return false;
      for(unsigned k=0;k<r->proofs_n;k++) {
        const struct census_typed_proof *q=&r->proofs[k];
        if(q->source.kind==CENSUS_ENUMERATED_ABSENT && q->source.pid==v->pid &&
            q->source.scan_index==v->scan_index && q->source.offset_ms==v->offset_ms) matching++;
      }
      if(!matching) {
        unsigned links=0;
        if(v->kind!=CENSUS_INCARNATION_RETIRED || strcmp(v->method,"pidfd_no_pid")) return false;
        for(unsigned k=0;k<r->resolutions_n;k++) {
          const struct census_typed_resolution *link=&r->resolutions[k];
          if(link->proof_index!=i+1) continue;
          if(link->kind!=CENSUS_RESOLUTION_ABSENCE || link->classified_scan || link->selected_seal ||
              !link->error_index || link->error_index>s->errors_n) return false;
          const struct census_global_fault *e=&s->errors[link->error_index-1];
          const struct census_owned_identity *prior=NULL;
          if(!census_standalone_knownstart_valid(s,e,&prior) || !census_absence_proof_matches(e,v) ||
              v->offset_ms<s->history->scans[e->scan_index-1].receipt.offset_ms) return false;
          links++;
        }
        if(links!=1) return false;
      } else if(matching!=1) return false;
      for(unsigned scan=(unsigned)v->scan_index-1;scan<s->history->n;scan++) {
        const struct census_history_row *later=census_history_find(&s->history->scans[scan],v->pid);
        if(census_row_verified(later) && later->identity.start==v->start) return false;
      }
    }
  }
  return true;
}
static inline bool census_resolve_standalone(struct census_global_source *s,
    struct census_resolution_ledger *r,unsigned error_index) {
  if(r->denied || !error_index || error_index>s->errors_n || s->truncated ||
      s->errors_n!=s->errors_total || expired() || s->budget->fd_failed ||
      !ev.trusted_kernel || fixture || !ev.ns[0] || strcmp(ev.ns,ev.binding.ns) || r->resolutions_n>=128) return false;
  const struct census_global_fault *e=&s->errors[error_index-1];
  bool zombie_fault=e->raw.operation && !strcmp(e->raw.operation,"pidfd_poll") &&
    e->raw.error==ESTALE && !e->raw.start;
  bool knownstart_fault=e->raw.start && e->raw.operation && !strcmp(e->raw.operation,"environ") &&
    e->raw.error==ESRCH;
  if(!census_absence_raw_eligible(e) || !e->scan_index || e->scan_index>s->history->n ||
      (!zombie_fault && !knownstart_fault && (e->raw.start || strcmp(e->raw.operation,"pidfd_open") ||
        (e->raw.error!=ESRCH && e->raw.error!=EINVAL)))) return false;
  for(unsigned i=0;i<r->resolutions_n;i++) if(r->resolutions[i].error_index==error_index) return false;
  const struct census_owned_identity *known=NULL;
  if(!census_standalone_prior(s->history,e->raw.pid,e->scan_index-1,&known) ||
      r->proofs_n>128-((known && !knownstart_fault)?2u:1u)) return false;
  if(knownstart_fault && !census_standalone_knownstart_valid(s,e,&known)) return false;
  if(zombie_fault) {
    uint64_t zombie_start=0;
    if(known || !census_terminal_zombie(e->raw.pid,s->budget,&zombie_start)) return false;
    uint64_t start=(uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u;
    uint64_t now=mono_ms();if(now<start || now-start>=10000) return false;
    struct proof_item proof={.kind=CENSUS_TERMINAL_ZOMBIE,.method="pidfd_zombie_stat",
      .pid=e->raw.pid,.start=zombie_start,.scan_index=(int)s->history->n,.offset_ms=now-start};
    unsigned index=r->proofs_n+1;
    r->proofs[r->proofs_n++]=(struct census_typed_proof){.source=proof};
    r->resolutions[r->resolutions_n++]=(struct census_typed_resolution){.error_index=error_index,
      .proof_index=index,.kind=CENSUS_RESOLUTION_ABSENCE};
    bool valid=census_typed_proofs_valid(r) && census_standalone_history_valid(s,r);
    if(!valid) r->denied=true;
    return valid;
  }
  /* ORIGerrno only selects this guarded path; this separate syscall is proof. */
  int fd=(int)syscall(SYS_pidfd_open,e->raw.pid,0),error=errno;
  if(fd>=0) {census_fd_take(s->budget,fd);census_fd_close(s->budget,fd);return false;}
  struct census_probe probe={.pid=e->raw.pid,.start=knownstart_fault?e->raw.start:0,.syscall_errno=error,
    .trusted_kernel=ev.trusted_kernel,.same_namespace=true};
  if(census_absence(&probe)!=(knownstart_fault?CENSUS_INCARNATION_RETIRED:CENSUS_ENUMERATED_ABSENT) || expired() || s->budget->fd_failed) return false;
  uint64_t start=(uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u;
  uint64_t now=mono_ms();if(now<start || now-start>=10000) return false;
  struct proof_item absence={.kind=knownstart_fault?CENSUS_INCARNATION_RETIRED:CENSUS_ENUMERATED_ABSENT,
    .method="pidfd_no_pid",.pid=e->raw.pid,.start=knownstart_fault?e->raw.start:0,
    .scan_index=(int)s->history->n,.offset_ms=now-start};
  unsigned index=r->proofs_n+1;
  r->proofs[r->proofs_n++]=(struct census_typed_proof){.source=absence};
  if(known && !knownstart_fault) {
    struct proof_item retirement=absence;retirement.kind=CENSUS_INCARNATION_RETIRED;retirement.start=known->start;
    r->proofs[r->proofs_n++]=(struct census_typed_proof){.source=retirement};
  }
  r->resolutions[r->resolutions_n++]=(struct census_typed_resolution){.error_index=error_index,
    .proof_index=index,.kind=CENSUS_RESOLUTION_ABSENCE};
  bool valid=census_typed_proofs_valid(r) && census_standalone_history_valid(s,r);
  if(!valid) r->denied=true;
  return valid;
}
#endif
