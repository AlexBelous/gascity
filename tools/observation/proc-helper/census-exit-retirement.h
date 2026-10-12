/* Source-owned bounded exit windows. Immutable raw faults and numeric receipts
 * remain intact. This module owns neither provider admission nor retry budgets. */
#ifndef GC_CENSUS_EXIT_RETIREMENT_H
#define GC_CENSUS_EXIT_RETIREMENT_H
static inline bool census_exit_full_prior(const struct census_global_source *s,uint32_t pid,
    unsigned through,const struct census_owned_identity **prior,unsigned *prior_scan) {
  *prior=NULL;*prior_scan=0;
  if(pid<=1 || pid==ev.binding.pid || !through || through>s->history->n) return false;
  for(unsigned scan=0;scan<s->history->n;scan++) {
    const struct census_history_row *row=census_history_find(&s->history->scans[scan],pid);
    if(!census_row_verified(row)) continue;
    const struct census_owned_identity *v=&row->identity;
    if(row->classification!=CENSUS_CLASS_NONMANAGED ||
        census_classify_owned(v)!=CENSUS_CLASS_NONMANAGED || v->declared_root || infra(v->name) ||
        !v->start || v->kernel_flags) return false;
    for(unsigned uid=0;uid<4;uid++) if(!v->uids[uid]) return false;
    if(*prior && !census_same_owned(*prior,v)) return false;
    if(scan<through) {*prior=v;*prior_scan=scan+1;}
  }
  return *prior!=NULL;
}
static inline bool census_exit_error_compatible(const struct census_global_fault *e,
    const struct census_typed_proof *p) {
  const struct census_exit_witness *w=&p->exit_witness;
  if(e->raw.pid!=p->source.pid || !e->raw.operation || !w->stage) return false;
  if(w->stage==3 && !strcmp(e->reason,"uninspected_birth"))
    return !strcmp(e->raw.operation,"census") && !e->raw.error && e->raw.start==p->source.start;
  if(strcmp(e->reason,"process_unavailable")) return false;
  if(w->stage==2) return !strcmp(e->raw.operation,"pidfd_poll") && e->raw.error==ESTALE && !e->raw.start;
  return !strcmp(e->raw.operation,"environ") && e->raw.error==ESRCH && e->raw.start==p->source.start;
}
static inline bool census_new_terminal_link_valid(const struct census_global_source *s,
    const struct census_resolution_ledger *r,const struct census_typed_resolution *link,unsigned seal) {
  if((link->kind!=CENSUS_RESOLUTION_KNOWN_TERMINAL && link->kind!=CENSUS_RESOLUTION_BOUND_BIRTH) ||
      !link->error_index || link->error_index>s->errors_n || !link->proof_index || link->proof_index>r->proofs_n ||
      !link->classified_scan || link->classified_scan>s->history->n ||
      (seal && link->selected_seal!=seal) || (!seal && link->selected_seal)) return false;
  const struct census_typed_proof *p=&r->proofs[link->proof_index-1];
  const struct census_global_fault *e=&s->errors[link->error_index-1];
  if(!census_exit_error_compatible(e,p) || p->source.scan_index<(int)e->scan_index ||
      p->source.offset_ms<s->history->scans[e->scan_index-1].receipt.offset_ms ||
      p->source.offset_ms<s->history->scans[link->classified_scan-1].receipt.offset_ms) return false;
  const struct census_history_row *prior=census_history_find(&s->history->scans[link->classified_scan-1],e->raw.pid);
  if(!census_row_verified(prior) || prior->classification!=CENSUS_CLASS_NONMANAGED ||
      prior->identity.start!=p->source.start || prior->identity.declared_root || infra(prior->identity.name)) return false;
  for(unsigned uid=0;uid<4;uid++) if(!prior->identity.uids[uid] ||
      prior->identity.uids[uid]!=p->exit_witness.uids_before[uid] ||
      prior->identity.uids[uid]!=p->exit_witness.uids_after[uid]) return false;
  if(p->exit_witness.stage==3) {
    if(link->kind==CENSUS_RESOLUTION_BOUND_BIRTH) {
      if(strcmp(e->reason,"uninspected_birth") || link->classified_scan!=e->scan_index ||
          e->scan_index<4 || s->history->scans[e->scan_index-1].receipt.kind!=CENSUS_SEAL ||
          !census_numeric_enumeration_absent(&s->history->scans[e->scan_index-2],e->raw.pid)) return false;
    } else if(link->classified_scan>=e->scan_index) return false;
  } else if(link->kind!=CENSUS_RESOLUTION_KNOWN_TERMINAL || link->classified_scan>=e->scan_index) return false;
  if(e->raw.capture_diagnostic_available && p->exit_witness.stage==1 &&
      (!e->raw.diagnostic_bound_live || e->raw.diagnostic_first_identity_present || e->raw.fields_pass!=1 ||
       e->raw.diagnostic_exit_poll!=p->exit_witness.initial_poll)) return false;
  if(seal && ((unsigned)p->source.scan_index>seal ||
      p->source.offset_ms>s->history->scans[seal-1].receipt.offset_ms ||
      ((unsigned)p->source.scan_index==seal && s->history->scans[seal-1].receipt.kind==CENSUS_SEAL))) return false;
  return true;
}
static inline bool census_exit_history_valid(const struct census_global_source *s,
    const struct census_resolution_ledger *r) {
  if(r->coverage_n>128) return false;
  bool active=r->coverage_n!=0;
  for(unsigned k=0;k<r->proofs_n;k++) if(r->proofs[k].exit_witness.stage || r->proofs[k].coverage_member) active=true;
  if(active && (!ev.trusted_kernel || fixture || !ev.ns[0] || strcmp(ev.ns,ev.binding.ns) ||
      expired() || s->budget->fd_failed)) return false;
  for(unsigned index=0;index<r->proofs_n;index++) {
    const struct census_typed_proof *p=&r->proofs[index];
    if(!p->exit_witness.stage) continue;
    const struct census_owned_identity *prior=NULL;unsigned prior_scan=0;
    if(p->source.scan_index<1 || (unsigned)p->source.scan_index>s->history->n ||
        p->source.offset_ms<s->history->scans[p->source.scan_index-1].receipt.offset_ms ||
        !census_exit_full_prior(s,p->source.pid,(unsigned)p->source.scan_index,&prior,&prior_scan) ||
        prior->start!=p->source.start) return false;
    for(unsigned scan=(unsigned)p->source.scan_index-1;scan<s->history->n;scan++) {
      const struct census_history_row *row=census_history_find(&s->history->scans[scan],p->source.pid);
      if(!census_row_verified(row)) continue;
      bool original=false;
      if(p->exit_witness.stage==3 && scan==(unsigned)p->source.scan_index-1)
        for(unsigned k=0;k<r->resolutions_n;k++)
          if(r->resolutions[k].proof_index==index+1 && r->resolutions[k].kind==CENSUS_RESOLUTION_BOUND_BIRTH &&
              r->resolutions[k].classified_scan==scan+1 && row->identity.start==p->source.start) original=true;
      if(!original) return false;
    }
    unsigned links=0,births=0,envs=0;
    for(unsigned k=0;k<r->resolutions_n;k++) if(r->resolutions[k].proof_index==index+1) {
      const struct census_typed_resolution *link=&r->resolutions[k];
      if(!census_new_terminal_link_valid(s,r,link,link->selected_seal)) return false;
      links++;if(link->kind==CENSUS_RESOLUTION_BOUND_BIRTH) births++;else envs++;
    }
    if(p->exit_witness.stage==3 ? (births!=1 || envs>1 || links!=births+envs) : links!=1) return false;
  }
  bool coverage_used[128]={0};
  for(unsigned k=0;k<r->coverage_n;k++) {
    const struct census_coverage_retirement *v=&r->coverage[k];
    if(!v->prior_scan || !v->missing_scan || v->prior_scan>=v->missing_scan || v->missing_scan>s->history->n ||
        !v->absence_proof || !v->retirement_proof || v->absence_proof>=v->retirement_proof ||
        v->retirement_proof>r->proofs_n || coverage_used[v->absence_proof-1] || coverage_used[v->retirement_proof-1]) return false;
    const struct census_typed_proof *a=&r->proofs[v->absence_proof-1],*p=&r->proofs[v->retirement_proof-1];
    const struct census_owned_identity *prior=NULL;unsigned prior_scan=0;
    if(!a->coverage_member || !p->coverage_member || a->source.kind!=CENSUS_ENUMERATED_ABSENT ||
        p->source.kind!=CENSUS_INCARNATION_RETIRED || a->source.pid!=v->pid || p->source.pid!=v->pid ||
        p->source.start!=v->start || a->source.scan_index!=(int)v->missing_scan || p->source.scan_index!=(int)v->missing_scan ||
        a->source.offset_ms!=p->source.offset_ms ||
        p->source.offset_ms<s->history->scans[v->missing_scan-1].receipt.offset_ms ||
        !census_numeric_enumeration_absent(&s->history->scans[v->missing_scan-1],v->pid) ||
        !census_exit_full_prior(s,v->pid,v->prior_scan,&prior,&prior_scan) || prior_scan!=v->prior_scan || prior->start!=v->start)
      return false;
    for(unsigned scan=v->missing_scan-1;scan<s->history->n;scan++)
      if(census_history_find(&s->history->scans[scan],v->pid)) return false;
    for(unsigned e=0;e<s->errors_n;e++) if(s->errors[e].raw.pid==v->pid) return false;
    if(v->selected_seal && (v->selected_seal!=s->history->n || p->source.scan_index>(int)v->selected_seal ||
        p->source.offset_ms>s->history->scans[v->selected_seal-1].receipt.offset_ms ||
        ((unsigned)p->source.scan_index==v->selected_seal && s->history->scans[v->selected_seal-1].receipt.kind==CENSUS_SEAL))) return false;
    coverage_used[v->absence_proof-1]=coverage_used[v->retirement_proof-1]=true;
  }
  for(unsigned index=0;index<r->proofs_n;index++) if(r->proofs[index].coverage_member!=coverage_used[index]) return false;
  return true;
}
static inline bool census_exit_original_scope(const struct census_global_source *s) {
  return census_recorded_prefix_valid(s) && !s->truncated && s->errors_n==s->errors_total && !expired() && !s->budget->fd_failed &&
    ev.trusted_kernel && !fixture && ev.ns[0] && !strcmp(ev.ns,ev.binding.ns);
}
/* Deferred re-observation is charged to the current scan/frame, never sleeps,
 * adds no scan slot and never substitutes a fresh pidfd for original custody. */
static inline bool census_resolve_exit_windows(struct census_global_source *s,
    struct census_resolution_ledger *r) {
  if(r->denied) return false;
  if(!census_exit_original_scope(s)) return true;
  for(unsigned slot=0;slot<s->errors_n;slot++) {
    struct census_global_fault *e=&s->errors[slot];struct census_capture_fault *custody=&e->raw;
    const struct census_owned_identity *prior=NULL;unsigned prior_scan=0,birth=0;
    bool done=false;for(unsigned k=0;k<r->resolutions_n;k++) if(r->resolutions[k].error_index==slot+1) done=true;
    if(done) continue;
    unsigned stage=custody->retained_stage;
    if(!strcmp(e->reason,"uninspected_birth") && e->scan_index<=s->history->n) {
      struct census_history_row *row=census_history_find(&s->history->scans[e->scan_index-1],e->raw.pid);
      if(row && row->raw_fault.retained_stage==3) {custody=&row->raw_fault;stage=3;birth=slot+1;}
    }
    if(!stage || stage>3 || !custody->retained_pidfd_plus_one || !custody->retained_procdir_plus_one) continue;
    unsigned through=stage==3?e->scan_index:e->scan_index-1;
    if(!census_exit_full_prior(s,e->raw.pid,through,&prior,&prior_scan) || prior->start!=custody->retained_start) continue;
    if(stage==3 && (prior_scan!=e->scan_index || !birth)) continue;
    uint32_t observed[4]={0};int fd=(int)custody->retained_pidfd_plus_one-1,dir=(int)custody->retained_procdir_plus_one-1;
    bool terminal=stage==3?census_exit_original_full_zombie(s->budget,fd,dir,prior,observed):
      census_exit_original_zombie(s->budget,fd,dir,e->raw.pid,prior->start,prior->uids,observed);
    if(!terminal) continue;
    unsigned compatible=0;
    if(stage==3) for(unsigned other=0;other<s->errors_n;other++) {
      const struct census_global_fault *f=&s->errors[other];
      if(other==slot || f->raw.pid!=e->raw.pid) continue;
      if(strcmp(f->reason,"process_unavailable") || !f->raw.operation || strcmp(f->raw.operation,"environ") ||
          f->raw.error!=ESRCH || f->raw.start!=prior->start || f->scan_index<=e->scan_index) {compatible=129;break;}
      compatible++;
    }
    if(compatible>1 || r->proofs_n>=128 || r->resolutions_n+1+compatible>128) continue;
    uint64_t origin=(uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u;
    uint64_t now=mono_ms();if(now<origin || now-origin>=10000) continue;
    struct census_typed_proof proof={.source={.kind=CENSUS_TERMINAL_ZOMBIE,.method="pidfd_zombie_stat",
      .pid=e->raw.pid,.start=prior->start,.scan_index=(int)s->history->n,.offset_ms=now-origin},
      .exit_witness={.stage=stage,.initial_poll=stage==2?CENSUS_EXIT_POLL_VALID_POLLIN:
        stage==3?CENSUS_EXIT_POLL_ZERO:e->raw.diagnostic_exit_poll,.bound_start=prior->start,.full_identity_revalidated=stage==3}};
    memcpy(proof.exit_witness.uids_before,observed,sizeof observed);memcpy(proof.exit_witness.uids_after,observed,sizeof observed);
    unsigned proof_index=r->proofs_n+1;r->proofs[r->proofs_n++]=proof;
    r->resolutions[r->resolutions_n++]=(struct census_typed_resolution){.error_index=slot+1,.proof_index=proof_index,
      .classified_scan=prior_scan,.kind=stage==3?CENSUS_RESOLUTION_BOUND_BIRTH:CENSUS_RESOLUTION_KNOWN_TERMINAL};
    if(stage==3) for(unsigned other=0;other<s->errors_n;other++) if(other!=slot && s->errors[other].raw.pid==e->raw.pid) {
      r->resolutions[r->resolutions_n++]=(struct census_typed_resolution){.error_index=other+1,.proof_index=proof_index,
        .classified_scan=prior_scan,.kind=CENSUS_RESOLUTION_KNOWN_TERMINAL};
      census_capture_custody_close(s->budget,&s->errors[other].raw);
    }
    census_capture_custody_close(s->budget,custody);
    if(!census_typed_proofs_valid(r) || !census_exit_history_valid(s,r)) {r->denied=true;return false;}
  }
  return true;
}
static inline bool census_collect_coverage_retirements(struct census_global_source *s,
    struct census_resolution_ledger *r) {
  if(r->denied) return false;
  if(!census_exit_original_scope(s) || s->history->n<2) return true;
  unsigned missing=s->history->n;
  const struct census_history_scan *last=&s->history->scans[missing-1];
  if(!census_numeric_enumeration_valid(last)) return true;
  for(unsigned scan=0;scan<missing-1;scan++) for(size_t row=0;row<s->history->scans[scan].allocated_rows;row++) {
    const struct census_history_row *old=&s->history->scans[scan].rows[row];
    if(expired() || s->budget->fd_failed) return false;
    if(!census_row_verified(old) || census_history_find(last,old->pid)) continue;
    bool used=false;
    for(unsigned k=0;k<r->proofs_n;k++) if(r->proofs[k].source.pid==old->pid) used=true;
    for(unsigned k=0;k<s->errors_n;k++) if(s->errors[k].raw.pid==old->pid) used=true;
    if(used) continue;
    const struct census_owned_identity *prior=NULL;unsigned prior_scan=0;
    if(!census_exit_full_prior(s,old->pid,missing-1,&prior,&prior_scan) || r->proofs_n>126 || r->coverage_n>=128) continue;
    int pin=process_pidfd(old->pid),error=errno;
    if(pin>=0) {if(!census_fd_take(s->budget,pin)) {census_fd_close(s->budget,pin);return false;}census_fd_close(s->budget,pin);continue;}
    if(error!=ESRCH || !census_exit_original_scope(s)) continue;
    uint64_t origin=(uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u;
    uint64_t now=mono_ms();if(now<origin || now-origin>=10000) continue;
    struct proof_item absence={.kind=CENSUS_ENUMERATED_ABSENT,.method="pidfd_no_pid",.pid=old->pid,
      .scan_index=(int)missing,.offset_ms=now-origin};
    unsigned a=r->proofs_n+1;r->proofs[r->proofs_n++]=(struct census_typed_proof){.source=absence,.coverage_member=true};
    absence.kind=CENSUS_INCARNATION_RETIRED;absence.start=prior->start;
    unsigned p=r->proofs_n+1;r->proofs[r->proofs_n++]=(struct census_typed_proof){.source=absence,.coverage_member=true};
    r->coverage[r->coverage_n++]=(struct census_coverage_retirement){.prior_scan=prior_scan,.missing_scan=missing,
      .absence_proof=a,.retirement_proof=p,.pid=old->pid,.start=prior->start};
    if(!census_typed_proofs_valid(r) || !census_exit_history_valid(s,r)) {r->denied=true;return false;}
  }
  return true;
}
static inline bool census_exit_pending(const struct census_global_source *s,unsigned error_index) {
  if(!census_exit_original_scope(s) || s->history->n>=10 || !error_index || error_index>s->errors_n) return false;
  const struct census_global_fault *e=&s->errors[error_index-1];
  const struct census_owned_identity *prior=NULL;unsigned prior_scan=0;
  if(!e->raw.retained_stage || e->raw.retained_stage>2 || !e->raw.retained_pidfd_plus_one || !e->raw.retained_procdir_plus_one ||
      !census_exit_full_prior(s,e->raw.pid,e->scan_index-1,&prior,&prior_scan) || prior->start!=e->raw.retained_start) return false;
  return true;
}
#endif
