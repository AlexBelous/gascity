/* Full typed V3 serialization, source-only until startup/dispatch integration.
 * Every buffer shares the source budget. Process completeness is provisional;
 * ordinary gc alone performs the BOTH-frame provider join. No fallback V2. */
#ifndef GC_CENSUS_ENVELOPE_H
#define GC_CENSUS_ENVELOPE_H
#include "census-json.h"
struct census_envelope {
  struct census_json *json;
  size_t duration_offset,finished_offset;
  bool complete;
};
static inline void census_envelope_free(struct census_budget *b,struct census_envelope *e) {
  if(!e) return;
  census_json_free(e->json);census_release(b,e,sizeof *e);
}
static inline void census_envelope_root(struct census_json *o,const struct census_history_scan *scan,
    const struct census_owned_identity *v) {
  struct census_history_row *parent_row=census_history_find(scan,v->ppid);
  const char *parent_name=census_row_verified(parent_row)?parent_row->identity.name:"";
  census_json_printf(o,"{\"pid\":%u,\"ppid\":%u,\"pgid\":%u,\"start_ticks\":\"%" PRIu64 "\",\"epoch\":%" PRIu64 ",\"session_id\":",v->pid,v->ppid,v->pgid,v->start,v->epoch);
  census_json_string(o,v->sid);census_json_text(o,",\"city\":");census_json_string(o,v->city);
  census_json_text(o,",\"template\":");census_json_string(o,v->template);
  census_json_text(o,",\"instance_token_sha256\":");census_json_string(o,v->token);
  census_json_text(o,",\"name\":");census_json_string(o,v->name);
  census_json_printf(o,",\"parent_is_provider_infrastructure\":%s,\"parent_name\":",infra(parent_name)?"true":"false");
  census_json_string(o,parent_name);census_json_text(o,"}");
}
/* ev.complete may only remain true after the production startup AND closing
 * caller/boot/namespace/kernel fences. This codec cannot establish those fences.
 * A failed final resolution always takes UNKNOWN, never an earlier good round. */
static inline struct census_envelope *census_envelope_new(struct census_global_source *s,
    const struct census_resolution_ledger *r,const struct census_round_journal *j) {
  struct census_envelope *e=census_alloc(s->budget,sizeof *e);if(!e) return NULL;
  e->json=census_json_new(s->budget);if(!e->json) {census_envelope_free(s->budget,e);return NULL;}
  struct census_json *o=e->json;
  e->complete=ev.complete && ev.trusted_kernel && !s->truncated &&
    census_final_resolutions_valid(s,r,j);
  const struct census_history_scan *initial=s->history->n?&s->history->scans[0]:NULL;
  const struct census_history_scan *closing=e->complete?&s->history->scans[j->selected_second-1]:
    s->history->n?&s->history->scans[s->history->n-1]:NULL;
  census_json_text(o,"{\"schema\":\"host-process-evidence/v3\",\"scope\":");
#ifdef GC_HELPER_TEST
  /* Owned-source fixtures never assert global host coverage, even when their
   * pidfd witnesses are genuine. The production reader rejects this scope. */
  census_json_string(o,"fixture_procfs");
#else
  census_json_string(o,"host_procfs");
#endif
  census_json_text(o,",\"request_nonce\":");census_json_string(o,ev.nonce);
  census_json_printf(o,",\"helper_source_revision\":\"%s\",\"helper_binary_sha256\":\"%s\",\"policy_digest\":\"%s\",\"boot_id\":",HELPER_SOURCE_REVISION,ev.binary,ev.policy);
  census_json_string(o,ev.boot);census_json_text(o,",\"pid_namespace_identity\":");census_json_string(o,ev.ns);
  census_json_text(o,",\"started_at\":");census_json_string(o,ev.started);
  census_json_text(o,",\"finished_at\":\"");e->finished_offset=o->n;
  census_json_text(o,"0000-00-00T00:00:00.000000000Z\",\"duration_ms\":");e->duration_offset=o->n;
  census_json_text(o,"               0");
  census_json_printf(o,",\"complete\":%s,\"enumerated_count_before\":%zu,\"enumerated_count_after\":%zu,\"enumeration_digest_before\":\"%s\",\"enumeration_digest_after\":\"%s\",\"caller_binding\":{\"pid\":%u,\"uid\":%u,\"start_ticks\":\"%" PRIu64 "\",\"boot_id\":\"%s\",\"controller_source_revision\":\"%s\",\"controller_binary_sha256\":\"%s\"},\"roots\":[",
    e->complete?"true":"false",initial?initial->receipt.enumerated:0,closing?closing->receipt.enumerated:0,
    initial?initial->receipt.raw_digest:"",closing?closing->receipt.raw_digest:"",
    ev.binding.pid,ev.binding.uid,ev.binding.start,ev.binding.boot,ev.binding.source,ev.binding.binary);
  if(e->complete) {
    const struct census_history_scan *seal=&s->history->scans[j->selected_seal-1];bool comma=false;
    for(size_t i=0;i<seal->allocated_rows;i++) {
      const struct census_history_row *row=&seal->rows[i];const struct census_owned_identity *v=&row->identity;
      if(!census_row_verified(row) || row->classification!=CENSUS_CLASS_MANAGED || !v->declared_root ||
          v->pid<=1 || v->pid==ev.binding.pid || infra(v->name)) continue;
      if(comma) census_json_text(o,",");
      comma=true;census_envelope_root(o,seal,v);
    }
  }
  census_json_text(o,"],\"errors\":");census_json_errors(o,s);
  census_json_printf(o,",\"errors_total\":%u,\"errors_truncated\":%s,\"kernel_release\":",s->errors_total,s->truncated?"true":"false");
  census_json_string(o,ev.binding.kernel_release);census_json_text(o,",\"kernel_proof_profile\":");census_json_string(o,ev.binding.proof_profile);
  census_json_text(o,",\"certificate_disposition\":\"provisional\",\"census\":");
  if(e->complete) census_json_census(o,s,r,j);else census_json_unknown_census(o,s,r);
  census_json_text(o,"}");return e;
}
/* Final timestamps/duration/retained peak include all envelope allocation and
 * encoding. A failed resource/time fence prevents sending any COMPLETE bytes. */
static inline bool census_envelope_finalize(struct census_envelope *e) {
  struct timespec finished;uint64_t start=(uint64_t)ev.monotonic_start.tv_sec*1000u+
    (uint64_t)ev.monotonic_start.tv_nsec/1000000u;
  uint64_t now=mono_ms();
  if(now<start || now-start>=10000 || clock_gettime(CLOCK_REALTIME,&finished) ||
      finished.tv_sec<ev.realtime_start.tv_sec ||
      (finished.tv_sec==ev.realtime_start.tv_sec && finished.tv_nsec<ev.realtime_start.tv_nsec) ||
      !census_json_finalize(e->json)) return false;
  ev.duration=now-start;timestamp(finished,ev.finished);
  if(strlen(ev.finished)!=30 || e->finished_offset>e->json->n || e->json->n-e->finished_offset<30 ||
      e->duration_offset>e->json->n || e->json->n-e->duration_offset<16) return false;
  memcpy(e->json->data+e->finished_offset,ev.finished,30);
  int n=snprintf(e->json->scratch,sizeof e->json->scratch,"%16" PRIu64,ev.duration);
  if(n!=16) return false;
  memcpy(e->json->data+e->duration_offset,e->json->scratch,16);return true;
}
#endif
