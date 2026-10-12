/* M/R: original descriptor custody is separate from legacy same-scan faults.
 * No raw reason, diagnostic, scan receipt or budget is rewritten. */
#ifndef GC_CENSUS_ORIGINAL_RETIREMENT_H
#define GC_CENSUS_ORIGINAL_RETIREMENT_H
static inline bool census_original_link_valid(const struct census_global_source *s,
    const struct census_resolution_ledger *r,const struct census_typed_resolution *v,unsigned seal) {
  const struct census_original_capture *o=&v->original;
  if(!census_original_kind(v->kind) || !o->present || !v->error_index || v->error_index>s->errors_n ||
      !v->proof_index || v->proof_index>r->proofs_n || !v->classified_scan ||
      v->classified_scan>s->history->n || !o->absence_proof || o->absence_proof>=v->proof_index ||
      !o->capture_scan || o->capture_scan>s->history->n || o->missing_scan<=o->capture_scan ||
      o->missing_scan>s->history->n || (seal && v->selected_seal!=seal) || (!seal && v->selected_seal) ||
      !o->pidfd_ino || !o->procdir_ino || !o->capture_handle || !o->pidfd_generation || !o->procdir_generation || o->binding_offset_ms> s->history->scans[o->capture_scan-1].receipt.offset_ms ||
      o->terminal_offset_ms<s->history->scans[o->missing_scan-1].receipt.offset_ms ||
      o->terminal_offset_ms>=10000 || !census_numeric_enumeration_absent(&s->history->scans[o->missing_scan-1],o->pid) ||
      !ev.trusted_kernel || fixture || !ev.ns[0] || strcmp(ev.ns,ev.binding.ns) || expired() || s->budget->fd_failed) return false;
  const struct census_global_fault *e=&s->errors[v->error_index-1];
  const struct census_typed_proof *p=&r->proofs[v->proof_index-1],*a=&r->proofs[o->absence_proof-1];
  const struct census_history_row *prior=census_history_find(&s->history->scans[v->classified_scan-1],o->pid);
  if(!census_row_verified(prior) || e->raw.pid!=o->pid || e->raw.start!=o->start ||
      prior->identity.start!=o->start || prior->identity.declared_root || infra(prior->identity.name) ||
      o->pid<=1 || o->pid==ev.binding.pid || e->scan_index!=o->capture_scan ||
      p->source.pid!=o->pid || p->source.start!=o->start || p->source.kind!=CENSUS_INCARNATION_RETIRED ||
      a->source.pid!=o->pid || a->source.start || a->source.kind!=CENSUS_ENUMERATED_ABSENT ||
      strcmp(p->source.method,"pidfd_no_pid") || strcmp(a->source.method,"pidfd_no_pid") ||
      (v->kind==CENSUS_RESOLUTION_MANAGED_BIRTH ? p->source.scan_index>(int)o->missing_scan : p->source.scan_index!=(int)o->missing_scan) || a->source.scan_index!=p->source.scan_index ||
      a->source.offset_ms!=p->source.offset_ms || (v->kind!=CENSUS_RESOLUTION_MANAGED_BIRTH && p->source.offset_ms<o->terminal_offset_ms) ||
      p->exit_witness.stage || a->exit_witness.stage || a->protected_identity || a->certificate_id ||
      p->coverage_member || a->coverage_member) return false;
  for(unsigned uid=0;uid<4;uid++) if(!o->uids[uid] || o->uids[uid]!=prior->identity.uids[uid]) return false;
  bool managed=v->kind!=CENSUS_RESOLUTION_REAPED_BIRTH;
  if(p->protected_identity!=managed || (managed ? !o->certificate_id || p->certificate_id!=o->certificate_id :
      o->certificate_id || p->certificate_id) || prior->classification!=(managed?CENSUS_CLASS_MANAGED:CENSUS_CLASS_NONMANAGED)) return false;
  if(v->kind==CENSUS_RESOLUTION_DEFERRED_MANAGED) {
    if(strcmp(e->reason,"process_unavailable") || !e->raw.operation || strcmp(e->raw.operation,"environ") ||
        e->raw.error!=ESRCH || v->classified_scan>=e->scan_index || !census_birth_companion_diagnostic(&e->raw) || !o->comm_verified) return false;
  } else {
    if(o->comm_verified) return false;
    if(strcmp(e->reason,"uninspected_birth") || !e->raw.operation || strcmp(e->raw.operation,"census") ||
        e->raw.error || v->classified_scan!=e->scan_index || e->scan_index<4 ||
        s->history->scans[e->scan_index-1].receipt.kind!=CENSUS_SEAL ||
        !census_numeric_enumeration_absent(&s->history->scans[e->scan_index-2],o->pid)) return false;
  }
  for(unsigned scan=0;scan<s->history->n;scan++) {
    const struct census_history_row *row=census_history_find(&s->history->scans[scan],o->pid);
    if(scan>=o->missing_scan-1) {
      if(row || !census_numeric_enumeration_absent(&s->history->scans[scan],o->pid)) return false;
    } else if(census_row_verified(row) && (!census_same_owned(&prior->identity,&row->identity) ||
        row->classification!=prior->classification)) return false;
  }
  if(managed) {
    if(o->certificate_id>r->certificates_n) return false;
    const struct census_typed_certificate *c=&r->certificates[o->certificate_id-1];
    if(c->retirement_proof!=v->proof_index || c->absence_proof!=o->absence_proof ||
        !c->witness.chain || c->witness.chain_n<2 ||
        !census_same_owned(&prior->identity,&c->witness.chain[0])) return false;
    for(size_t i=1;i<c->witness.chain_n;i++) for(unsigned scan=o->capture_scan-1;scan<s->history->n;scan++) {
      const struct census_history_row *row=census_history_find(&s->history->scans[scan],c->witness.chain[i].pid);
      if(!census_row_verified(row) || !census_same_owned(&row->identity,&c->witness.chain[i])) return false;
    }
  }
  if(seal && (o->missing_scan>=seal || o->terminal_offset_ms>s->history->scans[seal-1].receipt.offset_ms || p->source.offset_ms>s->history->scans[seal-1].receipt.offset_ms)) return false;
  return true;
}
/* Chain values are borrowed from ONE FULL prior receipt; output allocation is
 * charged by its caller, and the real protected creator deep-copies them. */
static inline size_t census_original_managed_chain(const struct census_global_source *s,
    unsigned prior_scan,unsigned fault_scan,uint32_t pid,struct census_owned_identity *chain) {
  if(!prior_scan || prior_scan> s->history->n || fault_scan<prior_scan || fault_scan>s->history->n) return 0;
  const struct census_history_scan *prior=&s->history->scans[prior_scan-1];
  const struct census_history_row *row=census_history_find(prior,pid);size_t n=0;
  while(row && census_row_verified(row) && row->classification==CENSUS_CLASS_MANAGED && n<128) {
    const struct census_owned_identity *v=&row->identity;
    if(!census_managed_identity(v) || (n && !census_same_inherited(&chain[0],v)) ||
        (!n && (v->declared_root || infra(v->name) || v->pid==ev.binding.pid))) return 0;
    for(unsigned uid=0;uid<4;uid++) if(!v->uids[uid]) return 0;
    for(size_t k=0;k<n;k++) if(chain[k].pid==v->pid) return 0;
    chain[n++]=*v;
    if(v->declared_root) break;
    row=census_history_find(prior,v->ppid);
  }
  if(n<2 || !chain[n-1].declared_root) return 0;
  for(size_t i=0;i<n;i++) for(unsigned scan=prior_scan;scan<s->history->n;scan++) {
    const struct census_history_row *fresh=census_history_find(&s->history->scans[scan],chain[i].pid);
    if(!i && scan+1>=fault_scan) {
      if(census_row_verified(fresh)) return 0;
    } else if(!census_row_verified(fresh) || !census_same_owned(&chain[i],&fresh->identity)) return 0;
  }
  return n;
}
static inline bool census_original_pending(const struct census_global_source *s,unsigned index) {
  if(!index || index>s->errors_n || s->history->n>=10 || !census_exit_original_scope(s)) return false;
  const struct census_global_fault *e=&s->errors[index-1];
  if(!e->raw.retained_binding_verified || e->raw.retained_stage!=1 || !e->raw.retained_pidfd_plus_one ||
      !e->raw.retained_procdir_plus_one || !census_birth_companion_diagnostic(&e->raw)) return false;
  struct census_owned_identity *chain=census_alloc(s->budget,128*sizeof *chain);if(!chain) return false;
  size_t n=census_original_managed_chain(s,e->scan_index-1,e->scan_index,e->raw.pid,chain);
  bool ok=n && chain[0].start==e->raw.retained_start;
  census_release(s->budget,chain,128*sizeof *chain);return ok;
}
/* A successful open is NEVER an absence proof. Even a failing take charged
 * the already-opened descriptor; close it exactly once before refusing. */
static inline bool census_original_no_pid_probe(struct census_budget *b,uint32_t pid) {
 int fd=process_pidfd(pid),error=errno;
 if(fd>=0) {bool taken=census_fd_take(b,fd);census_fd_close(b,fd);if(!taken) b->fd_failed=true;return false;}
 return error==ESRCH && !expired() && !b->fd_failed;
}
static inline bool census_collect_original_retirements(struct census_global_source *s,
    struct census_resolution_ledger *r) {
  if(r->denied || !census_exit_original_scope(s)) return false;
  for(unsigned slot=0;slot<s->errors_n;slot++) {
    struct census_global_fault *e=&s->errors[slot];bool resolved=false;
    for(unsigned k=0;k<r->resolutions_n;k++) if(r->resolutions[k].error_index==slot+1) resolved=true;
    bool birth=!strcmp(e->reason,"uninspected_birth");
    struct census_capture_fault *custody=&e->raw;
    if(birth) {struct census_history_row *original=census_history_find(&s->history->scans[e->scan_index-1],e->raw.pid);
      if(!census_row_verified(original)) continue;
      custody=&original->raw_fault;}
    if(resolved || !custody->retained_binding_verified || !custody->retained_pidfd_plus_one ||
        !custody->retained_procdir_plus_one || s->history->n<=e->scan_index || r->resolutions_n>=128) continue;
    unsigned prior_scan=birth?e->scan_index:e->scan_index-1;
    const struct census_history_row *prior=census_history_find(&s->history->scans[prior_scan-1],e->raw.pid);
    if(!census_row_verified(prior) || prior->identity.start!=custody->retained_start ||
        (birth?custody->retained_stage!=3:custody->retained_stage!=1) ||
        memcmp(prior->identity.uids,custody->retained_uids,sizeof custody->retained_uids) ||
        !census_numeric_enumeration_absent(&s->history->scans[s->history->n-1],e->raw.pid) ||
        !census_original_reaped(s->budget,custody,e->raw.pid,prior->identity.start)) continue;
    struct census_original_capture o={.present=true,.pid=e->raw.pid,.start=prior->identity.start,
      .capture_scan=e->scan_index,.missing_scan=s->history->n,
      .binding_offset_ms=custody->retained_binding_offset_ms,.pidfd_dev=custody->retained_pidfd_dev,
      .pidfd_ino=custody->retained_pidfd_ino,.procdir_dev=custody->retained_dir_dev,.procdir_ino=custody->retained_dir_ino};
    const struct census_capture_owner *owner=census_capture_owner_find(s->budget,custody->retained_capture_handle);
    if(!owner || owner->released) continue;
    o.capture_handle=owner->handle;o.pidfd_generation=owner->pidfd_generation;o.procdir_generation=owner->procdir_generation;
    o.comm_verified=custody->retained_comm_verified;
    memcpy(o.uids,prior->identity.uids,sizeof o.uids);
    uint64_t origin=(uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u;
    uint64_t now=mono_ms();if(now<origin || now-origin>=10000) continue;o.terminal_offset_ms=now-origin;
    unsigned a=0,p=0,certificate=0;struct census_descendant_witness witness={0};
    if(prior->classification==CENSUS_CLASS_MANAGED) {
      if(birth) {
        for(unsigned k=0;k<r->certificates_n;k++) {
          const struct census_typed_certificate *c=&r->certificates[k];
          if(c->witness.retirement.pid==o.pid && c->witness.retirement.start==o.start &&
              c->witness.retirement.scan_index<=(int)o.missing_scan &&
              census_same_owned(&prior->identity,&c->witness.chain[0])) {
            a=c->absence_proof;p=c->retirement_proof;certificate=c->id;
          }
        }
        if(!certificate) continue;
        /* The already indexed legacy certificate probe may precede this
         * separate custody observation, so obtain a fresh positive probe and
         * never retime the existing proof. This link's own terminal time must
         * still precede the real selected seal. */
        if(!census_original_no_pid_probe(s->budget,o.pid) || !census_original_reaped(s->budget,custody,e->raw.pid,prior->identity.start)) continue;
        now=mono_ms();if(now<origin || now-origin>=10000) continue;
        o.terminal_offset_ms=now-origin;
      } else {
        if(!census_birth_companion_diagnostic(&e->raw) || r->certificates_n>=64 || r->proofs_n>126) continue;
        struct census_owned_identity *chain=census_alloc(s->budget,128*sizeof *chain);if(!chain) return false;
        size_t n=census_original_managed_chain(s,prior_scan,e->scan_index,e->raw.pid,chain);
        bool ok=n && census_descendant_no_pid_checked_chain(s->budget,chain,n,(int)s->history->n,&witness);
        census_release(s->budget,chain,128*sizeof *chain);if(!ok) continue;
        if(!census_original_reaped(s->budget,custody,e->raw.pid,prior->identity.start)) {census_descendant_witness_clear(s->budget,&witness);continue;}
        a=r->proofs_n+1;p=a+1;certificate=r->certificates_n+1;
        r->certificates[r->certificates_n++]=(struct census_typed_certificate){.id=certificate,.prior_scan=prior_scan,
          .absence_proof=a,.retirement_proof=p,.witness=witness};
        r->proofs[r->proofs_n++]=(struct census_typed_proof){.source=witness.absence};
        r->proofs[r->proofs_n++]=(struct census_typed_proof){.source=witness.retirement,.protected_identity=true,.certificate_id=certificate};
      }
    } else {
      const struct census_owned_identity *known=NULL;unsigned classified=0;
      if(!birth || !census_exit_full_prior(s,o.pid,prior_scan,&known,&classified) ||
          classified!=prior_scan || r->proofs_n>126) continue;
      if(!census_original_no_pid_probe(s->budget,o.pid) || !census_exit_original_scope(s) || !census_original_reaped(s->budget,custody,e->raw.pid,prior->identity.start)) continue;
      now=mono_ms();if(now<origin || now-origin>=10000) continue;
      struct proof_item proof={.kind=CENSUS_ENUMERATED_ABSENT,.method="pidfd_no_pid",.pid=o.pid,
        .scan_index=(int)o.missing_scan,.offset_ms=now-origin};
      a=r->proofs_n+1;r->proofs[r->proofs_n++]=(struct census_typed_proof){.source=proof};
      proof.kind=CENSUS_INCARNATION_RETIRED;proof.start=o.start;
      p=r->proofs_n+1;r->proofs[r->proofs_n++]=(struct census_typed_proof){.source=proof};
    }
    o.absence_proof=a;o.certificate_id=certificate;
    struct census_typed_resolution v={.error_index=slot+1,.proof_index=p,.classified_scan=prior_scan,.original=o,
      .kind=certificate?(birth?CENSUS_RESOLUTION_MANAGED_BIRTH:CENSUS_RESOLUTION_DEFERRED_MANAGED):CENSUS_RESOLUTION_REAPED_BIRTH};
    /* A pre-existing certificate has its own independent time; don't change it.
     * The relation is timestamped by custody and its fresh probe. */
    r->resolutions[r->resolutions_n++]=v;
    if(!census_original_link_valid(s,r,&r->resolutions[r->resolutions_n-1],0) || !census_typed_proofs_valid(r)) {
      r->denied=true;return false;
    }
    census_capture_custody_close(s->budget,custody);
  }
  return true;
}
#endif
