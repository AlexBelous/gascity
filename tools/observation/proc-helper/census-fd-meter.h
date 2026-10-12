/* Source-only own-helper descriptor census. Include after observer.c.
 * Production bootstrap/other producer operations still must use this meter. */
#ifndef GC_CENSUS_FD_METER_H
#define GC_CENSUS_FD_METER_H
#include "census-owned-identity.h"
static inline struct census_fd_owner *census_fd_owner_find(struct census_budget *b,int fd) {
  for(unsigned i=0;i<32;i++) if(b->fd_owners[i].active && b->fd_owners[i].fd==fd) return &b->fd_owners[i];
  return NULL;
}
static inline bool census_fd_register(struct census_budget *b,int fd) {
  struct census_fd_owner *old=census_fd_owner_find(b,fd);
  if((old && old->owned) || b->fd_generation==UINT64_MAX) {b->fd_failed=true;return false;}
  /* Initial inherited/control descriptors are inventoried, not owned captures.
   * A newly opened descriptor may reuse a borrowed number; retained owned
   * descriptors can never be replaced through this path. */
  if(old) old->active=false;
  for(unsigned i=0;i<32;i++) if(!b->fd_owners[i].active) {
    b->fd_owners[i]=(struct census_fd_owner){.fd=fd,.generation=++b->fd_generation,.active=true,.owned=true};return true;
  }
  b->fd_failed=true;return false;
}
static inline bool census_fd_owned_generation(struct census_budget *b,int fd,uint64_t generation) {
  const struct census_fd_owner *owner=census_fd_owner_find(b,fd);
  return owner && owner->owned && generation && owner->generation==generation;
}
static inline struct census_capture_owner *census_capture_owner_find(struct census_budget *b,uint64_t handle) {
  if(!handle) return NULL;
  for(unsigned i=0;i<32;i++) if(b->capture_owners[i].handle==handle) return &b->capture_owners[i];
  return NULL;
}
static inline uint64_t census_capture_register(struct census_budget *b,int pin,int dir,uint32_t pid,uint64_t start) {
  const struct census_fd_owner *p=census_fd_owner_find(b,pin),*d=census_fd_owner_find(b,dir);
  if(!p || !d || !p->owned || !d->owned || !start || pid<=1 || b->capture_generation==UINT64_MAX || b->fd_failed) return 0;
  for(unsigned i=0;i<32;i++) if(!b->capture_owners[i].handle || b->capture_owners[i].released) {
    b->capture_owners[i]=(struct census_capture_owner){.handle=++b->capture_generation,.pidfd=pin,.procdir=dir,
      .pidfd_generation=p->generation,.procdir_generation=d->generation,.pid=pid,.start=start};
    return b->capture_owners[i].handle;
  }
  return 0;
}
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
      if(uint_value(entry->name,&fd)) {
        if(fd>INT32_MAX || ++count>32 || ((int)fd!=dir && !census_fd_register(budget,(int)fd))) {ok=false;break;}
        if((int)fd!=dir) census_fd_owner_find(budget,(int)fd)->owned=false;
      }
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
  bool registered=census_fd_register(budget,fd);
  if(budget->fd_live>budget->fd_peak) budget->fd_peak=budget->fd_live;
  if(budget->fd_live>32) budget->fd_failed=true;
  return registered && !budget->fd_failed;
}
static inline void census_fd_close(struct census_budget *budget,int fd) {
  if(fd<0) return;
  if(budget->fd_tracking) {
    struct census_fd_owner *owner=census_fd_owner_find(budget,fd);
    if(owner) owner->active=false;else budget->fd_failed=true;
    if(!budget->fd_live) budget->fd_failed=true;else budget->fd_live--;
  }
  if(close(fd)<0) budget->fd_failed=true;
}
/* The record's descriptor numbers/generations are immutable. Mutable raw-fault
 * aliases cannot select another descriptor to release or prove retirement. */
static inline void census_capture_owner_release(struct census_budget *b,uint64_t handle) {
  struct census_capture_owner *owner=census_capture_owner_find(b,handle);
  if(!owner || owner->released) return;
  if(census_fd_owned_generation(b,owner->pidfd,owner->pidfd_generation)) census_fd_close(b,owner->pidfd);
  if(census_fd_owned_generation(b,owner->procdir,owner->procdir_generation)) census_fd_close(b,owner->procdir);
  owner->released=true;
}
#endif
