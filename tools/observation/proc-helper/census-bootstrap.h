/* Separate V3 startup source. Never delegates to V2 uncharged reads.
 * Fixed compiled paths; request remains schema+nonce only. Include observer.c
 * then census-envelope.h. No install, activation, provider or signal authority. */
#ifndef GC_CENSUS_BOOTSTRAP_H
#define GC_CENSUS_BOOTSTRAP_H
#include <sys/resource.h>
struct census_bootstrap {
  struct census_budget budget;
  struct census_round_journal journal;
  struct census_owned_identity caller;
};
/* Conservative fixed automatic-storage reservation (no recursive source calls).
 * Final production compiler stack-usage review is required before release.
 * Explicit static ev/context storage is charged in addition to this reserve. */
#define CENSUS_V3_STACK_RESERVE 65536u
static inline bool census_bootstrap_init(struct census_bootstrap *b) {
  memset(b,0,sizeof *b);
  b->budget.limit=MAX_RETAINED;
  b->budget.used=sizeof ev+sizeof *b+CENSUS_V3_STACK_RESERVE;b->budget.peak=b->budget.used;
  if(b->budget.used>=b->budget.limit) return false;
  struct rlimit limit;
  if(getrlimit(RLIMIT_NOFILE,&limit)) return false;
  if(limit.rlim_cur>32) {limit.rlim_cur=32;if(setrlimit(RLIMIT_NOFILE,&limit)) return false;}
  /* Before any startup transient opens, kernel NOFILE32 bounds inherited and
   * newly opened descriptors. First inventory counts the retained proc dir. */
  ev.procfd=open("/proc",O_RDONLY|O_DIRECTORY|O_CLOEXEC|O_NOFOLLOW);
  return ev.procfd>=0 && census_fd_initialize(&b->budget);
}
static inline int census_startup_open(struct census_budget *b,const char *path,int flags) {
  if(expired() || b->fd_failed) {errno=expired()?ETIMEDOUT:EMFILE;return -1;}
  int fd=open(path,flags|O_CLOEXEC);
  if(fd>=0 && !census_fd_take(b,fd)) {census_fd_close(b,fd);errno=EMFILE;return -1;}
  return fd;
}
static inline bool census_binding_v3(struct census_budget *b) {
  const char *parents[]={"/","/etc","/etc/gascity-observer"};
  for(size_t i=0;i<3;i++) {
    int fd=census_startup_open(b,parents[i],O_RDONLY|O_DIRECTORY|O_NOFOLLOW);if(fd<0) return false;
    struct stat st;bool ok=!fstat(fd,&st) && st.st_uid==0 && !(st.st_mode&0022);
    census_fd_close(b,fd);if(!ok || b->fd_failed) return false;
  }
  int dir=census_startup_open(b,"/etc/gascity-observer",O_RDONLY|O_DIRECTORY|O_NOFOLLOW);if(dir<0) return false;
  int fd=fixed_open(dir,"binding.conf",0);bool ok=false;unsigned char *data=NULL;
  if(fd>=0 && census_fd_take(b,fd) && root_owned(fd)) {
    data=census_alloc(b,4097);size_t n=0;
    if(data) {
      while(n<4097 && !expired()) {ssize_t got=read(fd,data+n,4097-n);if(got<0) {if(errno==EINTR) continue;break;}if(!got) {ok=n<=4096 && parse_binding(data,n);break;}n+=(size_t)got;}
    }
  }
  census_release(b,data,4097);census_fd_close(b,fd);census_fd_close(b,dir);
  return ok && !b->fd_failed && !expired();
}
static inline bool census_self_hash_v3(struct census_budget *b) {
  int fd=census_startup_open(b,"/proc/self/exe",O_RDONLY);if(fd<0) return false;
  bool ok=root_owned(fd);unsigned char buffer[32768];sha256_ctx hash;sha256_init(&hash);size_t total=0;
  while(ok) {
    if(expired()) {ok=false;break;}
    ssize_t n=read(fd,buffer,sizeof buffer);if(n<0) {if(errno==EINTR) continue;ok=false;break;}if(!n) break;
    total+=(size_t)n;if(total>32u*1024u*1024u) {ok=false;break;}sha256_update(&hash,buffer,(size_t)n);
  }
  census_fd_close(b,fd);if(ok) sha256_hex(&hash,ev.binary);
  return ok && !b->fd_failed && !strcmp(ev.binary,ev.binding.helper_binary);
}
static inline bool census_text_v3(struct census_budget *b,const char *path,char *out,size_t cap) {
  struct census_buffer data={0};if(!census_buffer_read(b,ev.procfd,path,cap+1,&data)) return false;
  while(data.n && (data.data[data.n-1]=='\n' || data.data[data.n-1]=='\r')) data.n--;
  bool ok=data.n<cap && !memchr(data.data,0,data.n);
  if(ok) {memcpy(out,data.data,data.n);out[data.n]=0;}
  census_buffer_clear(b,&data);return ok && !b->fd_failed;
}
static inline bool census_mount_v3(struct census_budget *b) {
  struct statfs st;if(fstatfs(ev.procfd,&st) || st.f_type!=PROC_SUPER_MAGIC) return false;
  char path[48];snprintf(path,sizeof path,"%u/mountinfo",(unsigned)getpid());
  struct census_buffer data={0};if(!census_buffer_read(b,ev.procfd,path,1024u*1024u,&data)) return false;
  bool ok=!memchr(data.data,0,data.n),found=false;char *save=NULL,*line=strtok_r((char *)data.data,"\n",&save);
  while(ok && line) {
    if(strstr(line," - proc ")) {found=true;if((strstr(line,"hidepid=") && !strstr(line,"hidepid=0")) || strstr(line,"subset=pid")) ok=false;}
    line=strtok_r(NULL,"\n",&save);
  }
  census_buffer_clear(b,&data);return ok && found && !b->fd_failed;
}
static inline bool census_kernel_v3(struct census_budget *b) {
  struct utsname kernel;if(uname(&kernel) || strcmp(kernel.release,ev.binding.kernel_release) || strncmp(kernel.release,"6.8.",4)) return false;
  char path[48];snprintf(path,sizeof path,"%u/status",(unsigned)getpid());
  struct census_buffer data={0};if(!census_buffer_read(b,ev.procfd,path,65536,&data)) return false;
  bool ok=!memchr(data.data,0,data.n),found=false;char *save=NULL,*line=strtok_r((char *)data.data,"\n",&save);
  while(ok && line) {
    if(!strncmp(line,"TracerPid:",10)) {uint64_t tracer=1;char *value=line+10;while(*value==' ' || *value=='\t') value++;if(found || !uint_value(value,&tracer) || tracer) ok=false;found=true;}
    line=strtok_r(NULL,"\n",&save);
  }
  census_buffer_clear(b,&data);return ok && found && !b->fd_failed;
}
static inline bool census_peer_v3(struct census_bootstrap *b,bool initial) {
  struct ucred peer;socklen_t n=sizeof peer;
  if(getsockopt(CONNECTION_FD,SOL_SOCKET,SO_PEERCRED,&peer,&n) || n!=sizeof peer ||
      peer.pid<=0 || (uint32_t)peer.pid!=ev.binding.pid || peer.uid!=ev.binding.uid) return false;
  struct census_owned_identity now={0};struct census_capture_fault fault={0};
  if(!census_capture_bounded_evidenced(ev.binding.pid,&b->budget,&now,&fault)) return false;
  bool ok=now.start==ev.binding.start && now.uids[0]==peer.uid &&
    (initial || census_equal_bounded(&b->caller,&now));
  if(ok && initial) {b->caller=now;memset(&now,0,sizeof now);}
  census_identity_clear(&b->budget,&now);return ok && !b->budget.fd_failed && !expired();
}
static inline bool census_parse_request_v3(char *b) {
  const char *s = space(b);
  if (*s++ != '{')
    return false;
  unsigned fields = 0;
  for (;;) {
    char key[64], value[128];
    s = json_string(s, key, sizeof key);
    if (!s)
      return false;
    s = space(s);
    if (*s++ != ':')
      return false;
    s = json_string(s, value, sizeof value);
    if (!s)
      return false;
    if (!strcmp(key, "schema")) {
      if ((fields & 1) || strcmp(value, "observe-host-processes/v3"))
        return false;
      fields |= 1;
    } else if (!strcmp(key, "request_nonce")) {
      if ((fields & 2) || !hex_string(value, 64))
        return false;
      fields |= 2;
      strcpy(ev.nonce, value);
    } else
      return false;
    s = space(s);
    if (*s == '}') {
      s = space(s + 1);
      return !*s && fields == 3;
    }
    if (*s++ != ',')
      return false;
  }
}
#ifdef GC_HELPER_TEST
/* Clock scheduling regression only; no production hook or requester input. */
static uint64_t (*census_test_request_clock)(void);
#endif
static inline uint64_t census_request_now_v3(void) {
#ifdef GC_HELPER_TEST
  if(census_test_request_clock) return census_test_request_clock();
#endif
  return mono_ms();
}
static inline bool census_request_remaining_v3(uint64_t now,int *remaining) {
  uint64_t start=(uint64_t)ev.monotonic_start.tv_sec*1000u+
    (uint64_t)ev.monotonic_start.tv_nsec/1000000u;
  if(now==UINT64_MAX) {errno=EIO;return false;}
  if(now<start) {errno=ESTALE;return false;}
  uint64_t elapsed=now-start;
  if(elapsed>=10000) {errno=ETIMEDOUT;return false;}
  *remaining=(int)(10000-elapsed);return true;
}
static inline bool census_receive_request_v3(struct census_budget *budget) {
  unsigned char buf[MAX_REQUEST + 5], control[CMSG_SPACE(sizeof(int) * 16)];
  size_t got = 0;
  for (;;) {
    /* Exactly one clock sample: no second-sample overflow/negative poll.
     * poll never receives zero/negative (infinite) or an unbounded cast. */
    int remaining=0;
    if(budget->fd_failed || !census_request_remaining_v3(census_request_now_v3(),&remaining)) return false;
    struct pollfd p = {.fd = CONNECTION_FD, .events = POLLIN};
    if (poll(&p, 1, remaining) <= 0)
      return false;
    struct iovec iov = {.iov_base = buf + got, .iov_len = sizeof buf - got};
    struct msghdr msg = {.msg_iov = &iov,
                         .msg_iovlen = 1,
                         .msg_control = control,
                         .msg_controllen = sizeof control};
    ssize_t n = recvmsg(CONNECTION_FD, &msg, MSG_CMSG_CLOEXEC);
    if (n < 0)
      return false;
    bool ancillary =
        (msg.msg_flags & MSG_CTRUNC) || CMSG_FIRSTHDR(&msg) != NULL;
    for (struct cmsghdr *c = CMSG_FIRSTHDR(&msg); c; c = CMSG_NXTHDR(&msg, c))
      if (c->cmsg_level == SOL_SOCKET && c->cmsg_type == SCM_RIGHTS) {
        size_t z = (c->cmsg_len - CMSG_LEN(0)) / sizeof(int);
        int *fds = (int *)CMSG_DATA(c);
        for (size_t i = 0; i < z; i++) {
          (void)census_fd_take(budget,fds[i]);census_fd_close(budget,fds[i]);
        }
      }
    if (ancillary)
      return false;
    if (!n)
      break;
    got += (size_t)n;
    if (got == sizeof buf)
      return false;
    if (got >= 4) {
      uint32_t len;
      memcpy(&len, buf, 4);
      len = ntohl(len);
      if (!len || len > MAX_REQUEST || got > (size_t)len + 4)
        return false;
    }
  }
  if (got < 4)
    return false;
  uint32_t len;
  memcpy(&len, buf, 4);
  len = ntohl(len);
  if (got != (size_t)len + 4 || memchr(buf + 4, 0, len))
    return false;
  buf[got] = 0;
  return census_parse_request_v3((char *)buf + 4);
}

#endif
