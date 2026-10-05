/* V3 certificate custody building block. Not wired into the V2 producer.
 * No syscalls, borrowed strings, process authority or retirement permission. */
#ifndef GC_CENSUS_OWNED_IDENTITY_H
#define GC_CENSUS_OWNED_IDENTITY_H
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <limits.h>
#include <errno.h>

struct census_budget {
  size_t used, limit, peak;
  unsigned fd_live,fd_peak;
  bool fd_tracking,fd_failed;
};
struct census_owned_identity {
	uint32_t kernel_flags;
	bool no_gc_environment, environment_revalidated;
  uint32_t pid, ppid, pgid, uids[4];
  uint64_t start, epoch;
  char *sid, *city, *template, *name;
  char token[65];
  bool declared_root, stat_revalidated, pidfd_bound, uids_revalidated;
};
/* Raw source failure metadata, never a kernel absence certificate. Operation
 * points only to a compiled string literal, not a borrowed file/environment. */
struct census_capture_fault {
  const char *operation;
  uint32_t pid;
  uint64_t start;
  int error;
};

static inline void *census_alloc(struct census_budget *budget,size_t n) {
  if (!n || n>budget->limit || budget->used>budget->limit-n) {errno=ENOMEM;return NULL;}
  void *p=calloc(1,n);
  if(p) {budget->used+=n;if(budget->used>budget->peak) budget->peak=budget->used;}
  return p;
}
static inline void census_release(struct census_budget *budget,void *p,size_t n) {
  if (!p) return;
  /* Accounting corruption is fail-closed, never unsigned wrap into headroom. */
  if (n>budget->used) {budget->used=budget->limit;free(p);return;}
  budget->used-=n;free(p);
}
static inline char *census_string_copy(struct census_budget *budget,const char *s) {
  if (!s) return NULL;
  size_t n=strnlen(s,4097);if (n>4096) return NULL;
  char *p=census_alloc(budget,n+1);if(p) memcpy(p,s,n+1);return p;
}
static inline void census_identity_clear(struct census_budget *budget,struct census_owned_identity *v) {
  char **fields[]={&v->sid,&v->city,&v->template,&v->name};
  for(size_t i=0;i<4;i++) if (*fields[i]) {
    census_release(budget,*fields[i],strlen(*fields[i])+1);*fields[i]=NULL;
  }
}
static inline bool census_identity_copy(struct census_budget *budget,
                                 struct census_owned_identity *out,
                                 const struct census_owned_identity *in) {
  /* Caller first charges the destination array/container itself. */
  struct census_owned_identity copy=*in;
  copy.sid=copy.city=copy.template=copy.name=NULL;
  copy.sid=census_string_copy(budget,in->sid);
  copy.city=census_string_copy(budget,in->city);
  copy.template=census_string_copy(budget,in->template);
  copy.name=census_string_copy(budget,in->name);
  if(!copy.sid || !copy.city || !copy.template || !copy.name) {
    census_identity_clear(budget,&copy);return false;
  }
  *out=copy;return true;
}
/* Strict /proc/PID/status Uid line parsing; no labels or caller-supplied boolean
 * establish a UID. Producer must read it inside before/after stat+pidfd fences. */
static inline bool census_parse_uids(const char *line,uint32_t out[4]) {
  if (!line || strncmp(line,"Uid:",4)) return false;
  const char *p=line+4;
  for(size_t i=0;i<4;i++) {
    while(*p==' ' || *p=='\t') p++;
    if(*p<'0' || *p>'9') return false;
    uint64_t n=0;
    do {n=n*10+(unsigned)(*p-'0');if(n>UINT32_MAX) return false;p++;} while(*p>='0' && *p<='9');
    out[i]=(uint32_t)n;
    if(i<3 && *p!=' ' && *p!='\t') return false;
  }
  while(*p==' ' || *p=='\t') p++;
  if(*p=='\n') p++;
  return *p==0;
}
#endif
