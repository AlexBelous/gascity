/* Include after bounded stat/UID/buffer readers. Original fd, no rebind to a
 * different descriptor. These checks establish custody, not a ledger proof. */
#ifndef GC_CENSUS_EXIT_OBSERVATION_H
#define GC_CENSUS_EXIT_OBSERVATION_H
static inline bool census_exit_fdinfo_pid(struct census_budget *b,int fd,uint32_t pid) {
  if(fd<0 || pid<=1 || expired() || b->fd_failed) return false;
  char path[80];snprintf(path,sizeof path,"%u/fdinfo/%u",(unsigned)getpid(),(unsigned)fd);
  struct census_buffer raw={0};
  if(!census_buffer_read(b,ev.procfd,path,4096,&raw)) return false;
  bool found=false,ok=!memchr(raw.data,0,raw.n);
  for(char *line=(char *)raw.data;ok && line && *line;) {
    char *next=strchr(line,'\n');if(next) *next++=0;
    if(!strncmp(line,"Pid:",4)) {
      char *value=line+4;while(*value==' ' || *value=='\t') value++;
      uint64_t actual=0;
      if(found || !uint_value(value,&actual) || actual!=pid) ok=false;
      found=true;
    }
    line=next;
  }
  census_buffer_clear(b,&raw);
  return ok && found && !expired() && !b->fd_failed;
}
static inline bool census_exit_zstat(struct census_budget *b,int dir,uint32_t pid,
    uint64_t expected_start) {
  struct census_buffer raw={0};
  if(!census_buffer_read(b,dir,"stat",65536,&raw)) return false;
  char *end=strrchr((char *)raw.data,')');
  struct census_owned_identity stat={0};
  bool ok=end && (size_t)(end-(char *)raw.data)+3<raw.n && end[1]==' ' &&
    end[2]=='Z' && end[3]==' ' && census_parse_bounded_stat(&raw,pid,&stat) &&
    stat.start==expected_start && !stat.kernel_flags;
  census_buffer_clear(b,&raw);return ok;
}
static inline bool census_exit_original_zombie(struct census_budget *b,int fd,int dir,
    uint32_t pid,uint64_t start,const uint32_t prior_uids[4],uint32_t observed[4]) {
  if(fd<0 || dir<0 || !start || !prior_uids || expired() || b->fd_failed ||
      !ev.trusted_kernel || fixture || !ev.ns[0] || strcmp(ev.ns,ev.binding.ns)) return false;
  uint32_t first[4],last[4];short flags=0;
  if(!census_exit_fdinfo_pid(b,fd,pid) || !census_exit_zstat(b,dir,pid,start) ||
      !census_read_bounded_uids(b,dir,first) || process_poll(fd,&flags)!=1 ||
      !(flags&POLLIN) || (flags&~(POLLIN|POLLHUP)) ||
      !census_exit_zstat(b,dir,pid,start) || !census_read_bounded_uids(b,dir,last) ||
      !census_exit_fdinfo_pid(b,fd,pid) || !census_exit_zstat(b,dir,pid,start)) return false;
  for(unsigned i=0;i<4;i++)
    if(!first[i] || first[i]!=last[i] || last[i]!=prior_uids[i]) return false;
  memcpy(observed,last,sizeof last);
  return !expired() && !b->fd_failed;
}
/* C is a distinct stronger observation: every original FULL identity field
 * required by its contract must remain readable and equal on the original
 * directory. This helper never grants eligibility or reopens a descriptor. */
static inline bool census_exit_full_zidentity(struct census_budget *b,int dir,
    const struct census_owned_identity *prior) {
  if(!prior || !prior->name || !*prior->name || prior->pid<=1 || !prior->start ||
      !prior->ppid || !prior->pgid || prior->kernel_flags || expired()) return false;
  struct census_owned_identity current={0};
  if(!census_exit_zstat(b,dir,prior->pid,prior->start) ||
      !census_read_bounded_stat(b,dir,prior->pid,&current) ||
      current.start!=prior->start || current.ppid!=prior->ppid ||
      current.pgid!=prior->pgid || current.kernel_flags) return false;
  struct census_buffer comm={0};
  if(!census_buffer_read(b,dir,"comm",MAX_FIELD+1,&comm)) return false;
  if(comm.n && comm.data[comm.n-1]=='\n') comm.data[--comm.n]=0;
  bool same=!memchr(comm.data,0,comm.n) && utf8(comm.data,comm.n) &&
    strlen(prior->name)==comm.n && !memcmp(prior->name,comm.data,comm.n);
  census_buffer_clear(b,&comm);
  return same && !expired() && !b->fd_failed;
}
static inline bool census_exit_original_full_zombie(struct census_budget *b,
    int fd,int dir,const struct census_owned_identity *prior,uint32_t observed[4]) {
  if(!prior || !prior->name || !*prior->name || !prior->sid || *prior->sid ||
      !prior->city || strlen(prior->city)>4096 || strchr(prior->city,'\r') || strchr(prior->city,'\n') ||
      !prior->template || *prior->template || *prior->token || prior->epoch ||
      prior->declared_root || infra(prior->name) || prior->pid==ev.binding.pid ||
      !prior->no_gc_environment || !prior->environment_revalidated ||
      !prior->stat_revalidated || !prior->uids_revalidated || !prior->pidfd_bound)
    return false;
  return census_exit_full_zidentity(b,dir,prior) &&
    census_exit_original_zombie(b,fd,dir,prior->pid,prior->start,prior->uids,observed) &&
    census_exit_full_zidentity(b,dir,prior);
}
#endif
