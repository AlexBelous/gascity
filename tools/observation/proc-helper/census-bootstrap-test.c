/* Own unprivileged process/socket only; no root policy or activation. */
#define GC_HELPER_TEST 1
#define main census_legacy_test_main
#include "observer.c"
#undef main
#include "census-v3-reader.h"
#include "census-envelope.h"
#include "census-bootstrap.h"
#include <assert.h>
static unsigned owned_clock_calls;
static uint64_t owned_deadline_jump(void) {
  uint64_t start=(uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u;
  return start+(owned_clock_calls++?10009:9999);
}
int main(void) {
  clock_gettime(CLOCK_MONOTONIC,&ev.monotonic_start);
  clock_gettime(CLOCK_REALTIME,&ev.realtime_start);
  ev.processfd=ev.active_pidfd=-1;
  int sockets[2];assert(!socketpair(AF_UNIX,SOCK_STREAM|SOCK_CLOEXEC,0,sockets));
  if(sockets[0]!=CONNECTION_FD) {assert(dup2(sockets[0],CONNECTION_FD)==CONNECTION_FD);assert(!close(sockets[0]));}
  struct census_bootstrap b;assert(census_bootstrap_init(&b));
  size_t fixed=b.budget.used;unsigned initial_fds=b.budget.fd_live;
  assert(fixed==sizeof ev+sizeof b+CENSUS_V3_STACK_RESERVE && b.budget.fd_peak<=32);
  struct rlimit limit;assert(!getrlimit(RLIMIT_NOFILE,&limit) && limit.rlim_cur<=32);
  char boot[37];assert(census_text_v3(&b.budget,"sys/kernel/random/boot_id",boot,sizeof boot) && uuid_string(boot));
  assert(census_mount_v3(&b.budget));
  struct utsname kernel;assert(!uname(&kernel));strcpy(ev.binding.kernel_release,kernel.release);
  assert(census_kernel_v3(&b.budget));assert(b.budget.used==fixed && b.budget.fd_live==initial_fds);
  ev.binding.pid=(uint32_t)getpid();ev.binding.uid=(uint32_t)getuid();
  char path[32];snprintf(path,sizeof path,"%u",ev.binding.pid);
  int dir=fixed_open(ev.procfd,path,O_DIRECTORY);assert(dir>=0 && census_fd_take(&b.budget,dir));
  struct census_owned_identity stat={0};assert(census_read_bounded_stat(&b.budget,dir,ev.binding.pid,&stat));
  census_fd_close(&b.budget,dir);ev.binding.start=stat.start;
  assert(census_peer_v3(&b,true) && census_peer_v3(&b,false));
  b.caller.start++;assert(!census_peer_v3(&b,false));b.caller.start--;
  assert(census_peer_v3(&b,false));census_identity_clear(&b.budget,&b.caller);
  assert(b.budget.used==fixed && b.budget.fd_live==initial_fds);
  uint64_t start=(uint64_t)ev.monotonic_start.tv_sec*1000u+(uint64_t)ev.monotonic_start.tv_nsec/1000000u;
  int remaining=-1;
  assert(census_request_remaining_v3(start,&remaining) && remaining==10000);
  assert(census_request_remaining_v3(start+9999,&remaining) && remaining==1);
  assert(!census_request_remaining_v3(start+10009,&remaining) && errno==ETIMEDOUT);
  assert(!census_request_remaining_v3(start+10000,&remaining) && errno==ETIMEDOUT);
  assert(!census_request_remaining_v3(start-1,&remaining) && errno==ESTALE);
  assert(!census_request_remaining_v3(UINT64_MAX,&remaining) && errno==EIO);
  char request[256];
  const char *nonce="0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
  int n=snprintf(request,sizeof request,"{\"schema\":\"observe-host-processes/v3\",\"request_nonce\":\"%s\"}",nonce);assert(n>0);
  assert(census_parse_request_v3(request));
  const char *bad[]={"{}","{\"schema\":\"observe-host-processes/v2\"}","{\"schema\":\"observe-host-processes/v3\",\"schema\":\"observe-host-processes/v3\"}","{\"schema\":\"observe-host-processes/v3\",\"path\":\"/proc\"}","{\"schema\":\"observe-host-processes/v3\",\"request_nonce\":\"x\"}","[]"};
  for(size_t i=0;i<sizeof bad/sizeof bad[0];i++) {char data[256];strcpy(data,bad[i]);assert(!census_parse_request_v3(data));}
  uint32_t frame=htonl((uint32_t)n);assert(write(sockets[1],&frame,4)==4 && write(sockets[1],request,(size_t)n)==n);
  assert(!shutdown(sockets[1],SHUT_WR));assert(census_receive_request_v3(&b.budget));assert(!strcmp(ev.nonce,nonce));
  assert(b.budget.used==fixed && b.budget.fd_live==initial_fds && !b.budget.fd_failed);
  /* Silent own peer and an actual1ms poll, followed by the overdue clock
   * sample. No negative poll timeout may wait indefinitely after deschedule. */
  int silent[2];assert(!socketpair(AF_UNIX,SOCK_STREAM|SOCK_CLOEXEC,0,silent));
  assert(census_fd_take(&b.budget,silent[0]) && census_fd_take(&b.budget,silent[1]));
  census_fd_close(&b.budget,CONNECTION_FD);assert(dup2(silent[0],CONNECTION_FD)==CONNECTION_FD);
  assert(census_fd_take(&b.budget,CONNECTION_FD));census_fd_close(&b.budget,silent[0]);
  census_test_request_clock=owned_deadline_jump;uint64_t before=mono_ms();
  assert(!census_receive_request_v3(&b.budget));
  assert(owned_clock_calls==1 && mono_ms()-before<1000); /* poll1ms timeout itself denies. */
  census_test_request_clock=NULL;census_fd_close(&b.budget,silent[1]);
  printf("PASS owned bootstrap fixed=%zu charged_peak=%zu fd_peak=%u: NOFILE32, charged fixed proc text/mount/kernel/caller fences, changed caller DENY, V3-only strict nonce/schema request and framed EOF + single-sample positive poll bounds, expired/backward/clockfailure DENY; no root binding/install/host census\n",fixed,b.budget.peak,b.budget.fd_peak);
  census_fd_close(&b.budget,ev.procfd);assert(!close(sockets[1]));assert(!close(CONNECTION_FD));
}
