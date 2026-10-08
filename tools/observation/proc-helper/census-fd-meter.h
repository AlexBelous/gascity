/* Source-only own-helper descriptor census. Include after observer.c.
 * Production bootstrap/other producer operations still must use this meter. */
#ifndef GC_CENSUS_FD_METER_H
#define GC_CENSUS_FD_METER_H
#include "census-owned-identity.h"
static inline bool census_fd_initialize(struct census_budget *budget) {
  if(budget->fd_failed) return false;
  if(budget->fd_tracking) return true;
  char path[48];snprintf(path,sizeof path,"%u/fd",(unsigned)getpid());
  int dir=fixed_open(ev.procfd,path,O_DIRECTORY);if(dir<0) {budget->fd_failed=true;return false;}
  unsigned char *buffer=census_alloc(budget,4096);unsigned count=0;bool ok=buffer!=NULL;
  while(ok) {
    if(expired()) {ok=false;break;}
    int n=(int)syscall(SYS_getdents64,dir,buffer,4096);
    if(n<0) {ok=false;break;}if(!n) break;
    for(int at=0;at<n;) {
      struct linux_dirent64 *entry=(void *)(buffer+at);
      if(entry->reclen<offsetof(struct linux_dirent64,name)+1 || at+entry->reclen>n ||
          !memchr(entry->name,0,entry->reclen - offsetof(struct linux_dirent64,name))) {ok=false;break;}
      uint64_t fd;
      if(uint_value(entry->name,&fd)) {if(fd>INT32_MAX || ++count>32) {ok=false;break;}}
      at+=entry->reclen;
    }
  }
  census_release(budget,buffer,4096);close(dir);
  if(!ok || !count) {budget->fd_failed=true;return false;}
  /* Inventory included its own descriptor, which is now closed. Its peak counts. */
  budget->fd_live=count-1;budget->fd_peak=count;budget->fd_tracking=true;return true;
}
static inline bool census_fd_take(struct census_budget *budget,int fd) {
  if(fd<0) return false;
  if(!budget->fd_tracking) return !budget->fd_failed;
  budget->fd_live++;
  if(budget->fd_live>budget->fd_peak) budget->fd_peak=budget->fd_live;
  if(budget->fd_live>32) budget->fd_failed=true;
  return !budget->fd_failed;
}
static inline void census_fd_close(struct census_budget *budget,int fd) {
  if(fd<0) return;
  if(budget->fd_tracking) {
    if(!budget->fd_live) budget->fd_failed=true;else budget->fd_live--;
  }
  if(close(fd)<0) budget->fd_failed=true;
}
#endif
