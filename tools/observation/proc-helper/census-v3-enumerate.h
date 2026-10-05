/* Source-owned numeric enumeration with actual aggregate array charging.
 * Include after observer.c. No caller path/PID filter or managed-role claims. */
#ifndef GC_CENSUS_V3_ENUMERATE_H
#define GC_CENSUS_V3_ENUMERATE_H
#include "census-owned-identity.h"
#include "census-fd-meter.h"
struct census_pid_list {
  uint32_t *p;size_t n,capacity;
  int first_error,later_error;
  const char *first_operation,*later_operation;
};
static inline void census_pid_error(struct census_pid_list *list,const char *operation,int error) {
  if(!error) error=EIO;
  if(!list->first_error) {list->first_error=error;list->first_operation=operation;}
  else if(!list->later_error) {list->later_error=error;list->later_operation=operation;}
}
#ifdef GC_HELPER_TEST
static int (*census_test_getdents)(int,void *,size_t);
#endif
static inline int census_pid_getdents(int fd,void *buffer,size_t n) {
#ifdef GC_HELPER_TEST
  if(census_test_getdents) return census_test_getdents(fd,buffer,n);
#endif
  return (int)syscall(SYS_getdents64,fd,buffer,n);
}
static inline void census_pid_sift(uint32_t *p,size_t n,size_t root) {
  while(root<n/2) {
    size_t child=root*2+1;
    if(child+1<n && p[child]<p[child+1]) child++;
    if(p[root]>=p[child]) break;
    uint32_t swap=p[root];p[root]=p[child];p[child]=swap;root=child;
  }
}
static inline bool census_pid_sort(struct census_pid_list *list) {
  /* In-place heap sort: no libc qsort scratch outside the shared budget. */
  for(size_t i=list->n/2;i>0;i--) {
    if(expired()) {errno=ETIMEDOUT;return false;}
    census_pid_sift(list->p,list->n,i-1);
  }
  for(size_t n=list->n;n>1;) {
    if(expired()) {errno=ETIMEDOUT;return false;}
    uint32_t swap=list->p[0];list->p[0]=list->p[--n];list->p[n]=swap;
    census_pid_sift(list->p,n,0);
  }
  return true;
}
static void census_pid_clear(struct census_budget *budget,struct census_pid_list *list) {
  census_release(budget,list->p,list->capacity*sizeof *list->p);
  memset(list,0,sizeof *list);
}
static bool census_pid_append(struct census_budget *budget,struct census_pid_list *list,uint32_t pid) {
  if(list->n>=MAX_PIDS) {errno=EFBIG;return false;}
  if(list->n==list->capacity) {
    size_t capacity=list->capacity?list->capacity*2:128;
    if(capacity>MAX_PIDS) capacity=MAX_PIDS;
    if(capacity<=list->capacity || capacity>SIZE_MAX/sizeof *list->p) return false;
    uint32_t *fresh=census_alloc(budget,capacity*sizeof *fresh);
    if(!fresh) return false;
    if(list->p) memcpy(fresh,list->p,list->n*sizeof *fresh);
    census_release(budget,list->p,list->capacity*sizeof *list->p);
    list->p=fresh;list->capacity=capacity;
  }
  list->p[list->n++]=pid;return true;
}
static bool census_pid_enumerate(struct census_budget *budget,struct census_pid_list *list) {
  if(budget->fd_failed) {errno=EMFILE;return false;}
  int directory=fixed_open(ev.procfd,".",O_DIRECTORY);if(directory<0) return false;
  if(!census_fd_take(budget,directory)) {census_fd_close(budget,directory);errno=EMFILE;return false;}
  unsigned char *buf=census_alloc(budget,32768);bool ok=buf!=NULL;
  while(ok) {
    if(expired()) {errno=ETIMEDOUT;census_pid_error(list,"deadline",errno);ok=false;break;}
    int n=census_pid_getdents(directory,buf,32768);
    if(n<0) {census_pid_error(list,"getdents64",errno);ok=false;break;}if(!n) break;
    for(int at=0;at<n;) {
      struct linux_dirent64 *entry=(void *)(buf+at);
      if(entry->reclen<offsetof(struct linux_dirent64,name)+1 || at+entry->reclen>n ||
        !memchr(entry->name,0,entry->reclen - offsetof(struct linux_dirent64,name))) {errno=EBADMSG;ok=false;break;}
      uint64_t pid;
      if(uint_value(entry->name,&pid) && pid>0) {
        if(pid>INT32_MAX) {errno=EINVAL;ok=false;break;}
        if(!census_pid_append(budget,list,(uint32_t)pid)) {ok=false;break;}
      }
      at+=entry->reclen;
    }
  }
  if(!ok && !list->first_error) census_pid_error(list,"enumerate",errno);
  int error=errno;census_release(budget,buf,32768);census_fd_close(budget,directory);errno=error;
  if(list->n>1 && !census_pid_sort(list)) {census_pid_error(list,"sort_deadline",errno);ok=false;}
  if(list->n==MAX_PIDS) {errno=EFBIG;ok=false;}
  for(size_t i=1;i<list->n;i++) if(list->p[i-1]==list->p[i]) {errno=EBADMSG;ok=false;}
  if(budget->fd_failed) {errno=EMFILE;ok=false;}
  if(!ok) {if(!list->first_error) census_pid_error(list,"enumerate",errno);errno=list->first_error;}
  return ok;
}
#endif
