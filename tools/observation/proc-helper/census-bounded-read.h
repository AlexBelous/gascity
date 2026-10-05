/* Include after observer.c. Source-owned fixed directory/file reads only.
 * Every heap buffer, including overlapping growth allocations, is charged. */
#ifndef GC_CENSUS_BOUNDED_READ_H
#define GC_CENSUS_BOUNDED_READ_H
#include "census-owned-identity.h"
#include "census-fd-meter.h"
struct census_buffer {unsigned char *data;size_t n,capacity;};
#ifdef GC_HELPER_TEST
/* Owned-child scheduling only. This does not inject errno or a kernel proof;
 * production source contains no callback or request-selectable scheduling. */
static void (*census_test_before_status)(uint32_t);
#endif
static inline void census_buffer_clear(struct census_budget *budget,struct census_buffer *b) {
  if(b->data) {volatile unsigned char *wipe=b->data;for(size_t i=0;i<b->capacity;i++) wipe[i]=0;}
  census_release(budget,b->data,b->capacity);memset(b,0,sizeof *b);
}
static inline bool census_buffer_read(struct census_budget *budget,int dir,const char *leaf,
    size_t limit,struct census_buffer *out) {
  if(limit==SIZE_MAX || out->data) {errno=EINVAL;return false;}
  if(budget->fd_failed) {errno=EMFILE;return false;}
  if(expired()) {errno=ETIMEDOUT;return false;}
  errno=0;
  int fd=fixed_open(dir,leaf,0);if(fd<0) return false;
  if(!census_fd_take(budget,fd)) {census_fd_close(budget,fd);errno=EMFILE;return false;}
  struct census_buffer b={.capacity=limit+1<4096?limit+1:4096};
  b.data=census_alloc(budget,b.capacity);bool ok=false;
  if(!b.data) {errno=ENOMEM;goto done;}
  for(;;) {
    if(expired()) {errno=ETIMEDOUT;break;}
    if(b.n==b.capacity) {
      size_t cap=b.capacity> (limit+1)/2?limit+1:b.capacity*2;
      if(cap<=b.capacity) {errno=EFBIG;break;}
      unsigned char *fresh=census_alloc(budget,cap);
      if(!fresh) {errno=ENOMEM;break;}
      memcpy(fresh,b.data,b.n);
      struct census_buffer old=b;b.data=fresh;b.capacity=cap;
      census_buffer_clear(budget,&old);
    }
    ssize_t got=read(fd,b.data+b.n,b.capacity-b.n);
    if(got<0) {if(errno==EINTR) continue;break;}
    if(!got) {b.data[b.n]=0;ok=true;break;}
    b.n+=(size_t)got;
    if(b.n>limit) {errno=EFBIG;break;}
  }
done:
  {int error=errno;census_fd_close(budget,fd);errno=error;}
  if(!ok) census_buffer_clear(budget,&b);else *out=b;
  return ok;
}
/* Destructive parsing of the already charged stat buffer: no hidden malloc.
 * A zero start/PGID is allowed only with positively read PF_KTHREAD. */
static inline bool census_parse_bounded_stat(struct census_buffer *b,uint32_t pid,
    struct census_owned_identity *out) {
  if(memchr(b->data,0,b->n)) return false;
  char *text=(char *)b->data,*open=strchr(text,'('),*end=strrchr(text,')');
  if(!open || !end || end<=open || open==text || open[-1]!=' ') return false;
  open[-1]=0;uint64_t read_pid;if(!uint_value(text,&read_pid) || read_pid!=pid) return false;
  uint64_t ppid=0,pgid=0,flags=0,start=0;
  char *save=NULL,*field=strtok_r(end+1," \n",&save);unsigned index=3;
  while(field) {
    uint64_t *value=index==4?&ppid:index==5?&pgid:index==9?&flags:index==22?&start:NULL;
    if(value && !uint_value(field,value)) return false;
    field=strtok_r(NULL," \n",&save);index++;
  }
  uint32_t kernel=(uint32_t)(flags&PF_KTHREAD);
  if(index<23 || ppid>UINT32_MAX || pgid>UINT32_MAX || (!kernel && (!start || !pgid))) return false;
  out->pid=pid;out->ppid=(uint32_t)ppid;out->pgid=(uint32_t)pgid;
  out->start=start;out->kernel_flags=kernel;return true;
}
static inline bool census_read_bounded_stat(struct census_budget *budget,int dir,uint32_t pid,
    struct census_owned_identity *out) {
  struct census_buffer b={0};if(!census_buffer_read(budget,dir,"stat",65536,&b)) return false;
  bool ok=census_parse_bounded_stat(&b,pid,out);census_buffer_clear(budget,&b);
  if(!ok) errno=EINVAL;
  return ok;
}
static inline bool census_read_bounded_uids(struct census_budget *budget,int dir,uint32_t out[4]) {
  struct census_buffer b={0};if(!census_buffer_read(budget,dir,"status",65536,&b)) return false;
  bool found=false,ok=!memchr(b.data,0,b.n);
  for(char *line=(char *)b.data;ok && line && *line;) {
    char *next=strchr(line,'\n');if(next) *next++=0;
    if(!strncmp(line,"Uid:",4)) {
      if(found || !census_parse_uids(line,out)) {ok=false;break;}
      found=true;
    }
    line=next;
  }
  census_buffer_clear(budget,&b);
  if(!ok || !found) errno=EINVAL;
  return ok && found;
}
static inline bool census_read_bounded_environment(struct census_budget *budget,int dir,
    struct census_owned_identity *out) {
  struct census_buffer b={0};if(!census_buffer_read(budget,dir,"environ",MAX_ENV,&b)) return false;
  bool ok=!b.n || !b.data[b.n-1],has_gc=false;
  errno=EINVAL;
  char **seen=NULL,*fallback=NULL;size_t seen_n=0;
  if(!ok) goto done;
  for(size_t at=0;at<b.n;) {
    if(expired()) {errno=ETIMEDOUT;ok=false;goto done;}
    size_t len=strlen((char *)b.data+at);
    if(len>=3 && !memcmp(b.data+at,"GC_",3)) has_gc=true;
    at+=len+1;
  }
  /* City/fallback values are retained context, not session ownership. Any exact
   * SID/template/epoch/token key, including empty, keeps partial ownership invalid. */
  out->no_gc_environment=true;out->environment_revalidated=true;
  if(!has_gc) goto empty;
  seen=census_alloc(budget,4096*sizeof *seen);if(!seen) {errno=ENOMEM;ok=false;goto done;}
  for(size_t at=0;at<b.n;) {
    if(expired()) {errno=ETIMEDOUT;ok=false;goto done;}
    char *entry=(char *)b.data+at;size_t len=strlen(entry);at+=len+1;
    if(!len) continue;
    char *eq=memchr(entry,'=',len);if(!eq || eq==entry) {ok=false;goto done;}
    *eq=0;if(strncmp(entry,"GC_",3)) continue;
    if(seen_n==4096) {errno=EFBIG;ok=false;goto done;}
    for(size_t i=0;i<seen_n;i++) if(!strcmp(seen[i],entry)) {ok=false;goto done;}
    seen[seen_n++]=entry;char **dst=NULL;
    if(!strcmp(entry,"GC_SESSION_ID")) dst=&out->sid;
    else if(!strcmp(entry,"GC_CITY_PATH")) dst=&out->city;
    else if(!strcmp(entry,"GC_CITY")) dst=&fallback;
    else if(!strcmp(entry,"GC_TEMPLATE")) dst=&out->template;
    else if(!strcmp(entry,"GC_RUNTIME_EPOCH")) {
      out->no_gc_environment=false;
      if(!uint_value(eq+1,&out->epoch) || out->epoch>INT32_MAX) {ok=false;goto done;}
    } else if(!strcmp(entry,"GC_INSTANCE_TOKEN")) {
      out->no_gc_environment=false;
      if(eq[1]) sha256_sum(eq+1,strlen(eq+1),out->token);
    }
    if(dst) {
      if(dst==&out->sid || dst==&out->template) out->no_gc_environment=false;
      if(!utf8((unsigned char *)eq+1,strlen(eq+1))) {ok=false;goto done;}
      if(strlen(eq+1)>4096) {errno=EFBIG;ok=false;goto done;}
      if(!(*dst=census_string_copy(budget,eq+1))) {errno=ENOMEM;ok=false;goto done;}
    }
  }
  if((!out->city || !*out->city) && fallback) {
    if(out->city) census_release(budget,out->city,strlen(out->city)+1);
    out->city=fallback;fallback=NULL;
  }
empty:
  if(!out->sid) out->sid=census_string_copy(budget,"");
  if(!out->city) out->city=census_string_copy(budget,"");
  if(!out->template) out->template=census_string_copy(budget,"");
  ok=out->sid && out->city && out->template;
  if(!ok) errno=ENOMEM;
done:
  if(fallback) census_release(budget,fallback,strlen(fallback)+1);
  census_release(budget,seen,4096*sizeof *seen);census_buffer_clear(budget,&b);
  if(ok) errno=0;
  return ok;
}
static inline bool census_bounded_fields(struct census_budget *budget,int dir,uint32_t pid,
    struct census_owned_identity *out,struct census_capture_fault *fault) {
  fault->operation="stat";
  if(!census_read_bounded_stat(budget,dir,pid,out)) return false;
  fault->start=out->start;fault->operation="status";
#ifdef GC_HELPER_TEST
  if(census_test_before_status) census_test_before_status(pid);
#endif
  if(!census_read_bounded_uids(budget,dir,out->uids)) return false;
  fault->operation="environ";
  if(!out->kernel_flags) {
    if(!census_read_bounded_environment(budget,dir,out)) return false;
  } else {
    out->sid=census_string_copy(budget,"");out->city=census_string_copy(budget,"");
    out->template=census_string_copy(budget,"");
    if(!out->sid || !out->city || !out->template) return false;
  }
  fault->operation="comm";
  struct census_buffer comm={0};if(!census_buffer_read(budget,dir,"comm",MAX_FIELD+1,&comm)) return false;
  if(comm.n && comm.data[comm.n-1]=='\n') comm.data[--comm.n]=0;
  bool ok=!memchr(comm.data,0,comm.n) && utf8(comm.data,comm.n);
  if(ok) out->name=census_string_copy(budget,(char *)comm.data);
  if(!ok) errno=EINVAL;
  else if(!out->name) errno=comm.n>MAX_FIELD?EFBIG:ENOMEM;
  census_buffer_clear(budget,&comm);return ok && out->name;
}
static inline bool census_equal_bounded(const struct census_owned_identity *a,
    const struct census_owned_identity *b) {
  return a->pid==b->pid && a->ppid==b->ppid && a->pgid==b->pgid && a->start==b->start &&
    a->kernel_flags==b->kernel_flags && a->epoch==b->epoch && !memcmp(a->uids,b->uids,sizeof a->uids) &&
    a->no_gc_environment==b->no_gc_environment && a->environment_revalidated==b->environment_revalidated &&
    !strcmp(a->sid,b->sid) && !strcmp(a->city,b->city) && !strcmp(a->template,b->template) &&
    !strcmp(a->token,b->token) && (a->kernel_flags==PF_KTHREAD || !strcmp(a->name,b->name));
}
static inline bool census_capture_bounded_evidenced(uint32_t pid,struct census_budget *budget,
    struct census_owned_identity *out,struct census_capture_fault *fault) {
  *fault=(struct census_capture_fault){.pid=pid,.operation="deadline"};
  if(expired()) {fault->error=errno=ETIMEDOUT;return false;}
  fault->operation="fd_inventory";
  if(!census_fd_initialize(budget)) {fault->error=errno?errno:EMFILE;return false;}
  fault->operation="pidfd_open";
  int pin=process_pidfd(pid);if(pin<0) {fault->error=errno;return false;}
  if(!census_fd_take(budget,pin)) {
    census_fd_close(budget,pin);fault->operation="fd_budget";fault->error=errno=EMFILE;return false;
  }
  char path[32];snprintf(path,sizeof path,"%u",pid);
  int dir=fixed_open(ev.procfd,path,O_DIRECTORY);bool ok=false;short events=0;
  struct census_owned_identity a={0},b={0},last={0};
  fault->operation="proc_directory";
  if(dir<0) goto done;
  fault->operation="fd_budget";
  if(!census_fd_take(budget,dir)) {errno=EMFILE;goto done;}
  fault->operation="pidfd_poll";
  if(process_poll(pin,&events)!=0 || events) goto done;
  if(!census_bounded_fields(budget,dir,pid,&a,fault) ||
     !census_bounded_fields(budget,dir,pid,&b,fault)) goto done;
  fault->operation="identity_comparison";fault->start=a.start;
  if(!census_equal_bounded(&a,&b)) {errno=ESTALE;goto done;}
  fault->operation="final_stat";
  if(!census_read_bounded_stat(budget,dir,pid,&last)) goto done;
  if(last.start!=a.start || last.ppid!=a.ppid || last.pgid!=a.pgid || last.kernel_flags!=a.kernel_flags) {errno=ESTALE;goto done;}
  fault->operation="pidfd_poll";
  events=0;if(process_poll(pin,&events)!=0 || events) goto done;
  fault->operation="deadline";
  if(expired()) {errno=ETIMEDOUT;goto done;}
  fault->operation="fd_budget";
  if(budget->fd_failed) {errno=EMFILE;goto done;}
  b.stat_revalidated=b.pidfd_bound=b.uids_revalidated=true;
  *out=b;memset(&b,0,sizeof b);ok=true;
done:
  {int error=errno;census_identity_clear(budget,&a);census_identity_clear(budget,&b);
    census_fd_close(budget,dir);
    census_fd_close(budget,pin);
    if(ok && budget->fd_failed) {
      census_identity_clear(budget,out);memset(out,0,sizeof *out);ok=false;error=EBADF;fault->operation="close";
    }
    errno=ok?0:(error?error:ESTALE);}
  if(ok) memset(fault,0,sizeof *fault);else fault->error=errno;
  return ok;
}
static inline bool census_capture_bounded(uint32_t pid,struct census_budget *budget,
    struct census_owned_identity *out) {
  struct census_capture_fault fault={0};
  return census_capture_bounded_evidenced(pid,budget,out,&fault);
}
#endif
